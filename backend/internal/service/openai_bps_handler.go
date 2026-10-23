package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"go.uber.org/zap"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
)

// BPS 网关错误码。错误对象用 infraerrors 承载（HTTP 状态 + reason），响应体
// 用 OpenAI 兼容错误信封写出，客户端按 code 分流。
const (
	OpenAIBPSErrorPlatformDisabled   = "bps_platform_disabled"
	OpenAIBPSErrorAccountUnavailable = "bps_account_unavailable"
	OpenAIBPSErrorContextExpired     = "bps_context_expired"
	OpenAIBPSErrorUpstream           = "bps_upstream_error"
	OpenAIBPSErrorInvalidRequest     = "bps_invalid_request"
	OpenAIBPSErrorStateUnavailable   = "bps_state_unavailable"
	OpenAIBPSErrorInvalidCredentials = "bps_invalid_credentials"
	OpenAIBPSErrorInvalidAccount     = "bps_invalid_account"
	OpenAIBPSErrorUnauthorized       = "invalid_api_key"
)

// 请求体上限：BPS 只承载文本 Responses，32 MiB 足够且能防住恶意大包。
const openAIBPSMaxRequestBodyBytes = 32 << 20

// gin.Context 上的 BPS 观测键。handler 在关键节点写入，Lead 的路由/计费/
// 指标中间件（以及 Resolver）可据此拿到会话、账号与上游结果。
const (
	// OpenAIBPSSessionKeyContextKey 是派生出的会话键（已按 API Key 隔离）。
	OpenAIBPSSessionKeyContextKey = "openai_bps_session_key"
	// OpenAIBPSBoundAccountIDContextKey / OpenAIBPSBoundWorkspaceContextKey 在
	// 已绑定会话路径上写入，供 Resolver 在无 Lookup 扩展时遵守绑定约束。
	OpenAIBPSBoundAccountIDContextKey = "openai_bps_bound_account_id"
	OpenAIBPSBoundWorkspaceContextKey = "openai_bps_bound_workspace"
	// OpenAIBPSAccountIDContextKey 是本次实际使用的账号 ID。
	OpenAIBPSAccountIDContextKey = "openai_bps_account_id"
	// OpenAIBPSUpstreamStatusContextKey / OpenAIBPSUpstreamRequestIDContextKey
	// 是上游状态码与 x-request-id，供 usage/日志/监测使用。
	OpenAIBPSUpstreamStatusContextKey    = "openai_bps_upstream_status"
	OpenAIBPSUpstreamRequestIDContextKey = "openai_bps_upstream_request_id"
)

// OpenAIBPSAccountResolver 由调度层实现：按模型选一个可用的 BPS 账号。
// 返回错误时若为 *infraerrors.ApplicationError 则原样透传给客户端，否则按
// bps_account_unavailable(503) 处理。
type OpenAIBPSAccountResolver interface {
	SelectOpenAIBPSAccount(ctx context.Context, c *gin.Context, model string) (*Account, error)
}

// OpenAIBPSBoundAccountLookup 是 OpenAIBPSAccountResolver 的可选扩展。
//
// 已绑定会话是硬约束：必须精确取回绑定账号，不能走调度重新选号。实现该接口
// 后 handler 直接按 ID 取号；未实现时退化为「把绑定写进 gin 上下文 → 调
// SelectOpenAIBPSAccount → 校验返回账号 ID/workspace」，此时 Resolver 必须
// 读取 OpenAIBPSBoundAccountIDContextKey 并返回同一个账号，否则请求失败。
type OpenAIBPSBoundAccountLookup interface {
	OpenAIBPSAccountByID(ctx context.Context, accountID int64) (*Account, error)
}

// OpenAIBPSAccountReleaser 是 OpenAIBPSAccountResolver 的可选扩展：如果
// SelectOpenAIBPSAccount 在选号时占用了账号并发槽/租约，实现该方法供 handler
// 在请求结束后释放（仅对通过 SelectOpenAIBPSAccount 选中的账号调用；通过
// OpenAIBPSAccountByID 取回的绑定账号由 Lookup 实现自行管理生命周期）。
type OpenAIBPSAccountReleaser interface {
	ReleaseOpenAIBPSAccount(ctx context.Context, account *Account)
}

// OpenAIBPSHandlerDeps 是 BPS Responses 入口的依赖。
type OpenAIBPSHandlerDeps struct {
	Resolver   OpenAIBPSAccountResolver
	Sessions   *OpenAIBPSSessionStore
	HTTPClient *http.Client
}

// OpenAIBPSHandler 是 OpenAI BPS 的 /responses 文本网关入口。
type OpenAIBPSHandler struct {
	deps OpenAIBPSHandlerDeps
}

// NewOpenAIBPSHandler 构造 BPS Responses handler。
func NewOpenAIBPSHandler(deps OpenAIBPSHandlerDeps) *OpenAIBPSHandler {
	return &OpenAIBPSHandler{deps: deps}
}

// RegisterOpenAIBPSRoutes 在给定 v1 路由组下注册 BPS 端点。
// POST /responses 在这里注册；POST /responses/compact 由上层接线（阶段 5
// 交付 OpenAIBPSHandler.Compact 入口，路由挂载由 Lead 统一完成）。
func RegisterOpenAIBPSRoutes(v1 *gin.RouterGroup, h *OpenAIBPSHandler) {
	if v1 == nil || h == nil {
		return
	}
	v1.POST("/responses", h.Responses)
	v1.POST("/responses/compact", h.Compact)
}

// Responses 处理 POST /v1/responses。
//
// 流程：平台开关 → 解析/校验 body → 派生会话键 → 已绑定用绑定账号（硬约束），
// 未绑定选号并原子绑定（并发冲突重读赢家）→ 构造固定上游请求 → 发送 →
// 原样转发（JSON/SSE）→ 释放。
func (h *OpenAIBPSHandler) Responses(c *gin.Context) {
	h.handleResponses(c, false)
}

// Compact 处理 POST /v1/responses/compact。
//
// 与 Responses 共用平台开关、会话绑定、账号选择与错误信封；区别只在请求
// 准备阶段强制走真实摘要请求（关闭客户端工具），响应只回写 bpscmp_v1_
// 版本化不透明引用。路由由上层接线。
func (h *OpenAIBPSHandler) Compact(c *gin.Context) {
	h.handleResponses(c, true)
}

func (h *OpenAIBPSHandler) handleResponses(c *gin.Context, compact bool) {
	if c == nil || c.Writer == nil || h == nil {
		return
	}
	if !OpenAIBPSEnabled() {
		WriteOpenAIBPSError(c, infraerrors.Newf(http.StatusServiceUnavailable,
			OpenAIBPSErrorPlatformDisabled, "OpenAI BPS platform is disabled"))
		return
	}
	body, err := readOpenAIBPSRequestBody(c)
	if err != nil {
		WriteOpenAIBPSError(c, err)
		return
	}
	model := strings.TrimSpace(gjson.GetBytes(body, "model").String())
	if model == "" {
		WriteOpenAIBPSError(c, infraerrors.Newf(http.StatusBadRequest,
			OpenAIBPSErrorInvalidRequest, "model is required"))
		return
	}
	apiKeyID := getAPIKeyIDFromContext(c)
	if apiKeyID <= 0 {
		WriteOpenAIBPSError(c, infraerrors.Newf(http.StatusUnauthorized,
			OpenAIBPSErrorUnauthorized, "API key context is missing"))
		return
	}
	ctx := openAIBPSContext(c)

	sessionKey, explicitSession := OpenAIBPSSessionKey(apiKeyID, body)
	if !explicitSession {
		// 没有显式会话 ID 时由压缩引用恢复原会话：否则压缩后仅回传引用+
		// 工具结果的新请求会派生出一个新会话，丢掉绑定与摘要。
		if ref := openAIBPSCompactRefFromBody(body); ref != "" {
			recovered, err := h.recoverCompactSessionKey(ctx, apiKeyID, ref)
			if err != nil {
				WriteOpenAIBPSError(c, err)
				return
			}
			sessionKey = recovered
		}
	}
	c.Set(OpenAIBPSSessionKeyContextKey, sessionKey)
	if sessionKey == "" {
		if openAIBPSBodyLooksLikeContinuation(body) {
			WriteOpenAIBPSError(c, infraerrors.Newf(http.StatusConflict,
				OpenAIBPSErrorContextExpired, "BPS conversation state has expired; start a new conversation"))
			return
		}
		WriteOpenAIBPSError(c, infraerrors.Newf(http.StatusBadRequest,
			OpenAIBPSErrorInvalidRequest, "unable to derive a stable BPS session identity from the request"))
		return
	}

	account, release, err := h.selectUpstreamAccount(ctx, c, apiKeyID, sessionKey, model)
	if release != nil {
		defer release()
	}
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return
		}
		WriteOpenAIBPSError(c, err)
		return
	}
	if account == nil {
		WriteOpenAIBPSError(c, infraerrors.Newf(http.StatusServiceUnavailable,
			OpenAIBPSErrorAccountUnavailable, "No OpenAI BPS account is available"))
		return
	}

	// 请求准备（阶段 4 工具隧道 + 阶段 5 压缩 + 阶段 6 结构化）：工具目录写进
	// developer 协议消息、历史工具调用从 Redis 回放、压缩引用解析成摘要；
	// 发往上游的 body 与客户端 body 结构上不同。
	prepared, err := prepareOpenAIBPSRequestWithOptions(ctx, h.deps.Sessions, account, apiKeyID, sessionKey, body,
		openAIBPSPrepareOptions{CompactEndpoint: compact})
	if err != nil {
		WriteOpenAIBPSError(c, err)
		return
	}
	req, err := BuildOpenAIBPSUpstreamRequest(ctx, account, prepared.Body, prepared.Stream)
	if err != nil {
		WriteOpenAIBPSError(c, err)
		return
	}
	c.Set(OpenAIBPSAccountIDContextKey, account.ID)
	log := logger.FromContext(ctx).With(
		zap.String("platform", PlatformOpenAIBPS),
		zap.Int64("account_id", account.ID),
		zap.String("model", model),
		zap.String("session_key", sessionKey),
		zap.Bool("stream", prepared.Stream),
		zap.Bool("explicit_session", explicitSession),
		zap.Int("client_tools", len(prepared.Tools)),
		zap.Bool("compact", prepared.Compact),
		zap.Bool("structured", prepared.Structured != nil),
		zap.String("turn_id", prepared.TurnID),
		zap.Int("agent_iteration", prepared.AgentIteration),
	)

	client := h.deps.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		log.Warn("openai bps: upstream request failed", zap.Error(err))
		WriteOpenAIBPSError(c, infraerrors.Newf(http.StatusBadGateway,
			OpenAIBPSErrorUpstream, "OpenAI BPS upstream request failed"))
		return
	}
	defer func() { _ = resp.Body.Close() }()

	requestID := strings.TrimSpace(resp.Header.Get("x-request-id"))
	c.Set(OpenAIBPSUpstreamStatusContextKey, resp.StatusCode)
	c.Set(OpenAIBPSUpstreamRequestIDContextKey, requestID)
	log = log.With(zap.Int("upstream_status", resp.StatusCode), zap.String("upstream_request_id", requestID))
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		// 上游错误不在这里换号：已绑定会话是硬约束；未绑定会话的换号重试
		// 属于调度层（阶段 3 文本闭环先原样透传错误）。
		log.Warn("openai bps: upstream returned error status")
	}

	if err := forwardOpenAIBPSProtocolResponse(ctx, c, h.deps.Sessions, prepared, resp); err != nil {
		if ctx.Err() != nil {
			return
		}
		log.Warn("openai bps: forward response failed", zap.Error(err))
		if !c.Writer.Written() {
			WriteOpenAIBPSError(c, infraerrors.Newf(http.StatusBadGateway,
				OpenAIBPSErrorUpstream, "OpenAI BPS response forwarding failed"))
		}
		return
	}

	// 显式会话在成功响应后显式续期（BoundAccount/Bind 已续期；这里覆盖
	// 「绑定在本次请求期间被删除、但客户端仍持有显式会话 ID」的保活路径）。
	if explicitSession {
		if err := h.deps.Sessions.Touch(ctx, apiKeyID, sessionKey); err != nil {
			log.Debug("openai bps: touch session failed", zap.Error(err))
		}
	}
}

// recoverCompactSessionKey 由压缩引用恢复原会话键（无显式会话 ID 时使用）。
//
// 引用索引按 API Key 隔离：别的租户的引用必然查不到，按上下文过期处理，
// 绝不跨租户恢复摘要。Redis 不可用时显式 503，不降级。
func (h *OpenAIBPSHandler) recoverCompactSessionKey(ctx context.Context, apiKeyID int64, ref string) (string, error) {
	if h.deps.Sessions == nil {
		return "", openAIBPSStateUnavailable()
	}
	reference, err := h.deps.Sessions.LoadCompactReference(ctx, apiKeyID, ref)
	if err == nil {
		return reference.SessionKey, nil
	}
	switch {
	case errors.Is(err, ErrOpenAIBPSCompactExpired):
		return "", infraerrors.Newf(http.StatusConflict, OpenAIBPSErrorContextExpired,
			"BPS compaction state has expired; start a new conversation")
	case errors.Is(err, ErrOpenAIBPSSessionStoreUnavailable):
		return "", openAIBPSStateUnavailable()
	default:
		return "", openAIBPSStateUnavailable()
	}
}

// selectUpstreamAccount 解析本次请求使用的账号，返回可选的释放函数。
func (h *OpenAIBPSHandler) selectUpstreamAccount(ctx context.Context, c *gin.Context, apiKeyID int64, sessionKey, model string) (*Account, func(), error) {
	sessions := h.deps.Sessions
	if sessions == nil {
		return nil, nil, infraerrors.Newf(http.StatusServiceUnavailable,
			OpenAIBPSErrorStateUnavailable, "OpenAI BPS conversation state is not configured")
	}
	boundID, boundWorkspace, bound, err := sessions.BoundAccount(ctx, apiKeyID, sessionKey)
	if err != nil {
		return nil, nil, openAIBPSStateError(ctx, err, "read session binding")
	}
	if bound {
		return h.resolveBoundAccount(ctx, c, boundID, boundWorkspace, model)
	}

	account, release, err := h.selectAccount(ctx, c, model)
	if err != nil {
		return nil, nil, err
	}
	created, bindErr := sessions.Bind(ctx, apiKeyID, sessionKey, account.ID, openAIBPSWorkspace(account))
	if bindErr != nil {
		if release != nil {
			release()
		}
		return nil, nil, openAIBPSStateError(ctx, bindErr, "bind session")
	}
	if created {
		return account, release, nil
	}
	// 并发冲突：另一个请求先完成了绑定。绑定是赢家，必须重读并以它为准，
	// 绝不能覆盖（否则同一会话会被两个账号交替服务）。
	boundID, boundWorkspace, bound, err = sessions.BoundAccount(ctx, apiKeyID, sessionKey)
	if err != nil {
		if release != nil {
			release()
		}
		return nil, nil, openAIBPSStateError(ctx, err, "re-read session binding")
	}
	if !bound {
		// 两次调用之间绑定过期/被删：本次继续用刚选中的账号，下次请求重建绑定。
		return account, release, nil
	}
	if release != nil {
		release()
	}
	return h.resolveBoundAccount(ctx, c, boundID, boundWorkspace, model)
}

// selectAccount 通过 Resolver 选号，并把可选的租约释放函数一并返回。
func (h *OpenAIBPSHandler) selectAccount(ctx context.Context, c *gin.Context, model string) (*Account, func(), error) {
	if h.deps.Resolver == nil {
		return nil, nil, infraerrors.Newf(http.StatusServiceUnavailable,
			OpenAIBPSErrorAccountUnavailable, "OpenAI BPS account resolver is not configured")
	}
	account, err := h.deps.Resolver.SelectOpenAIBPSAccount(ctx, c, model)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, nil, err
		}
		var appErr *infraerrors.ApplicationError
		if errors.As(err, &appErr) {
			return nil, nil, err
		}
		logger.FromContext(ctx).Warn("openai bps: account selection failed",
			zap.String("model", model), zap.Error(err))
		return nil, nil, infraerrors.Newf(http.StatusServiceUnavailable,
			OpenAIBPSErrorAccountUnavailable, "No OpenAI BPS account is available for model %q", model)
	}
	if account == nil {
		return nil, nil, infraerrors.Newf(http.StatusServiceUnavailable,
			OpenAIBPSErrorAccountUnavailable, "No usable OpenAI BPS account is available for model %q", model)
	}
	var release func()
	if releaser, ok := h.deps.Resolver.(OpenAIBPSAccountReleaser); ok {
		// 释放必须能在客户端断开（ctx 已取消）后继续完成，因此剥离取消信号。
		releaseCtx := context.WithoutCancel(ctx)
		release = func() { releaser.ReleaseOpenAIBPSAccount(releaseCtx, account) }
	}
	if !openAIBPSAccountUsable(account) {
		if release != nil {
			release()
		}
		return nil, nil, infraerrors.Newf(http.StatusServiceUnavailable,
			OpenAIBPSErrorAccountUnavailable, "No usable OpenAI BPS account is available for model %q", model)
	}
	return account, release, nil
}

// resolveBoundAccount 取回已绑定会话的账号（绑定是硬约束，不允许换号）。
func (h *OpenAIBPSHandler) resolveBoundAccount(ctx context.Context, c *gin.Context, boundID int64, boundWorkspace, model string) (*Account, func(), error) {
	if h.deps.Resolver == nil {
		return nil, nil, infraerrors.Newf(http.StatusServiceUnavailable,
			OpenAIBPSErrorAccountUnavailable, "OpenAI BPS account resolver is not configured")
	}
	if lookup, ok := h.deps.Resolver.(OpenAIBPSBoundAccountLookup); ok {
		account, err := lookup.OpenAIBPSAccountByID(ctx, boundID)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return nil, nil, err
			}
			// 绑定账号取号失败（删除/仓储错误）一律收敛为 bps_account_unavailable：
			// 已绑定会话不允许静默换号，内部错误码也不应泄漏给客户端。
			logger.FromContext(ctx).Warn("openai bps: bound account lookup failed",
				zap.Int64("account_id", boundID), zap.Error(err))
			return nil, nil, openAIBPSBoundAccountUnavailable()
		}
		if err := validateOpenAIBPSBoundAccount(account, boundID, boundWorkspace); err != nil {
			return nil, nil, err
		}
		return account, nil, nil
	}

	// 退化路径：没有按 ID 取号的扩展时，把绑定约束写进 gin 上下文，由
	// Resolver 在选号时遵守，选择后强校验。Resolver 若忽略绑定，请求失败
	// 而不是静默换号。
	c.Set(OpenAIBPSBoundAccountIDContextKey, boundID)
	c.Set(OpenAIBPSBoundWorkspaceContextKey, boundWorkspace)
	account, release, err := h.selectAccount(ctx, c, model)
	if err != nil {
		return nil, nil, err
	}
	if err := validateOpenAIBPSBoundAccount(account, boundID, boundWorkspace); err != nil {
		if release != nil {
			release()
		}
		return nil, nil, err
	}
	return account, release, nil
}

// validateOpenAIBPSBoundAccount 校验取回的账号仍满足绑定约束且可用。
func validateOpenAIBPSBoundAccount(account *Account, boundID int64, boundWorkspace string) error {
	if account == nil || account.ID != boundID {
		return openAIBPSBoundAccountUnavailable()
	}
	if !openAIBPSAccountUsable(account) {
		return openAIBPSBoundAccountUnavailable()
	}
	if boundWorkspace != "" {
		if workspace := openAIBPSWorkspace(account); workspace != "" && workspace != boundWorkspace {
			return openAIBPSBoundAccountUnavailable()
		}
	}
	return nil
}

func openAIBPSBoundAccountUnavailable() error {
	return infraerrors.Newf(http.StatusConflict, OpenAIBPSErrorAccountUnavailable,
		"The account bound to this BPS conversation is unavailable; restore that account or start a new conversation")
}

// openAIBPSAccountUsable 报告账号是否仍可承载已绑定会话。
//
// 刻意不检查调度冷却（限流/过载）：这些是瞬时上游状态，应由上游 429/5xx
// 原样透传给客户端，而不是把绑定会话判定为永久不可用。账号级不可用
// （删除/禁用/凭据过期/缺 token）才返回 bps_account_unavailable。
func openAIBPSAccountUsable(account *Account) bool {
	if account == nil || !account.IsOpenAIBPS() || account.Type != AccountTypeOAuth {
		return false
	}
	if !account.IsActive() {
		return false
	}
	if account.AutoPauseOnExpired && account.ExpiresAt != nil && !time.Now().Before(*account.ExpiresAt) {
		return false
	}
	if openAIBPSCredentialExpired(account) {
		return false
	}
	return openAIBPSAccessToken(account) != ""
}

// openAIBPSStateError 把会话存储错误映射成对外错误码。
func openAIBPSStateError(ctx context.Context, err error, op string) error {
	switch {
	case errors.Is(err, ErrOpenAIBPSSessionCorrupt):
		return infraerrors.Newf(http.StatusConflict, OpenAIBPSErrorContextExpired,
			"BPS conversation state has expired; start a new conversation")
	case errors.Is(err, ErrOpenAIBPSSessionStoreUnavailable):
		return infraerrors.Newf(http.StatusServiceUnavailable, OpenAIBPSErrorStateUnavailable,
			"OpenAI BPS conversation state is unavailable")
	default:
		logger.FromContext(ctx).Warn("openai bps: session state error", zap.String("op", op), zap.Error(err))
		return infraerrors.Newf(http.StatusServiceUnavailable, OpenAIBPSErrorStateUnavailable,
			"OpenAI BPS conversation state is temporarily unavailable")
	}
}

// readOpenAIBPSRequestBody 读取并做基本校验的请求体。
func readOpenAIBPSRequestBody(c *gin.Context) ([]byte, error) {
	if c.Request == nil || c.Request.Body == nil {
		return nil, infraerrors.Newf(http.StatusBadRequest, OpenAIBPSErrorInvalidRequest, "request body is empty")
	}
	body, err := io.ReadAll(io.LimitReader(c.Request.Body, int64(openAIBPSMaxRequestBodyBytes)+1))
	if err != nil {
		return nil, infraerrors.Newf(http.StatusBadRequest, OpenAIBPSErrorInvalidRequest, "failed to read request body")
	}
	if len(body) > openAIBPSMaxRequestBodyBytes {
		return nil, infraerrors.Newf(http.StatusRequestEntityTooLarge, OpenAIBPSErrorInvalidRequest,
			"request body exceeds %d bytes", openAIBPSMaxRequestBodyBytes)
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return nil, infraerrors.Newf(http.StatusBadRequest, OpenAIBPSErrorInvalidRequest, "request body is empty")
	}
	return body, nil
}

// OpenAIBPSSessionKey 派生稳定的 BPS 会话键（纯函数）。
//
// 优先级：
//  1. 显式会话 ID：session_id/sessionId → prompt_cache_key/promptCacheKey →
//     client_metadata.session_id/sessionId（第二个返回值 true）；
//  2. 首条用户消息文本的 sha256（第二个返回值 false）；
//  3. 都没有时返回空串，由调用方决定报错语义（continuation → bps_context_expired）。
//
// 摘要混入 apiKeyID：同一段 session_id 在不同 API Key 下必须是不同键，避免
// 跨租户碰撞（Redis 键本身也含 apiKeyID，双重隔离）。
func OpenAIBPSSessionKey(apiKeyID int64, body []byte) (string, bool) {
	scope := ""
	explicit := false
	for _, path := range []string{
		"session_id", "sessionId",
		"prompt_cache_key", "promptCacheKey",
		"client_metadata.session_id", "client_metadata.sessionId",
	} {
		if value := strings.TrimSpace(gjson.GetBytes(body, path).String()); value != "" {
			scope = "explicit:" + value
			explicit = true
			break
		}
	}
	if scope == "" {
		text := openAIBPSFirstUserMessageText(body)
		if text == "" {
			return "", false
		}
		scope = "root:" + text
	}
	sum := sha256.Sum256([]byte("openai_bps:" + strconv.FormatInt(apiKeyID, 10) + ":" + scope))
	return hex.EncodeToString(sum[:]), explicit
}

// openAIBPSFirstUserMessageText 提取首条用户消息的文本（input 字符串或
// Responses 的 input 数组；内容支持字符串与 input_text/text 分片）。
func openAIBPSFirstUserMessageText(body []byte) string {
	input := gjson.GetBytes(body, "input")
	if !input.Exists() {
		return ""
	}
	if input.Type == gjson.String {
		return strings.TrimSpace(input.String())
	}
	if !input.IsArray() {
		return ""
	}
	for _, item := range input.Array() {
		if strings.TrimSpace(item.Get("role").String()) != "user" {
			continue
		}
		if text := openAIBPSContentText(item.Get("content")); text != "" {
			return text
		}
	}
	return ""
}

// openAIBPSContentText 拼接消息内容里的文本分片。
func openAIBPSContentText(content gjson.Result) string {
	if !content.Exists() {
		return ""
	}
	if content.Type == gjson.String {
		return strings.TrimSpace(content.String())
	}
	if !content.IsArray() {
		return ""
	}
	parts := make([]string, 0, len(content.Array()))
	for _, part := range content.Array() {
		if text := strings.TrimSpace(part.Get("text").String()); text != "" {
			parts = append(parts, text)
		}
	}
	return strings.TrimSpace(strings.Join(parts, "\n"))
}

// openAIBPSBodyLooksLikeContinuation 报告 body 是否明显是「已有会话的续写」：
// 没有可派生身份却带 assistant/工具/推理条目，说明客户端回传了需要会话状态
// 的历史，此时应返回 bps_context_expired 而不是当新会话重新绑定。
func openAIBPSBodyLooksLikeContinuation(body []byte) bool {
	if strings.TrimSpace(gjson.GetBytes(body, "previous_response_id").String()) != "" {
		return true
	}
	input := gjson.GetBytes(body, "input")
	if !input.IsArray() {
		return false
	}
	for _, item := range input.Array() {
		switch strings.TrimSpace(item.Get("role").String()) {
		case "assistant", "system", "developer":
			return true
		}
		switch strings.TrimSpace(item.Get("type").String()) {
		case "function_call", "function_call_output", "custom_tool_call", "custom_tool_call_output",
			"reasoning", "compaction", "compaction_summary":
			return true
		}
	}
	return false
}

// WriteOpenAIBPSError 用 OpenAI 兼容错误信封回写 BPS 网关错误。
func WriteOpenAIBPSError(c *gin.Context, err error) {
	if c == nil || c.Writer == nil || err == nil {
		return
	}
	statusCode, status := infraerrors.ToHTTP(err)
	if statusCode <= 0 {
		statusCode = http.StatusInternalServerError
	}
	code := strings.TrimSpace(status.Reason)
	if code == "" {
		code = OpenAIBPSErrorUpstream
	}
	message := strings.TrimSpace(status.Message)
	if message == "" {
		message = http.StatusText(statusCode)
	}
	c.JSON(statusCode, gin.H{
		"error": gin.H{
			"type":    openAIBPSErrorType(statusCode),
			"code":    code,
			"message": message,
		},
	})
}

func openAIBPSErrorType(statusCode int) string {
	switch {
	case statusCode == http.StatusUnauthorized:
		return "authentication_error"
	case statusCode == http.StatusForbidden:
		return "permission_error"
	case statusCode == http.StatusTooManyRequests:
		return "rate_limit_error"
	case statusCode >= http.StatusInternalServerError:
		return "server_error"
	default:
		return "invalid_request_error"
	}
}
