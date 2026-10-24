package service

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"maps"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/google/uuid"
	"github.com/tidwall/gjson"
)

// 本文件是主动打票链路：
//   扫描循环（harvestLoop / refreshTickets）→ 单账号单模型尝试（runAttempt）
//   → 代理池轮换（chooseProxy）→ 探测请求（buildProbeRequest / fireProbe）
//   → 票据形状校验并落库 → 成功按周期刷新、失败按区间抖动重试。
//
// 打票只使用账号自身 OAuth 凭据；探测请求走本地「IP 设置」代理池轮换，
// 业务转发仍走账号原代理，二者互不影响（只共享代理记录）。

const (
	// codexTicketVerificationShape 是兜底验真方式：只校验票据形状
	// （gAAAAA 前缀 + 账号规则长度）。
	codexTicketVerificationShape = "ticket_shape_v1"
	// codexTicketVerificationFingerprint 表示票据同时通过了 ModelTrace 指纹验真
	// （探测输出判定为请求模型本身或其同系列参考类）。
	codexTicketVerificationFingerprint = "ticket_fingerprint_v1"

	codexTicketTriggerAutomatic = "automatic"
	codexTicketTriggerManual    = "manual"
	codexTicketOutcomeSuccess   = "success"
	codexTicketOutcomeMiss      = "miss"
	codexTicketOutcomeError     = "error"

	// codexTicketProbeMaxBodyBytes 限制探测响应读取量，防止异常上游拖垮打票循环。
	codexTicketProbeMaxBodyBytes = 2 << 20
)

// CodexTicketHarvestEnabled 判断账号/模型是否参与打票（只看已保存选择，不看运行时可用性）。
func CodexTicketHarvestEnabled(account *Account, model string) bool {
	return isCodexTicketAccount(account) && CodexTicketParticipationEnabled(account, model)
}

// harvestLoop 是后台扫描循环：定时扫描 + 被 notifyHarvester 唤醒。
func (s *CodexTicketService) harvestLoop(ctx context.Context) {
	timer := time.NewTimer(0)
	defer timer.Stop()
	lastCleanup := time.Time{}
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.wakeChannel():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(0)
		case <-timer.C:
			s.refreshTickets(ctx)
			if s.attempts != nil && time.Since(lastCleanup) >= codexTicketHistoryCleanupInterval {
				lastCleanup = time.Now()
				cleanupCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
				if err := s.attempts.Cleanup(cleanupCtx); err != nil && ctx.Err() == nil {
					logger.LegacyPrintf("service.codex_ticket", "[CodexTicket] history cleanup failed: %v", err)
				}
				cancel()
			}
			timer.Reset(s.nextScanDelay())
		}
	}
}

// wakeChannel 返回唤醒通道（未启动时返回 nil，select 会一直阻塞在其它分支）。
func (s *CodexTicketService) wakeChannel() <-chan struct{} {
	s.startMu.Lock()
	defer s.startMu.Unlock()
	return s.wake
}

// nextScanDelay 计算下一轮扫描间隔：配置周期与「最近一次重试时间」取较小值，
// 保证 10-30 秒的重试不会被 6 秒扫描周期以外的抖动拖长。
func (s *CodexTicketService) nextScanDelay() time.Duration {
	delay := time.Duration(s.baseConfig().HarvestProbeIntervalSeconds) * time.Second
	now := time.Now()
	s.nextRetry.Range(func(_, value any) bool {
		next, ok := value.(time.Time)
		if !ok {
			return true
		}
		if until := next.Sub(now); until > 0 && until < delay {
			delay = until
		}
		return true
	})
	if delay < time.Second {
		return time.Second
	}
	return delay
}

// refreshTickets 扫描全部 OpenAI 账号，对「缺票/临期/到重试点」的 (账号, 模型) 各打一发。
func (s *CodexTicketService) refreshTickets(ctx context.Context) {
	if s == nil || s.accountRepo == nil || ctx.Err() != nil {
		return
	}
	settings := s.loadSettings(ctx)
	if !settings.enabled {
		return
	}
	accounts, err := s.accountRepo.ListByPlatform(ctx, PlatformOpenAI)
	if err != nil {
		logger.LegacyPrintf("service.codex_ticket", "[CodexTicket] list accounts failed: %v", err)
		return
	}
	now := time.Now()
	sem := make(chan struct{}, codexTicketHarvestConcurrency)
	var wg sync.WaitGroup
	probed := 0
	for i := range accounts {
		account := accounts[i]
		if !isCodexTicketAccount(&account) || account.Status != StatusActive {
			continue
		}
		if codexTicketQuota(&account, now).paused {
			continue
		}
		for _, configuredModel := range settings.cfg.Models {
			model := normalizeCodexTicketModel(configuredModel)
			if model == "" || !CodexTicketParticipationEnabled(&account, model) {
				continue
			}
			if codexTicketHarvestLimited(&account, model, now) {
				continue
			}
			if !s.automaticDue(&account, model, now) {
				continue
			}
			// 每个模型持有独立的 Extra/Credentials 快照：storeTicket 会写 Extra，
			// 同一账号的多个模型并发探测时不能共享 map。
			acc := account
			acc.Extra = maps.Clone(account.Extra)
			acc.Credentials = maps.Clone(account.Credentials)
			probed++
			wg.Add(1)
			sem <- struct{}{}
			go func(acc Account, model string) {
				defer wg.Done()
				defer func() { <-sem }()
				s.probeOnce(ctx, &acc, model)
			}(acc, model)
		}
	}
	wg.Wait()
	if probed > 0 {
		logger.LegacyPrintf("service.codex_ticket", "[CodexTicket] probe cycle: %d attempt(s)", probed)
	}
}

// probeOnce 是自动扫描的单次尝试：结果只记流水/日志，不向外抛错。
func (s *CodexTicketService) probeOnce(ctx context.Context, account *Account, model string) {
	if s == nil || ctx.Err() != nil || !CodexTicketHarvestEnabled(account, model) {
		return
	}
	now := time.Now()
	if codexTicketQuota(account, now).paused || codexTicketHarvestLimited(account, model, now) {
		return
	}
	result, err := s.runAttempt(ctx, account, model, codexTicketTriggerAutomatic)
	if err != nil {
		if !errors.Is(err, ErrCodexTicketBusy) && !errors.Is(err, ErrCodexTicketNoProxy) && ctx.Err() == nil {
			logger.LegacyPrintf("service.codex_ticket", "[CodexTicket] probe unavailable account=%d model=%s err=%v", account.ID, model, err)
		}
		return
	}
	if result.Outcome == codexTicketOutcomeSuccess {
		logger.LegacyPrintf("service.codex_ticket", "[CodexTicket] harvested account=%d model=%s http=%v", account.ID, model, derefInt(result.HTTPStatus))
	} else {
		logger.LegacyPrintf("service.codex_ticket", "[CodexTicket] probe miss account=%d model=%s reason=%s", account.ID, model, result.ReasonCode)
	}
}

// automaticDue 判断自动扫描是否应该为该 (账号, 模型) 打票：
//   - 有重试计划 → 到点才打；
//   - 有可用票据 → 只有超过主动刷新周期才重打（RefreshSeconds<=0 表示不主动刷新）；
//   - 无票据 → 立即打。
func (s *CodexTicketService) automaticDue(account *Account, model string, now time.Time) bool {
	if account == nil {
		return false
	}
	if !s.retryDue(account.ID, model, now) {
		return false
	}
	ticket := s.lookupTicket(account, model)
	if ticket == nil {
		return true
	}
	if usable, _ := ticket.usable(now); !usable {
		return true
	}
	refreshSeconds := s.cachedSettings().cadence.RefreshSeconds
	if refreshSeconds <= 0 {
		return false
	}
	return !now.Before(ticket.CapturedAt.Add(time.Duration(refreshSeconds) * time.Second))
}

// scheduleAfterSuccess 按主动刷新周期安排下一次重打。
func (s *CodexTicketService) scheduleAfterSuccess(ticket *codexTicket) {
	if ticket == nil {
		return
	}
	key := codexTicketCacheKey(ticket.AccountID, ticket.Model)
	refreshSeconds := s.cachedSettings().cadence.RefreshSeconds
	if refreshSeconds <= 0 {
		s.nextRetry.Delete(key)
		return
	}
	s.nextRetry.Store(key, ticket.CapturedAt.Add(time.Duration(refreshSeconds)*time.Second))
}

// scheduleAfterFailure 在 [RetryMin, RetryMax] 区间内按 key+时间抖动安排重试。
func (s *CodexTicketService) scheduleAfterFailure(accountID int64, model string, at time.Time) {
	cadence := s.cachedSettings().cadence
	minDelay := time.Duration(cadence.RetryMinSeconds) * time.Second
	maxDelay := time.Duration(cadence.RetryMaxSeconds) * time.Second
	s.scheduleRetry(accountID, model, at.Add(codexTicketJitter(codexTicketCacheKey(accountID, model), at, minDelay, maxDelay)))
}

// codexTicketJitter 用 FNV-1a 生成稳定的伪随机抖动，避免同批账号同时重试。
func codexTicketJitter(key string, at time.Time, minDelay, maxDelay time.Duration) time.Duration {
	if maxDelay <= minDelay {
		return minDelay
	}
	h := fnv.New64a()
	_, _ = h.Write([]byte(key))
	_, _ = h.Write([]byte(at.UTC().Format(time.RFC3339Nano)))
	return minDelay + time.Duration(h.Sum64()%uint64(maxDelay-minDelay+1))
}

// chooseProxy 从打票代理池按 (账号, 模型) 轮换选择代理。
// 轮换状态保存在进程内：同一 key 连续打票会依次命中不同代理，避免长期固定出口。
func (s *CodexTicketService) chooseProxy(ctx context.Context, accountID int64, model string) (string, *Proxy, error) {
	if s == nil || s.settingSvc == nil {
		return "", nil, ErrCodexTicketNoProxy
	}
	proxies, err := s.settingSvc.AvailableCodexTicketProxies(ctx)
	if err != nil {
		return "", nil, err
	}
	if len(proxies) == 0 {
		return "", nil, ErrCodexTicketNoProxy
	}
	key := codexTicketCacheKey(accountID, model)
	h := fnv.New32a()
	_, _ = h.Write([]byte(key))
	seed := uint64(h.Sum32())
	value, _ := s.proxyTurns.LoadOrStore(key, &atomic.Uint64{})
	counter, ok := value.(*atomic.Uint64)
	if !ok || counter == nil {
		counter = &atomic.Uint64{}
		s.proxyTurns.Store(key, counter)
	}
	turn := counter.Add(1) - 1
	selected := proxies[(seed+turn)%uint64(len(proxies))]
	return selected.URL(), &selected, nil
}

// ManualHarvest 是管理端手动打票入口。
func (s *CodexTicketService) ManualHarvest(ctx context.Context, accountID int64, model string) (CodexTicketHarvestResult, error) {
	var empty CodexTicketHarvestResult
	if s == nil {
		return empty, ErrCodexTicketUnavailable
	}
	model = normalizeCodexTicketModel(model)
	if !s.gatedModel(model) {
		return empty, ErrCodexTicketModel
	}
	settings := s.runtimeSettings(ctx)
	if !settings.enabled {
		return empty, ErrCodexTicketUnavailable
	}
	if s.accountRepo == nil {
		return empty, ErrCodexTicketUnavailable
	}
	account, err := s.accountRepo.GetByID(ctx, accountID)
	if err != nil {
		return empty, err
	}
	if !isCodexTicketAccount(account) || account.Status != StatusActive || !CodexTicketParticipationEnabled(account, model) {
		return empty, ErrCodexTicketUnavailable
	}
	if codexTicketHarvestLimited(account, model, time.Now()) {
		return empty, ErrCodexTicketRateLimited
	}
	result, err := s.runAttempt(ctx, account, model, codexTicketTriggerManual)
	if err != nil {
		return result, err
	}
	// runAttempt 内部会重新读库并写入票据，这里刷新账号快照再取状态。
	if refreshed, refreshErr := s.accountRepo.GetByID(ctx, accountID); refreshErr == nil {
		account = refreshed
	}
	result.TicketStatus = s.TicketStatus(account, model)
	s.notifyHarvester()
	return result, nil
}

// runAttempt 执行一次完整打票尝试，并把流水写库、安排下一次重试。
// 返回的 error 只表示「无法开始/进行尝试」；探测本身的失败通过 result.Outcome 表达。
func (s *CodexTicketService) runAttempt(ctx context.Context, account *Account, model, trigger string) (result CodexTicketHarvestResult, err error) {
	if s == nil || account == nil || account.ID <= 0 {
		return result, ErrCodexTicketUnavailable
	}
	model = normalizeCodexTicketModel(model)
	key := codexTicketCacheKey(account.ID, model)
	if _, loaded := s.inflight.LoadOrStore(key, struct{}{}); loaded {
		return result, ErrCodexTicketBusy
	}
	defer s.inflight.Delete(key)
	// 跨副本互斥：同一 (账号, 模型) 在任一时刻只允许一发探测。
	if s.attempts != nil {
		unlock, acquired, lockErr := s.attempts.TryLock(ctx, account.ID, model)
		if lockErr != nil {
			return result, lockErr
		}
		if !acquired {
			return result, ErrCodexTicketBusy
		}
		defer unlock()
	}
	current := account
	if s.accountRepo != nil {
		loaded, loadErr := s.accountRepo.GetByID(ctx, account.ID)
		if loadErr != nil {
			return result, loadErr
		}
		current = loaded
	}
	if current.Status != StatusActive || !CodexTicketHarvestEnabled(current, model) {
		return result, ErrCodexTicketUnavailable
	}
	now := time.Now()
	if codexTicketHarvestLimited(current, model, now) {
		return result, ErrCodexTicketRateLimited
	}
	settings := s.runtimeSettings(ctx)
	start := time.Now()
	attempt := &CodexTicketAttempt{
		AccountID:          current.ID,
		Model:              model,
		Trigger:            trigger,
		VerificationMethod: codexTicketVerificationShape,
	}
	defer func() {
		attempt.OccurredAt = time.Now()
		attempt.DurationMS = int(attempt.OccurredAt.Sub(start).Milliseconds())
		if s.attempts != nil {
			writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			if insertErr := s.attempts.Insert(writeCtx, attempt); insertErr != nil {
				logger.LegacyPrintf("service.codex_ticket", "[CodexTicket] history persist failed account=%d model=%s err=%v", current.ID, model, insertErr)
			} else {
				result.HistoryRecorded = true
			}
			cancel()
		}
		if attempt.Outcome != codexTicketOutcomeSuccess {
			s.scheduleAfterFailure(current.ID, model, attempt.OccurredAt)
		}
		result.CodexTicketAttempt = *attempt
	}()
	// 1) 代理池
	proxyURL, proxy, proxyErr := s.chooseProxy(ctx, current.ID, model)
	if proxyErr != nil {
		attempt.Outcome, attempt.ReasonCode = codexTicketOutcomeError, "proxy_unavailable"
		return result, proxyErr
	}
	attempt.ProxyID, attempt.ProxyName = &proxy.ID, proxy.Name
	// 2) OAuth access token（可刷新时由网关 TokenProvider 提供）
	token, tokenErr := s.accessToken(ctx, current)
	if tokenErr != nil || strings.TrimSpace(token) == "" {
		attempt.Outcome, attempt.ReasonCode = codexTicketOutcomeError, "credentials"
		return result, nil
	}
	// 3) 探测挑战
	challenge, challengeErr := newCodexTicketProbeChallenge()
	if challengeErr != nil {
		attempt.Outcome, attempt.ReasonCode = codexTicketOutcomeError, "challenge_generation_failed"
		return result, nil
	}
	attempt.ChallengeExpectedCount = &challenge.ExpectedCount
	// 4) 打一发
	output, state, cookie, status, probeErr := s.fireProbe(ctx, current, token, model, proxyURL, challenge, settings)
	if status != 0 {
		attempt.HTTPStatus = &status
		length := len(state)
		attempt.TicketLength = &length
		turnPresent, cookiePresent := strings.TrimSpace(state) != "", strings.TrimSpace(cookie) != ""
		attempt.TurnStatePresent, attempt.CookiePresent = &turnPresent, &cookiePresent
	}
	if probeErr != nil {
		attempt.Outcome, attempt.ReasonCode = codexTicketOutcomeError, "request"
		if errors.Is(probeErr, context.DeadlineExceeded) {
			attempt.ReasonCode = "timeout"
		}
		return result, nil
	}
	parsedCount := codexTicketCountChallengeNumbers(output)
	attempt.ParsedNumberCount = &parsedCount
	if status != http.StatusOK && status != http.StatusTooManyRequests {
		attempt.Outcome, attempt.ReasonCode = codexTicketOutcomeMiss, "http_status"
		return result, nil
	}
	// 5) 票据形状校验：前缀 + 账号规则长度（team 332，其余 292）
	if !codexTicketStateValid(current, settings.cfg.TargetLength, state) {
		attempt.Outcome = codexTicketOutcomeMiss
		if strings.TrimSpace(state) == "" {
			attempt.ReasonCode = "missing_turn_state"
		} else {
			attempt.ReasonCode = "ticket_shape"
		}
		return result, nil
	}
	// 5.5) 指纹验真：用同一挑战的输出判定实际服务模型。命中其它模型（degraded）
	// 说明上游把请求降级到了别的模型，票据不可信，不落库；无法解析（failed）或
	// 指纹库缺类（uncertain）不拦截，保留形状验真结论，避免误杀健康账号。
	verificationMethod := codexTicketVerificationShape
	fingerprint := EvaluateCodexDiagnostic(model, output, ModelTraceChallenge(challenge))
	switch fingerprint.Status {
	case CodexDegradationStatusDegraded:
		attempt.Outcome, attempt.ReasonCode = codexTicketOutcomeMiss, "fingerprint_degraded"
		return result, nil
	case CodexDegradationStatusNormal, CodexDegradationStatusUncertain:
		verificationMethod = codexTicketVerificationFingerprint
	}
	attempt.VerificationMethod = verificationMethod
	// 6) 落库
	capturedAt := time.Now()
	generation := uuid.NewString()
	ticket := &codexTicket{
		AccountID:          current.ID,
		Model:              model,
		GenerationID:       generation,
		VerificationMethod: verificationMethod,
		State:              state,
		Cookie:             cookie,
		Length:             len(state),
		CapturedAt:         capturedAt,
		ExpiresAt:          capturedAt.Add(time.Duration(settings.cfg.TTLSeconds) * time.Second),
		HTTPStatus:         status,
	}
	if storeErr := s.storeTicket(ctx, current, ticket); storeErr != nil {
		attempt.Outcome, attempt.ReasonCode = codexTicketOutcomeError, "persistence"
		return result, nil
	}
	s.scheduleAfterSuccess(ticket)
	attempt.Outcome, attempt.TicketGenerationID = codexTicketOutcomeSuccess, &generation
	expiresAt := ticket.ExpiresAt
	attempt.ExpiresAt = &expiresAt
	return result, nil
}

// accessToken 获取打票用的 OAuth access token。
func (s *CodexTicketService) accessToken(ctx context.Context, account *Account) (string, error) {
	if s.tokenProvider != nil {
		token, _, err := s.tokenProvider.GetAccessToken(ctx, account)
		return strings.TrimSpace(token), err
	}
	token := strings.TrimSpace(account.GetOpenAIAccessToken())
	if token == "" {
		return "", errors.New("access_token not found in credentials")
	}
	return token, nil
}

// fireProbe 构造并执行一次打票探测，返回 (模型输出, turn-state, 基础设施 Cookie, HTTP 状态, error)。
func (s *CodexTicketService) fireProbe(ctx context.Context, account *Account, token, model, proxyURL string, challenge codexTicketProbeChallenge, settings *codexTicketRuntimeSettings) (output, state, cookie string, status int, err error) {
	timeout := time.Duration(settings.cfg.HarvestAttemptTimeoutSeconds) * time.Second
	attemptCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	body, headers, buildErr := s.buildProbeRequest(attemptCtx, account, model, challenge)
	if buildErr != nil {
		return "", "", "", 0, buildErr
	}
	probeURL := s.probeURL
	if probeURL == "" {
		probeURL = chatgptCodexURL
	}
	req, reqErr := http.NewRequestWithContext(attemptCtx, http.MethodPost, probeURL, bytes.NewReader(body))
	if reqErr != nil {
		return "", "", "", 0, reqErr
	}
	for name, values := range headers {
		req.Header[name] = values
	}
	req.Host = "chatgpt.com"
	req.Header.Set("Authorization", "Bearer "+token)
	req.Close = true
	resp, doErr := s.doUpstream(req, proxyURL, account)
	if doErr != nil {
		return "", "", "", 0, doErr
	}
	if resp == nil {
		return "", "", "", 0, errors.New("nil upstream response")
	}
	defer func() {
		if resp.Body != nil {
			_ = resp.Body.Close()
		}
	}()
	state = strings.TrimSpace(resp.Header.Get(openAICodexTurnStateHeader))
	cookie = extractCodexTicketResponseCookies(resp.Header)
	status = resp.StatusCode
	if resp.Body == nil {
		return "", state, cookie, status, nil
	}
	output, readErr := readCodexTicketProbeStream(attemptCtx, resp.Body)
	if readErr != nil {
		return output, state, cookie, status, readErr
	}
	return output, state, cookie, status, nil
}

// doUpstream 走网关共用的 HTTP 执行器；未注入时用内置 client（测试/降级）。
func (s *CodexTicketService) doUpstream(req *http.Request, proxyURL string, account *Account) (*http.Response, error) {
	if s.upstream != nil {
		return s.upstream.Do(req, proxyURL, account.ID, account.Concurrency)
	}
	client := s.fallbackHTTP
	if strings.TrimSpace(proxyURL) != "" {
		parsed, err := url.Parse(proxyURL)
		if err != nil {
			return nil, fmt.Errorf("parse codex ticket proxy: %w", err)
		}
		client = &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(parsed)}}
	}
	if client == nil {
		client = http.DefaultClient
	}
	return client.Do(req)
}

// buildProbeRequest 构造与真实 Codex 回合形状一致的探测请求。
// 只复用仓库既有的身份工具函数，不依赖 P4 的指纹/模板代码。
func (s *CodexTicketService) buildProbeRequest(ctx context.Context, account *Account, model string, challenge codexTicketProbeChallenge) ([]byte, http.Header, error) {
	if account == nil {
		return nil, nil, ErrCodexTicketUnavailable
	}
	if codexAccountIdentityNamespace(account) == "" {
		return nil, nil, errors.New("codex account identity unavailable")
	}
	now := time.Now()
	installationID := scopeCodexAccountIdentityValue(account, 0, "installation", "codex-ticket-probe-v1")
	// 合成身份必须与官方 now_v7 同构：session/turn/context-window 用 v7（时间戳为
	// 当前时刻，与 turn_started_at_unix_ms 自洽），window 为 <thread>:<ordinal>。
	sessionID := scopeCodexAccountIdentityValue(account, 0, "session", newCodexUUIDv7String())
	threadID := sessionID
	turnID := scopeCodexAccountIdentityValue(account, 0, "turn", newCodexUUIDv7String())
	windowID := threadID + ":0"
	contextWindowID := scopeCodexAccountIdentityValue(account, 0, "context-window", newCodexUUIDv7String())
	metadata, err := marshalCodexTurnMetadata(map[string]any{
		"installation_id":         installationID,
		"session_id":              sessionID,
		"thread_id":               threadID,
		"turn_id":                 turnID,
		"window_id":               windowID,
		"window_number":           0,
		"context_window_id":       contextWindowID,
		"turn_started_at_unix_ms": now.UnixMilli(),
		"request_kind":            "turn",
	})
	if err != nil {
		metadata = []byte("{}")
	}
	body := map[string]any{
		"model":        model,
		"store":        false,
		"stream":       true,
		"instructions": "You are Codex, a coding agent. Follow the user's instruction exactly. Do not use any tools for this task.",
		"input": []any{
			map[string]any{
				"role": "developer",
				"content": []any{map[string]string{
					"type": "input_text",
					"text": "You are a helpful coding assistant. Follow the user's instruction exactly.",
				}},
			},
			map[string]any{
				"role": "user",
				"content": []any{map[string]string{
					"type": "input_text",
					"text": challenge.Prompt,
				}},
			},
		},
		"prompt_cache_key":    sessionID,
		"parallel_tool_calls": false,
		"tool_choice":         "auto",
		"tools":               []any{},
		"reasoning":           map[string]any{"effort": "low", "summary": "auto"},
		"include":             []string{"reasoning.encrypted_content"},
		"client_metadata": map[string]string{
			"session_id":              sessionID,
			"thread_id":               threadID,
			"turn_id":                 turnID,
			"x-codex-installation-id": installationID,
			"x-codex-window-id":       windowID,
			"x-codex-turn-metadata":   string(metadata),
		},
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return nil, nil, err
	}
	headers := http.Header{}
	headers.Set("Content-Type", "application/json")
	headers.Set("Accept", "text/event-stream")
	headers.Set("OpenAI-Beta", "responses=experimental")
	for _, key := range []string{"session_id", "session-id", "thread-id", "x-client-request-id"} {
		headers.Set(key, sessionID)
	}
	headers.Set("x-codex-installation-id", installationID)
	headers.Set("x-codex-window-id", windowID)
	headers.Set("x-codex-turn-metadata", string(metadata))
	applyOpenAICodexProbeHeaders(headers)
	enforceCodexIdentityHeaders(headers)
	if err := resolveAndSetOpenAIChatGPTAccountHeaders(ctx, s.accountRepo, headers, account); err != nil {
		return nil, nil, err
	}
	return encoded, headers, nil
}

// readCodexTicketProbeStream 读取探测响应（SSE）里的模型输出文本。
// 只关心 output_text.delta 与 response.completed 的 output_text，忽略其它事件。
func readCodexTicketProbeStream(ctx context.Context, body io.Reader) (string, error) {
	if body == nil {
		return "", nil
	}
	scanner := bufio.NewScanner(io.LimitReader(body, codexTicketProbeMaxBodyBytes))
	scanner.Buffer(make([]byte, 4096), 1<<20)
	var deltas strings.Builder
	var completed strings.Builder
	for scanner.Scan() {
		if ctx != nil && ctx.Err() != nil {
			return "", ctx.Err()
		}
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" || payload == "[DONE]" {
			if payload == "[DONE]" {
				break
			}
			continue
		}
		switch gjson.Get(payload, "type").String() {
		case "response.output_text.delta":
			_, _ = deltas.WriteString(gjson.Get(payload, "delta").String())
		case "response.completed":
			gjson.Get(payload, "response.output").ForEach(func(_, message gjson.Result) bool {
				message.Get("content").ForEach(func(_, part gjson.Result) bool {
					if part.Get("type").String() == "output_text" {
						_, _ = completed.WriteString(part.Get("text").String())
					}
					return true
				})
				return true
			})
		}
	}
	if err := scanner.Err(); err != nil {
		return deltas.String(), err
	}
	if deltas.Len() > 0 {
		return deltas.String(), nil
	}
	return completed.String(), nil
}

// derefInt 是日志辅助函数。
func derefInt(value *int) int {
	if value == nil {
		return 0
	}
	return *value
}

// ExtractCodexTicketModel 从请求体提取出站模型名，供转发路径在调用 Apply 前使用。
// 返回值为归一化（去空白）后的模型名；空串表示无法判定，调用方应跳过注入。
func ExtractCodexTicketModel(body []byte) string {
	return normalizeCodexTicketModel(gjson.GetBytes(body, "model").String())
}
