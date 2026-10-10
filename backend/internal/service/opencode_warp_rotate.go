package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"golang.org/x/sync/singleflight"

	"github.com/Wei-Shaw/sub2api/internal/config"
)

// WarpExitRotator 为 opencode_go 免费层 429 提供「换一个 WARP 出口 IP」的能力。
//
// 语义：EnsureFreshExit 返回后，proxyID 对应的代理端点（URL 不变）背后的出口 IP
// 已被确认可用；返回值为当前出口 IP（用于日志与 ops 事件）。
//
// 注意代理绑定本身不变：SOCKS5 端点始终是同一个，变的只是它背后的 WARP 出口。
// 因此本能力不修改账号 credentials，也不重写 proxy 绑定。
//
// 实现由 repository 层提供（HTTP 客户端调用外部轮换服务），测试可用替身注入。
type WarpExitRotator interface {
	EnsureFreshExit(ctx context.Context, proxyID int64, proxyURL string) (exitIP string, err error)
}

// openCodeWarpRotateCredential 是账号级开关的凭据键，沿用 credentials.free_tier_gate
// 的凭据扩展范式（不新增数据库列、不需要迁移）。
//
// 取值：
//   - "on"：该账号显式开启（全局 enabled=false 时也可用，便于灰度）。
//   - "off"：该账号显式关闭（即使全局已开启）。
//   - 缺失 / "auto" / 非法值：跟随全局 gateway.opencode_warp_rotate.enabled。
const openCodeWarpRotateCredential = "warp_rotate_on_429"

const (
	openCodeWarpRotateModeOn   = "on"
	openCodeWarpRotateModeOff  = "off"
	openCodeWarpRotateModeAuto = "auto"
)

// openCodeFreeUsageLimitErrorType 是 opencode 免费层配额耗尽的错误类型。
// 生产响应体形如：
//
//	{"type":"error","error":{"type":"FreeUsageLimitError","message":"Rate limit exceeded. Please try again later."}}
const openCodeFreeUsageLimitErrorType = "FreeUsageLimitError"

const (
	// defaultOpenCodeWarpRotateTimeoutSeconds 客户端超时默认值。轮换服务自身硬超时
	// 约 90 秒，客户端必须略大于它，否则会在服务端仍在轮换时提前放弃。
	defaultOpenCodeWarpRotateTimeoutSeconds = 95
	// defaultOpenCodeWarpRotateMaxPerRequest 同账号重试次数上限默认值。
	// 刻意保持很小的值：每次重试都伴随一次最长 90 秒的出口轮换。
	defaultOpenCodeWarpRotateMaxPerRequest = 2
	// openCodeWarpRotateRetryDelay 同账号重试前的最小间隔。轮换本身已经消耗了
	// 绝大部分等待时间，这里只需一个很小的延迟让上游状态稳定。
	openCodeWarpRotateRetryDelay = 300 * time.Millisecond

	// openCodeWarpRotateOpsKind 是轮换相关 ops 事件的 Kind。
	openCodeWarpRotateOpsKind = "opencode_warp_rotate"

	openCodeWarpRotateReasonRotated         = "rotated"
	openCodeWarpRotateReasonFailed          = "failed"
	openCodeWarpRotateReasonTimeout         = "timeout"
	openCodeWarpRotateReasonSkippedDisabled = "skipped_disabled"
	openCodeWarpRotateReasonSkippedNoProxy  = "skipped_no_proxy"
	openCodeWarpRotateReasonSkippedMinGap   = "skipped_min_interval"
)

// errOpenCodeWarpRotateMinInterval 表示本次轮换因最小间隔限制被跳过。
// 它不是故障：同一 proxy 刚刚轮换过，直接走原有 failover 链路即可。
var errOpenCodeWarpRotateMinInterval = errors.New("opencode warp rotate: min interval not elapsed")

// openCodeWarpRotatedContextKey 标记「本次请求已经成功轮换过 WARP 出口」。
// 该标记沿请求 ctx 传给 RateLimitService.handle429，避免刚轮换完就把账号写进
// 429 冷却并摘出调度快照，导致同账号重试在选号阶段被静默排除。
type openCodeWarpRotatedContextKey struct{}

// openCodeWarpRotateState 持有按 proxyID 的并发去重与最小间隔状态。
type openCodeWarpRotateState struct {
	flight singleflight.Group

	mu            sync.Mutex
	lastRotatedAt map[int64]time.Time
}

// SetWarpExitRotator 注入 WARP 出口轮换器。未注入时本能力整体为 no-op，
// opencode_go 的 429 走原有 failover 链路。沿用 SetPluginManager 的 setter 注入
// 范式，避免改动 NewOpenAIGatewayService 的构造函数签名。
func (s *OpenAIGatewayService) SetWarpExitRotator(rotator WarpExitRotator) {
	if s == nil {
		return
	}
	s.warpExitRotator = rotator
}

func (s *OpenAIGatewayService) openCodeWarpRotateState() *openCodeWarpRotateState {
	if s == nil {
		return nil
	}
	s.openCodeWarpRotateMu.Lock()
	defer s.openCodeWarpRotateMu.Unlock()
	if s.openCodeWarpRotate == nil {
		s.openCodeWarpRotate = &openCodeWarpRotateState{lastRotatedAt: make(map[int64]time.Time)}
	}
	return s.openCodeWarpRotate
}

// reserve 在最小间隔内拒绝重复轮换。成功返回 true 时同时记录本次尝试时间，
// 因此失败（含超时）也会被最小间隔节流，避免轮换服务不可用时被反复打满。
func (st *openCodeWarpRotateState) reserve(proxyID int64, minInterval time.Duration, now time.Time) bool {
	if st == nil {
		return true
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.lastRotatedAt == nil {
		st.lastRotatedAt = make(map[int64]time.Time)
	}
	if minInterval > 0 {
		if last, ok := st.lastRotatedAt[proxyID]; ok && now.Sub(last) < minInterval {
			return false
		}
	}
	st.lastRotatedAt[proxyID] = now
	return true
}

// openCodeWarpRotateMode 读取账号级开关，缺省/非法值回落 auto。
func openCodeWarpRotateMode(account *Account) string {
	if account == nil {
		return openCodeWarpRotateModeAuto
	}
	switch mode := strings.ToLower(strings.TrimSpace(account.GetCredential(openCodeWarpRotateCredential))); mode {
	case openCodeWarpRotateModeOn:
		return openCodeWarpRotateModeOn
	case openCodeWarpRotateModeOff:
		return openCodeWarpRotateModeOff
	default:
		return openCodeWarpRotateModeAuto
	}
}

// isOpenCodeFreeUsageLimit429 是插入点的三重限定：平台必须是 opencode_go、
// 状态码必须是 429、响应体必须带 FreeUsageLimitError。
//
// 插入点（/responses 与 CC 收口）被所有平台共用，任何一条不满足都必须原样返回，
// 否则会把其他平台的 429（如 OpenAI 的 usage_limit_reached）误当成免费层配额耗尽。
func isOpenCodeFreeUsageLimit429(account *Account, statusCode int, responseBody []byte) bool {
	if account == nil || !account.IsOpenCodeGo() || statusCode != http.StatusTooManyRequests || len(responseBody) == 0 {
		return false
	}
	if gjson.ValidBytes(responseBody) {
		// 合法 JSON：只认结构化字段，避免把 message 里引用该类型名的响应误判。
		for _, path := range []string{"error.type", "response.error.type", "detail.type", "type"} {
			if strings.EqualFold(strings.TrimSpace(gjson.GetBytes(responseBody, path).String()), openCodeFreeUsageLimitErrorType) {
				return true
			}
		}
		return false
	}
	// 非法 JSON（被中间层截断/包裹）时退回子串匹配。
	return bytes.Contains(responseBody, []byte(openCodeFreeUsageLimitErrorType))
}

// openCodeWarpRotateSettings 返回配置并判定轮换服务是否已部署。
func (s *OpenAIGatewayService) openCodeWarpRotateSettings() (config.GatewayOpenCodeWarpRotateConfig, bool) {
	var settings config.GatewayOpenCodeWarpRotateConfig
	if s == nil || s.cfg == nil {
		return settings, false
	}
	settings = s.cfg.Gateway.OpenCodeWarpRotate
	if strings.TrimSpace(settings.ServiceURL) == "" {
		return settings, false
	}
	return settings, true
}

// shouldRotateOpenCodeWarpExit 判定当前账号是否允许触发出口轮换。
func (s *OpenAIGatewayService) shouldRotateOpenCodeWarpExit(account *Account) bool {
	if s == nil || s.warpExitRotator == nil {
		return false
	}
	settings, ok := s.openCodeWarpRotateSettings()
	if !ok {
		return false
	}
	switch openCodeWarpRotateMode(account) {
	case openCodeWarpRotateModeOff:
		return false
	case openCodeWarpRotateModeOn:
		// 账号级显式开启：允许在全局关闭时按账号灰度。
		return true
	default:
		return settings.Enabled
	}
}

// openCodeWarpRotateProxy 取出轮换所需的代理绑定。ProxyID 为空（直连或未绑定
// 代理）时没有可轮换的出口，返回 false。
func openCodeWarpRotateProxy(account *Account) (proxyID int64, proxyURL string, ok bool) {
	if account == nil || account.ProxyID == nil || *account.ProxyID <= 0 {
		return 0, "", false
	}
	if account.Proxy != nil {
		proxyURL = account.Proxy.URL()
	}
	return *account.ProxyID, proxyURL, true
}

// ensureFreshWarpExit 执行一次（可能被合并的）出口轮换。
//
// 并发去重按 proxyID 做 singleflight：同一 proxy 上并发的多个 429 只会产生一次
// 轮换调用，其余调用共享同一结果。最小间隔在 singleflight 内部判定，因此被合并
// 的调用同样会拿到「跳过」结论。
func (s *OpenAIGatewayService) ensureFreshWarpExit(
	ctx context.Context,
	settings config.GatewayOpenCodeWarpRotateConfig,
	proxyID int64,
	proxyURL string,
) (string, bool, error) {
	if s == nil || s.warpExitRotator == nil {
		return "", false, errors.New("opencode warp rotate: rotator not configured")
	}
	state := s.openCodeWarpRotateState()
	if state == nil {
		return "", false, errors.New("opencode warp rotate: state unavailable")
	}
	minInterval := time.Duration(settings.MinIntervalSeconds) * time.Second
	value, err, shared := state.flight.Do(strconv.FormatInt(proxyID, 10), func() (any, error) {
		if !state.reserve(proxyID, minInterval, time.Now()) {
			return "", errOpenCodeWarpRotateMinInterval
		}
		return s.warpExitRotator.EnsureFreshExit(ctx, proxyID, proxyURL)
	})
	if err != nil {
		return "", shared, err
	}
	exitIP, _ := value.(string)
	return exitIP, shared, nil
}

// closeOpenCodeWarpRotatedIdleConnections 关闭该代理缓存客户端的空闲连接。
//
// 这一步是功能能否生效的关键：OpenAI profile + socks5h 代理走
// upstreamProtocolModeOpenAIH2，一条 HTTP/2 TCP 隧道被多路复用，空闲连接池的
// IdleConnTimeout 长达 90 秒。不显式关闭的话，重试会复用轮换前建立的旧隧道，
// 出口 IP 根本没变，轮换静默失效。
//
// 通过可选接口断言调用（仓库先例见 ratelimit_service.go 的 runtimeBlocker 断言），
// 避免修改 HTTPUpstream 接口定义牵连所有测试 stub。
func (s *OpenAIGatewayService) closeOpenCodeWarpRotatedIdleConnections(account *Account, proxyURL string) bool {
	if s == nil || s.httpUpstream == nil || account == nil {
		return false
	}
	closer, ok := s.httpUpstream.(interface {
		CloseUpstreamIdleConnections(proxyURL string, accountID int64)
	})
	if !ok {
		return false
	}
	closer.CloseUpstreamIdleConnections(proxyURL, account.ID)
	return true
}

// openCodeWarpRotateMaxRetries 返回本次请求允许的同账号重试次数上限。
func (s *OpenAIGatewayService) openCodeWarpRotateMaxRetries() int {
	settings, ok := s.openCodeWarpRotateSettings()
	if !ok || settings.MaxRotationsPerRequest <= 0 {
		return defaultOpenCodeWarpRotateMaxPerRequest
	}
	return settings.MaxRotationsPerRequest
}

// rotateOpenCodeWarpExitOn429 是 failover 收口点上的轮换钩子。
//
// 返回值：
//   - ctx：轮换成功时携带「已轮换」标记，供 RateLimitService.handle429 跳过冷却；
//   - rotated：轮换成功、应把该次 failover 标为可在同账号上重试。
//
// 必须在 handleFailoverSideEffects 之前调用：后者会走 handle429 写冷却并把账号
// 摘出调度快照，先轮换才能把「已轮换」标记带进那条链路。
//
// 该函数自身绝不返回错误，也不 panic：轮换失败（含超时、未配置、无代理绑定）
// 时原样返回 (ctx, false)，调用方继续走原有 failover 分支。
func (s *OpenAIGatewayService) rotateOpenCodeWarpExitOn429(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	statusCode int,
	responseBody []byte,
) (context.Context, bool) {
	if s == nil || !isOpenCodeFreeUsageLimit429(account, statusCode, responseBody) {
		return ctx, false
	}
	if !s.shouldRotateOpenCodeWarpExit(account) {
		s.appendOpenCodeWarpRotateOps(c, account, openCodeWarpRotateReasonSkippedDisabled, "", false)
		return ctx, false
	}
	proxyID, proxyURL, ok := openCodeWarpRotateProxy(account)
	if !ok {
		s.appendOpenCodeWarpRotateOps(c, account, openCodeWarpRotateReasonSkippedNoProxy, "", false)
		return ctx, false
	}
	settings, _ := s.openCodeWarpRotateSettings()
	exitIP, shared, err := s.ensureFreshWarpExit(ctx, settings, proxyID, proxyURL)
	if err != nil {
		reason := openCodeWarpRotateReasonFailed
		switch {
		case errors.Is(err, errOpenCodeWarpRotateMinInterval):
			reason = openCodeWarpRotateReasonSkippedMinGap
		case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
			reason = openCodeWarpRotateReasonTimeout
		}
		s.appendOpenCodeWarpRotateOps(c, account, reason, err.Error(), shared)
		return ctx, false
	}
	// 轮换成功。先关空闲连接，再返回；顺序不能反，否则重试仍会复用旧隧道。
	closedIdle := s.closeOpenCodeWarpRotatedIdleConnections(account, proxyURL)
	s.appendOpenCodeWarpRotateOps(
		c, account, openCodeWarpRotateReasonRotated,
		fmt.Sprintf("exit_ip=%s idle_conn_closed=%t", exitIP, closedIdle), shared,
	)
	return context.WithValue(ctx, openCodeWarpRotatedContextKey{}, true), true
}

// ShouldRetryOpenCodeWarpRotated429 让 RateLimitService 在本次请求已成功轮换// WARP 出口时跳过 429 冷却。与 OpenAI OAuth 的 ShouldRetryOpenAIOAuth429 同构：
// 一旦写了冷却/持久化限流，下一次选号就会把该账号排除，同账号重试必然落空。
//
// 由 ratelimit_service.go 的 handle429 通过可选接口断言调用。
func (s *OpenAIGatewayService) ShouldRetryOpenCodeWarpRotated429(ctx context.Context, account *Account, responseBody []byte) bool {
	if s == nil || ctx == nil || account == nil {
		return false
	}
	rotated, _ := ctx.Value(openCodeWarpRotatedContextKey{}).(bool)
	if !rotated {
		return false
	}
	return isOpenCodeFreeUsageLimit429(account, http.StatusTooManyRequests, responseBody)
}

// appendOpenCodeWarpRotateOps 记录轮换相关 ops 事件与结构化日志。
// UpstreamStatusCode 刻意留 0：该事件不是上游响应，且 checkSkipMonitoringForUpstreamEvent
// 对 0 直接早退，不会影响紧随其后的 failover 事件的监控语义。
func (s *OpenAIGatewayService) appendOpenCodeWarpRotateOps(
	c *gin.Context,
	account *Account,
	reason string,
	detail string,
	shared bool,
) {
	reason = strings.TrimSpace(reason)
	if account == nil {
		return
	}
	if shared {
		detail = strings.TrimSpace(detail + " shared=singleflight")
	}
	appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
		ProxyID:     opsUpstreamProxyID(account),
		ProxyName:   opsUpstreamProxyName(account),
		Platform:    account.Platform,
		AccountID:   account.ID,
		AccountName: account.Name,
		Kind:        openCodeWarpRotateOpsKind,
		Message:     reason,
		Detail:      detail,
	})
	slog.Info("opencode_warp_rotate",
		"account_id", account.ID,
		"platform", account.Platform,
		"reason", reason,
		"detail", detail,
		"shared", shared,
	)
}

// applyOpenCodeWarpRotateRetryBudget 在轮换成功后给 failover 错误打上同账号重试预算。
//
// 刻意只设置 SameAccountRetryMax（错误级次数上限），绝不设置
// SameAccountRetryDeadline：handler 的 sameAccountRetryAllowed 一旦看到非零
// deadline 就会绕过次数上限，在窗口内无限重试同账号。
func (s *OpenAIGatewayService) applyOpenCodeWarpRotateRetryBudget(failoverErr *UpstreamFailoverError, rotated bool) {
	if !rotated || failoverErr == nil {
		return
	}
	failoverErr.RetryableOnSameAccount = true
	failoverErr.SameAccountRetryMax = s.openCodeWarpRotateMaxRetries()
	if failoverErr.SameAccountRetryDelay <= 0 {
		failoverErr.SameAccountRetryDelay = openCodeWarpRotateRetryDelay
	}
}
