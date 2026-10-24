package service

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/google/uuid"
)

// 本文件是真实流量观测：转发路径拿到上游响应后调用 Observe / ObserveResponse。
//
// 两件事：
//  1. 捕获：上游响应头里出现结构合法的新票据时，按 账号×模型 落库（真实回合是
//     票据的第一来源，比主动打票更贴近客户端真实身份）；
//  2. 失效：若本请求注入过票据，而上游返回了**不同**的 turn-state 且 __oailb
//     路由 Cookie 同时变化，说明这一代票据已被上游拒绝/轮换。写失效流水（按
//     generation 去重），并原子清掉当前这一代票据，交给后台重打。
//
// 只有「注入票据的请求」才可能触发失效判定：客户端自己回带的票据属于其真实回合
// 血统，服务端不参与、也不判定。

// Observe 是转发路径的观测入口（契约签名）。status 未知时传 0、route 默认 /v1/responses。
func (s *CodexTicketService) Observe(ctx context.Context, account *Account, model string, respHeader http.Header) {
	s.ObserveResponse(ctx, account, model, 0, "/v1/responses", respHeader)
}

// ObserveResponse 是带响应状态与路由的观测入口，供 Lead 在转发路径接入以获得更准的失效元数据。
func (s *CodexTicketService) ObserveResponse(ctx context.Context, account *Account, model string, status int, route string, respHeader http.Header) {
	if s == nil || respHeader == nil || !isCodexTicketAccount(account) {
		return
	}
	settings := s.runtimeSettings(ctx)
	if !settings.enabled {
		return
	}
	model = normalizeCodexTicketModel(model)
	if !s.gatedModel(model) {
		return
	}
	returned := strings.TrimSpace(respHeader.Get(openAICodexTurnStateHeader))
	if returned == "" {
		return
	}
	current := s.lookupTicket(account, model)
	injection := s.takeTicketInjection(account.ID, model)
	// 1) 失效判定：只针对「我们注入过、且上游换掉了」的票据。
	if injection != nil && returned != injection.state {
		s.recordTicketInvalidation(ctx, account, model, injection, status, route, returned, respHeader)
	}
	// 2) 捕获：形状合法且与当前票据不同才落库，避免每个回合重复写 extra。
	if !codexTicketStateValid(account, settings.cfg.TargetLength, returned) {
		return
	}
	cookie := extractCodexTicketResponseCookies(respHeader)
	if current != nil && current.State == returned {
		// 同一代票据（客户端回带/上游原样返回）：不重复落库。
		return
	}
	capturedAt := time.Now()
	ticket := &codexTicket{
		AccountID:          account.ID,
		Model:              model,
		GenerationID:       uuid.NewString(),
		VerificationMethod: codexTicketVerificationShape,
		State:              returned,
		Cookie:             cookie,
		Length:             len(returned),
		CapturedAt:         capturedAt,
		ExpiresAt:          capturedAt.Add(time.Duration(settings.cfg.TTLSeconds) * time.Second),
		HTTPStatus:         status,
	}
	if err := s.storeTicket(ctx, account, ticket); err != nil {
		logger.LegacyPrintf("service.codex_ticket", "[CodexTicket] observe capture failed account=%d model=%s err=%v", account.ID, model, err)
		return
	}
	s.scheduleAfterSuccess(ticket)
}

// recordTicketInvalidation 判定并持久化一条失效事件。
func (s *CodexTicketService) recordTicketInvalidation(ctx context.Context, account *Account, model string, injection *codexTicketInjectionSnapshot, status int, route, returned string, respHeader http.Header) {
	if ctx == nil {
		ctx = context.Background()
	}
	if s.lifecycle == nil || injection == nil || injection.generationID == "" {
		return
	}
	sentCookies, err := http.ParseCookie(injection.cookie)
	if err != nil {
		return
	}
	originalOAILB, originalOK := codexOAILBValue(sentCookies)
	returnedOAILB, returnedOK := codexResponseOAILB(respHeader)
	// 只有「turn-state 与 __oailb 同时变化」才算失效证据：
	// 单独换 turn-state 是正常的新回合铸造，单独换路由 Cookie 是 CDN 侧行为。
	if !originalOK || !returnedOK || originalOAILB == returnedOAILB {
		return
	}
	if strings.TrimSpace(route) == "" {
		route = "/v1/responses"
	}
	occurredAt := time.Now().UTC()
	event := &CodexTicketInvalidation{
		AccountID:          account.ID,
		Model:              model,
		TicketGenerationID: injection.generationID,
		OccurredAt:         occurredAt,
		ReasonCode:         CodexTicketInvalidationCredentialsChanged,
		RequestKind:        "user_request",
		RequestRoute:       route,
		OriginalTicket:     optionalCodexTicketCredential(injection.state),
		OriginalCookie:     optionalCodexTicketCredential(injection.cookie),
		ReturnedTicket:     returned,
		ReturnedCookie:     optionalCodexTicketCredential(extractCodexTicketResponseCookies(respHeader)),
		ReturnedSetCookies: append([]string(nil), respHeader.Values("Set-Cookie")...),
	}
	if status > 0 {
		event.ResponseHTTPStatus = &status
	}
	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	inserted, insertErr := s.lifecycle.Invalidate(writeCtx, event)
	if insertErr != nil {
		logger.LegacyPrintf("service.codex_ticket", "[CodexTicket] invalidation persist failed account=%d model=%s err=%v", account.ID, model, insertErr)
		return
	}
	// 未插入说明同一代票据的失效事件已经记过（并发请求/多副本）：去重后直接返回。
	if !inserted {
		return
	}
	if !event.InvalidatedCurrent {
		return
	}
	s.removeCachedTicket(account.ID, model)
	// 同步清掉本请求账号快照里的旧票：调度缓存可能仍持有该快照，
	// 不清会让后续请求继续注入已失效的票据。
	if account.Extra != nil {
		delete(account.Extra, codexTicketExtraKey(model))
	}
	s.nextRetry.Delete(codexTicketCacheKey(account.ID, model))
	logger.LegacyPrintf("service.codex_ticket", "[CodexTicket] ticket invalidated account=%d model=%s", account.ID, model)
	s.notifyHarvester()
}

// optionalCodexTicketCredential 把空串转为 nil，便于 JSON 中省略。
func optionalCodexTicketCredential(value string) *string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return &value
}
