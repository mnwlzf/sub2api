package service

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"go.uber.org/zap"
)

// BPS 错误分类、冷却与换号边界（阶段 7）。
//
// 分类只依赖上游 HTTP 状态 + 错误码/消息 + Retry-After，输出一个纯数据的
// 处置决策（OpenAIBPSErrorAction）：是否冷却账号、冷却哪个模型多久、是否标记
// 认证失败、是否允许换号。真正的状态写入通过窄接口
// OpenAIBPSAccountStateWriter 完成，DB 迁移与仓储实现不在本阶段新增。
const (
	// OpenAIBPSModelNotAllowedCooldown 是模型权限错误的冷却时长（spec D10）。
	OpenAIBPSModelNotAllowedCooldown = 30 * time.Minute
	// OpenAIBPSRateLimitDefaultCooldown 是 429 未给出可用 Retry-After 时的兜底冷却。
	OpenAIBPSRateLimitDefaultCooldown = 60 * time.Second
	// OpenAIBPSRateLimitMaxCooldown 上限：异常的超大 Retry-After 不能把账号冻结数天。
	OpenAIBPSRateLimitMaxCooldown = 24 * time.Hour

	// OpenAIBPSModelNotAllowedCode 是模型权限错误归一后的错误码。
	// 原始错误码保留在诊断日志与 OpenAIBPSErrorAction.UpstreamCode 中。
	OpenAIBPSModelNotAllowedCode = "bps_model_not_allowed"
	// OpenAIBPSRateLimitedCode 是 429 未带上游错误码时的兜底错误码。
	OpenAIBPSRateLimitedCode = "bps_rate_limited"
	// OpenAIBPSForbiddenCode 是普通 403 未带上游错误码时的兜底错误码。
	OpenAIBPSForbiddenCode = "bps_upstream_forbidden"
	// OpenAIBPSUpstreamUnreachableCode 是传输层失败（未到达上游）的错误码。
	OpenAIBPSUpstreamUnreachableCode = "bps_upstream_unreachable"
	// OpenAIBPSDefaultAuthErrorCode 是 401 未带可识别错误码时的兜底（映射 revoked）。
	OpenAIBPSDefaultAuthErrorCode = "token_revoked"
)

// OpenAIBPSErrorKind 是错误分类枚举。
type OpenAIBPSErrorKind string

const (
	// OpenAIBPSErrorKindNone 表示上游成功或无需处置。
	OpenAIBPSErrorKindNone OpenAIBPSErrorKind = ""
	// OpenAIBPSErrorKindAuth 表示 401/令牌撤销：停止该账号调度。
	OpenAIBPSErrorKindAuth OpenAIBPSErrorKind = "auth"
	// OpenAIBPSErrorKindModelNotAllowed 表示模型权限错误：按映射后模型冷却。
	OpenAIBPSErrorKindModelNotAllowed OpenAIBPSErrorKind = "model_not_allowed"
	// OpenAIBPSErrorKindForbidden 表示普通 403：单次失败，不整号失效。
	OpenAIBPSErrorKindForbidden OpenAIBPSErrorKind = "forbidden"
	// OpenAIBPSErrorKindInvalidRequest 表示 400：不重试。
	OpenAIBPSErrorKindInvalidRequest OpenAIBPSErrorKind = "invalid_request"
	// OpenAIBPSErrorKindRateLimited 表示 429：按 Retry-After 冷却。
	OpenAIBPSErrorKindRateLimited OpenAIBPSErrorKind = "rate_limited"
	// OpenAIBPSErrorKindTransport 表示网络错误（未取得上游响应）。
	OpenAIBPSErrorKindTransport OpenAIBPSErrorKind = "transport"
	// OpenAIBPSErrorKindUpstream 表示上游 5xx。
	OpenAIBPSErrorKindUpstream OpenAIBPSErrorKind = "upstream_5xx"
	// OpenAIBPSErrorKindUnknown 表示其它状态码：保守按单次失败处理。
	OpenAIBPSErrorKindUnknown OpenAIBPSErrorKind = "unknown"
)

// OpenAIBPSUpstreamError 是从上游错误体解析出的诊断信息。
// Code 为空时 Type 作为回退；两者都缺失时由分类层按状态码兜底。
type OpenAIBPSUpstreamError struct {
	StatusCode int
	Code       string
	Type       string
	Message    string
}

// DiagnosticCode 返回用于分类的稳定错误码：优先 error.code，缺失时回退
// error.type（上游有时只把权限错误码放在 type 里）。
func (e OpenAIBPSUpstreamError) DiagnosticCode() string {
	if code := strings.TrimSpace(e.Code); code != "" {
		return code
	}
	return strings.TrimSpace(e.Type)
}

// ParseOpenAIBPSUpstreamError 解析 BPS/OpenAI 兼容错误信封。
//
// 只读取已知字段，绝不把任意响应文本当作错误码；消息截断到 500 字符，
// 避免把大段 HTML/堆栈写进日志或状态。
func ParseOpenAIBPSUpstreamError(statusCode int, body []byte) OpenAIBPSUpstreamError {
	parsed := OpenAIBPSUpstreamError{StatusCode: statusCode}
	if len(body) == 0 {
		return parsed
	}
	if !gjson.ValidBytes(body) {
		parsed.Message = openAIBPSTruncateMessage(strings.TrimSpace(string(body)))
		return parsed
	}
	parsed.Code = strings.TrimSpace(gjson.GetBytes(body, "error.code").String())
	parsed.Type = strings.TrimSpace(gjson.GetBytes(body, "error.type").String())
	parsed.Message = strings.TrimSpace(gjson.GetBytes(body, "error.message").String())
	if parsed.Code == "" {
		parsed.Code = strings.TrimSpace(gjson.GetBytes(body, "code").String())
	}
	if parsed.Type == "" {
		parsed.Type = strings.TrimSpace(gjson.GetBytes(body, "type").String())
	}
	if parsed.Message == "" {
		parsed.Message = strings.TrimSpace(gjson.GetBytes(body, "message").String())
	}
	if parsed.Message == "" {
		parsed.Message = strings.TrimSpace(gjson.GetBytes(body, "error.detail").String())
	}
	parsed.Message = openAIBPSTruncateMessage(parsed.Message)
	return parsed
}

// openAIBPSTruncateMessage 截断诊断消息（按 rune 安全截断）。
func openAIBPSTruncateMessage(message string) string {
	const limit = 500
	if len(message) <= limit {
		return message
	}
	runes := []rune(message)
	if len(runes) <= limit {
		return message
	}
	return string(runes[:limit])
}

// OpenAIBPSErrorInput 是错误分类的输入。
type OpenAIBPSErrorInput struct {
	// StatusCode 为 0 表示请求没有到达上游（网络/超时）。
	StatusCode int
	Code       string
	Message    string
	// RetryAfter 是上游 Retry-After 头原文（秒数或 HTTP-date）。
	RetryAfter string
	// Model 是映射后的上游模型：模型级冷却的 scope。
	Model string
	// SessionBound 表示请求开始前会话已有绑定（续写会话，不允许换号）。
	SessionBound bool
	// OutputStarted 表示已经向客户端输出过任何字节（不允许重放）。
	OutputStarted bool
	Now           time.Time
}

// OpenAIBPSErrorAction 是一次错误处置决策（纯数据，便于单测与日志）。
type OpenAIBPSErrorAction struct {
	Kind       OpenAIBPSErrorKind
	StatusCode int
	// Code 是归一后的诊断错误码（例如 bps_model_not_allowed）。
	Code string
	// UpstreamCode 是上游原始错误码，保留在诊断信息中。
	UpstreamCode string
	Message      string
	// Retryable 表示是否允许「未绑定上下文且未输出」的新会话换号重试。
	Retryable bool
	// DisableAccount 表示停止该账号调度（401/令牌撤销）。
	DisableAccount bool
	// AuthErrorCode 是写入凭证诊断的错误码（token_revoked / invalid_token 等）。
	AuthErrorCode string
	// RateLimitUntil 非零时冷却整个账号（429 Retry-After）。
	RateLimitUntil time.Time
	// ModelCooldownUntil / ModelCooldownScope 非零时只冷却指定模型。
	ModelCooldownUntil time.Time
	ModelCooldownScope string
}

// HasStateWrite 报告该决策是否需要写账号状态。
func (a OpenAIBPSErrorAction) HasStateWrite() bool {
	return a.DisableAccount || !a.RateLimitUntil.IsZero() || !a.ModelCooldownUntil.IsZero()
}

// ClassifyOpenAIBPSError 按 spec D10 分类上游错误。
//
// 规则（顺序敏感）：
//   - 401：认证失败，停止账号调度，不重试；
//   - 403 + basispoints_model_access_changed/model_not_allowed：归一为
//     bps_model_not_allowed，按映射后模型冷却 30 分钟，不重试；
//   - 403 普通：单次失败，不整号失效，不重试；
//   - 400：参数错误，不重试；
//   - 429：按 Retry-After 冷却账号；仅未绑定且未输出的新会话可换号；
//   - 网络错误/5xx：不冷却；仅未绑定且未输出的新会话可换号；
//   - 其它：保守按单次失败处理。
func ClassifyOpenAIBPSError(input OpenAIBPSErrorInput) OpenAIBPSErrorAction {
	now := input.Now
	if now.IsZero() {
		now = time.Now()
	}
	code := strings.ToLower(strings.TrimSpace(input.Code))
	message := strings.ToLower(strings.TrimSpace(input.Message))
	model := strings.TrimSpace(input.Model)
	action := OpenAIBPSErrorAction{
		StatusCode:   input.StatusCode,
		UpstreamCode: strings.TrimSpace(input.Code),
		Message:      input.Message,
	}
	// 换号边界对 429/5xx/网络错误统一生效：已绑定会话或已输出后绝不重放。
	failoverAllowed := !input.SessionBound && !input.OutputStarted

	switch {
	case input.StatusCode == 0:
		action.Kind = OpenAIBPSErrorKindTransport
		action.Code = OpenAIBPSUpstreamUnreachableCode
		action.Retryable = failoverAllowed
	case input.StatusCode == http.StatusUnauthorized:
		action.Kind = OpenAIBPSErrorKindAuth
		action.DisableAccount = true
		action.Code = code
		if action.Code == "" {
			action.Code = OpenAIBPSDefaultAuthErrorCode
		}
		action.AuthErrorCode = code
		if action.AuthErrorCode == "" {
			action.AuthErrorCode = OpenAIBPSDefaultAuthErrorCode
		}
	case input.StatusCode == http.StatusForbidden:
		if openAIBPSIsModelNotAllowed(code, message) {
			action.Kind = OpenAIBPSErrorKindModelNotAllowed
			action.Code = OpenAIBPSModelNotAllowedCode
			if model != "" {
				action.ModelCooldownScope = model
				action.ModelCooldownUntil = now.Add(OpenAIBPSModelNotAllowedCooldown)
			}
			// 模型权限错误不换号：换号可能只是把同一个权限问题复制到别的账号。
			action.Retryable = false
			break
		}
		action.Kind = OpenAIBPSErrorKindForbidden
		action.Code = code
		if action.Code == "" {
			action.Code = OpenAIBPSForbiddenCode
		}
		// 普通 403 不判定整号失效，也不冷却：保持账号可调度。
		action.Retryable = false
	case input.StatusCode == http.StatusBadRequest:
		action.Kind = OpenAIBPSErrorKindInvalidRequest
		action.Code = code
		if action.Code == "" {
			action.Code = OpenAIBPSErrorInvalidRequest
		}
		action.Retryable = false
	case input.StatusCode == http.StatusTooManyRequests:
		action.Kind = OpenAIBPSErrorKindRateLimited
		action.Code = code
		if action.Code == "" {
			action.Code = OpenAIBPSRateLimitedCode
		}
		cooldown := OpenAIBPSRateLimitDefaultCooldown
		if parsed, ok := ParseOpenAIBPSRetryAfter(input.RetryAfter, now); ok && parsed > 0 {
			cooldown = parsed
		}
		action.RateLimitUntil = now.Add(cooldown)
		action.Retryable = failoverAllowed
	case input.StatusCode >= http.StatusInternalServerError:
		action.Kind = OpenAIBPSErrorKindUpstream
		action.Code = code
		if action.Code == "" {
			action.Code = OpenAIBPSErrorUpstream
		}
		action.Retryable = failoverAllowed
	default:
		action.Kind = OpenAIBPSErrorKindUnknown
		action.Code = code
		if action.Code == "" {
			action.Code = OpenAIBPSErrorUpstream
		}
		action.Retryable = false
	}
	return action
}

// openAIBPSIsModelNotAllowed 识别模型权限错误码/消息。
// 上游可能把码放在 code、type 或 message 里，三者都认。
func openAIBPSIsModelNotAllowed(code, message string) bool {
	for _, needle := range []string{"basispoints_model_access_changed", "model_not_allowed"} {
		if code == needle || strings.Contains(message, needle) {
			return true
		}
	}
	return false
}

// ParseOpenAIBPSRetryAfter 解析 Retry-After（秒数或 HTTP-date）。
// 返回值已按 [0, OpenAIBPSRateLimitMaxCooldown] 截断；ok=false 表示头缺失/非法。
func ParseOpenAIBPSRetryAfter(value string, now time.Time) (time.Duration, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, false
	}
	if now.IsZero() {
		now = time.Now()
	}
	var cooldown time.Duration
	if seconds, err := strconv.Atoi(value); err == nil {
		if seconds < 0 {
			return 0, false
		}
		cooldown = time.Duration(seconds) * time.Second
	} else if when, err := http.ParseTime(value); err == nil {
		cooldown = when.Sub(now)
		if cooldown < 0 {
			cooldown = 0
		}
	} else {
		return 0, false
	}
	if cooldown > OpenAIBPSRateLimitMaxCooldown {
		cooldown = OpenAIBPSRateLimitMaxCooldown
	}
	return cooldown, true
}

// OpenAIBPSAccountStateWriter 是错误处置所需的账号状态写入窄接口。
//
// 默认实现 OpenAIBPSAccountStateStore 直接复用既有仓储能力（SetRateLimited /
// SetModelRateLimit / MarkOpenAIBPSAuthFailure），不新增 DB 迁移。
type OpenAIBPSAccountStateWriter interface {
	// SetOpenAIBPSRateLimit 冷却整个账号（429）。
	SetOpenAIBPSRateLimit(ctx context.Context, accountID int64, until time.Time) error
	// SetOpenAIBPSModelCooldown 只冷却指定模型（模型权限错误）。
	SetOpenAIBPSModelCooldown(ctx context.Context, accountID int64, model string, until time.Time) error
	// MarkOpenAIBPSAuthFailure 条件写入认证失败诊断（401）；返回是否真正写入。
	MarkOpenAIBPSAuthFailure(ctx context.Context, accountID int64, accessToken, accountIDHeader, errorCode string) (bool, error)
}

// OpenAIBPSAccountStateRepository 是默认状态写入器所需的仓储窄接口。
// AccountRepository 直接满足（SetRateLimited / SetModelRateLimit）。
type OpenAIBPSAccountStateRepository interface {
	SetRateLimited(ctx context.Context, id int64, resetAt time.Time) error
	SetModelRateLimit(ctx context.Context, id int64, scope string, resetAt time.Time, reason ...string) error
}

// OpenAIBPSAccountStateStore 是 OpenAIBPSAccountStateWriter 的默认实现。
//
// accounts 提供限流/模型冷却写入；credentials 提供条件认证失败写入。两者都
// 允许为 nil（未接线时对应写入变成 no-op，绝不 panic）。
type OpenAIBPSAccountStateStore struct {
	accounts    OpenAIBPSAccountStateRepository
	credentials OpenAIBPSCredentialFailureWriter
}

// NewOpenAIBPSAccountStateStore 构造默认状态写入器。
// 生产接线：NewOpenAIBPSAccountStateStore(accountRepo, accountRepo)。
func NewOpenAIBPSAccountStateStore(accounts OpenAIBPSAccountStateRepository, credentials OpenAIBPSCredentialFailureWriter) *OpenAIBPSAccountStateStore {
	return &OpenAIBPSAccountStateStore{accounts: accounts, credentials: credentials}
}

var _ OpenAIBPSAccountStateWriter = (*OpenAIBPSAccountStateStore)(nil)

// SetOpenAIBPSRateLimit 写入账号级冷却。
func (s *OpenAIBPSAccountStateStore) SetOpenAIBPSRateLimit(ctx context.Context, accountID int64, until time.Time) error {
	if s == nil || s.accounts == nil || accountID <= 0 || until.IsZero() {
		return nil
	}
	return s.accounts.SetRateLimited(ctx, accountID, until)
}

// SetOpenAIBPSModelCooldown 写入模型级冷却（extra.model_rate_limits）。
func (s *OpenAIBPSAccountStateStore) SetOpenAIBPSModelCooldown(ctx context.Context, accountID int64, model string, until time.Time) error {
	model = strings.TrimSpace(model)
	if s == nil || s.accounts == nil || accountID <= 0 || model == "" || until.IsZero() {
		return nil
	}
	return s.accounts.SetModelRateLimit(ctx, accountID, model, until, OpenAIBPSModelNotAllowedCode)
}

// MarkOpenAIBPSAuthFailure 条件写入认证失败诊断。
func (s *OpenAIBPSAccountStateStore) MarkOpenAIBPSAuthFailure(ctx context.Context, accountID int64, accessToken, accountIDHeader, errorCode string) (bool, error) {
	if s == nil || s.credentials == nil || accountID <= 0 || strings.TrimSpace(accessToken) == "" {
		return false, nil
	}
	return s.credentials.MarkOpenAIBPSAuthFailure(ctx, accountID, accessToken, accountIDHeader, errorCode)
}

// applyOpenAIBPSErrorAction 把分类结果落到账号状态。
//
// 同时更新内存中的账号对象，使同进程后续选号立刻生效；DB 写入失败只记日志，
// 不改变已经回给客户端的错误语义。
func (h *OpenAIBPSHandler) applyOpenAIBPSErrorAction(ctx context.Context, account *Account, action OpenAIBPSErrorAction, log *zap.Logger) {
	if h == nil || account == nil || !action.HasStateWrite() {
		return
	}
	writer := h.deps.AccountState
	if writer == nil {
		return
	}
	now := time.Now()
	switch {
	case action.DisableAccount:
		written, err := writer.MarkOpenAIBPSAuthFailure(ctx, account.ID,
			openAIBPSAccessToken(account), openAIBPSWorkspace(account), action.AuthErrorCode)
		if err != nil {
			openAIBPSLogWarn(log, "openai bps: mark auth failure failed", account, action, zap.Error(err))
			return
		}
		if written {
			// 条件写入成功才更新内存，避免把已替换的新凭证标记为失效。
			if account.Extra == nil {
				account.Extra = make(map[string]any)
			}
			account.Extra[OpenAIBPSCredentialStateExtraKey] = NewOpenAIBPSCredentialStateExtra(
				action.AuthErrorCode,
				OpenAIBPSCredentialTokenFingerprint(openAIBPSAccessToken(account)),
				openAIBPSWorkspace(account),
				now,
			)
		}
	case !action.RateLimitUntil.IsZero():
		if err := writer.SetOpenAIBPSRateLimit(ctx, account.ID, action.RateLimitUntil); err != nil {
			openAIBPSLogWarn(log, "openai bps: set account rate limit failed", account, action, zap.Error(err))
			return
		}
		until := action.RateLimitUntil
		account.RateLimitedAt = &now
		account.RateLimitResetAt = &until
	case !action.ModelCooldownUntil.IsZero():
		if err := writer.SetOpenAIBPSModelCooldown(ctx, account.ID, action.ModelCooldownScope, action.ModelCooldownUntil); err != nil {
			openAIBPSLogWarn(log, "openai bps: set model cooldown failed", account, action, zap.Error(err))
			return
		}
		setAccountModelRateLimitSnapshot(account, action.ModelCooldownScope, action.ModelCooldownUntil, OpenAIBPSModelNotAllowedCode, now)
	}
}

// openAIBPSLogWarn 统一错误处置日志字段（kind/状态码/归一码/原始码/冷却窗口）。
func openAIBPSLogWarn(log *zap.Logger, message string, account *Account, action OpenAIBPSErrorAction, fields ...zap.Field) {
	if log == nil {
		return
	}
	base := []zap.Field{
		zap.String("kind", string(action.Kind)),
		zap.Int("upstream_status", action.StatusCode),
		zap.String("normalized_code", action.Code),
		zap.String("upstream_code", action.UpstreamCode),
	}
	if account != nil {
		base = append(base, zap.Int64("account_id", account.ID))
	}
	if !action.ModelCooldownUntil.IsZero() {
		base = append(base, zap.String("model_cooldown_scope", action.ModelCooldownScope),
			zap.Time("model_cooldown_until", action.ModelCooldownUntil))
	}
	if !action.RateLimitUntil.IsZero() {
		base = append(base, zap.Time("rate_limit_until", action.RateLimitUntil))
	}
	if action.DisableAccount {
		base = append(base, zap.Bool("disable_account", true), zap.String("auth_error_code", action.AuthErrorCode))
	}
	log.Warn(message, append(base, fields...)...)
}

// openAIBPSReplayReadCloser 把「已读取的前缀 + 剩余原始 body」重新拼成可读 body。
// Close 必须关闭原始 body，否则上游连接无法复用。
type openAIBPSReplayReadCloser struct {
	io.Reader
	io.Closer
}

// openAIBPSPeekUpstreamError 预读上游错误体用于分类与 usage 提取，然后把 body
// 还原给转发层（前缀 + 剩余流），不改变既有原样透传语义。
func openAIBPSPeekUpstreamError(c *gin.Context, resp *http.Response) OpenAIBPSUpstreamError {
	if resp == nil || resp.Body == nil {
		return OpenAIBPSUpstreamError{}
	}
	prefix, readErr := io.ReadAll(io.LimitReader(resp.Body, openAIBPSUsageErrorBodyPeekBytes))
	original := resp.Body
	resp.Body = &openAIBPSReplayReadCloser{
		Reader: io.MultiReader(bytes.NewReader(prefix), original),
		Closer: original,
	}
	if readErr != nil {
		return OpenAIBPSUpstreamError{StatusCode: resp.StatusCode}
	}
	// 失败响应也可能带 usage：先落到 gin 上下文，转发结束后统一记录。
	if usage, ok := OpenAIBPSUsageFromJSON(prefix); ok && c != nil {
		if _, exists := c.Get(OpenAIBPSUsageContextKey); !exists {
			c.Set(OpenAIBPSUsageContextKey, usage)
		}
	}
	return ParseOpenAIBPSUpstreamError(resp.StatusCode, prefix)
}

// openAIBPSUpstreamRetryAfter 返回应透传给客户端的 Retry-After（仅合法值）。
func openAIBPSUpstreamRetryAfter(resp *http.Response) string {
	if resp == nil {
		return ""
	}
	value := strings.TrimSpace(resp.Header.Get("Retry-After"))
	if value == "" {
		return ""
	}
	if _, ok := ParseOpenAIBPSRetryAfter(value, time.Now()); !ok {
		return ""
	}
	return value
}
