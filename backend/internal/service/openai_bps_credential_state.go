package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"regexp"
	"strings"
	"time"
)

// BPS 凭证状态机。
//
// 凭证状态有四种：normal / expired / revoked / auth_failed。
//   - normal：无本地过期信息，或 exp 尚未到达；
//   - expired：JWT exp 已过（本地判定，不依赖上游）；
//   - revoked：上游 401 / 明确撤销；
//   - auth_failed：其它认证失败（如 invalid_token、账号 ID 不匹配）。
//
// revoked/auth_failed 由上游响应写入 accounts.extra 的 bps_credential_state。
// 写入必须按"本次请求使用的 Access Token + 账号 ID"做条件更新：请求可能持续
// 数十秒，管理员在这期间替换凭证时，迟到响应绝不能把新凭证标记为失效。
const (
	// OpenAIBPSCredentialStateExtraKey 是只读凭证状态的 extra 存储键。
	OpenAIBPSCredentialStateExtraKey = "bps_credential_state"
	// openAIBPSExpiredErrorCode 是本地判定过期时对外展示的错误码。
	openAIBPSExpiredErrorCode = "bps_token_expired"
	// openAIBPSDefaultAuthErrorCode 是上游未给出可识别错误码时的兜底。
	openAIBPSDefaultAuthErrorCode = "bps_invalid_credentials"
	// openAIBPSRevokedErrorCode 是"令牌已撤销"的标准错误码，映射为 revoked 状态。
	openAIBPSRevokedErrorCode = "token_revoked"
)

// OpenAIBPSCredentialStatus 是 BPS 凭证的只读状态枚举。
type OpenAIBPSCredentialStatus string

const (
	// OpenAIBPSCredentialStatusNormal 表示凭证没有已知认证问题。
	OpenAIBPSCredentialStatusNormal OpenAIBPSCredentialStatus = "normal"
	// OpenAIBPSCredentialStatusExpired 表示 JWT exp 已过。
	OpenAIBPSCredentialStatusExpired OpenAIBPSCredentialStatus = "expired"
	// OpenAIBPSCredentialStatusRevoked 表示上游已撤销/401。
	OpenAIBPSCredentialStatusRevoked OpenAIBPSCredentialStatus = "revoked"
	// OpenAIBPSCredentialStatusAuthFailed 表示其它认证失败。
	OpenAIBPSCredentialStatusAuthFailed OpenAIBPSCredentialStatus = "auth_failed"
)

// OpenAIBPSCredentialState 是管理端只读凭证状态（可直接 JSON 序列化）。
// ExpiresAt 只描述 JWT 截止时间，不代表认证成功；LastSeenAt 是最近一次上游
// 认证失败被发现的时间。
type OpenAIBPSCredentialState struct {
	Status     OpenAIBPSCredentialStatus `json:"status"`
	ExpiresAt  *time.Time                `json:"expires_at,omitempty"`
	LastSeenAt *time.Time                `json:"last_seen_at,omitempty"`
	ErrorCode  string                    `json:"error_code,omitempty"`
}

// OpenAIBPSCredentialStateExtra 是 accounts.extra["bps_credential_state"] 的持久化
// 形状。token_fingerprint 与 account_id 用于识别诊断对应的具体凭证：两者任一
// 与当前凭证不一致即视为陈旧诊断，不参与状态计算。
type OpenAIBPSCredentialStateExtra struct {
	Status           OpenAIBPSCredentialStatus `json:"status"`
	LastSeenAt       *time.Time                `json:"last_seen_at,omitempty"`
	ErrorCode        string                    `json:"error_code,omitempty"`
	TokenFingerprint string                    `json:"token_fingerprint,omitempty"`
	AccountID        string                    `json:"account_id,omitempty"`
}

// OpenAIBPSCredentialFailureWriter 是认证失败的条件写入接口。
//
// 实现方必须校验账号当前 credentials 仍是"本次请求使用的 Access Token + 账号
// ID"，只有一致时才写入状态并返回 true；账号已删除、凭证已替换或账号 ID 已
// 变更时返回 false（不视为错误）。accountIDHeader 是本次请求实际发送的
// chatgpt-account-id 头。
type OpenAIBPSCredentialFailureWriter interface {
	MarkOpenAIBPSAuthFailure(ctx context.Context, accountID int64, accessToken string, accountIDHeader string, errorCode string) (bool, error)
}

// openAIBPSCredentialErrorCodePattern 约束错误码形状，避免把上游任意文本写进 extra。
var openAIBPSCredentialErrorCodePattern = regexp.MustCompile(`^[a-z0-9_.-]{1,128}$`)

// OpenAIBPSCredentialTokenFingerprint 返回 Access Token 的短指纹（sha256 前 16
// 字节的 hex，32 字符）。只用于比较凭证是否变化，不能反推 token。
func OpenAIBPSCredentialTokenFingerprint(token string) string {
	sum := sha256.Sum256([]byte(normalizeOpenAIBPSAccessToken(token)))
	return hex.EncodeToString(sum[:16])
}

// NewOpenAIBPSCredentialStateExtra 构造一次认证失败的持久化状态。
// errorCode 会被归一化；token_revoked 映射为 revoked，其余映射为 auth_failed。
func NewOpenAIBPSCredentialStateExtra(errorCode, tokenFingerprint, accountID string, now time.Time) OpenAIBPSCredentialStateExtra {
	code := normalizeOpenAIBPSCredentialErrorCode(errorCode)
	status := OpenAIBPSCredentialStatusAuthFailed
	if code == openAIBPSRevokedErrorCode {
		status = OpenAIBPSCredentialStatusRevoked
	}
	seen := now.UTC()
	return OpenAIBPSCredentialStateExtra{
		Status:           status,
		LastSeenAt:       &seen,
		ErrorCode:        code,
		TokenFingerprint: strings.TrimSpace(tokenFingerprint),
		AccountID:        strings.TrimSpace(accountID),
	}
}

// ResolveOpenAIBPSCredentialState 计算账号当前凭证状态。
//
// 优先级：revoked > expired > auth_failed > normal。expired 只来自本地 exp，
// 不依赖上游；revoked/auth_failed 只在 extra 中诊断的 token 指纹与账号 ID 仍与
// 当前凭证一致时生效，否则视为陈旧诊断（例如管理员已替换 token）。
// 非 BPS 账号返回零值状态（Status 为空），调用方据此判断"不适用"。
func ResolveOpenAIBPSCredentialState(account *Account, now time.Time) OpenAIBPSCredentialState {
	if account == nil || !account.IsOpenAIBPS() {
		return OpenAIBPSCredentialState{}
	}
	state := OpenAIBPSCredentialState{
		Status:    OpenAIBPSCredentialStatusNormal,
		ExpiresAt: account.OpenAIBPSExpiresAt(),
	}
	if saved, ok := openAIBPSCredentialStateExtraFromAccount(account); ok &&
		openAIBPSCredentialStateExtraIsCurrent(saved, account) {
		switch saved.Status {
		case OpenAIBPSCredentialStatusRevoked,
			OpenAIBPSCredentialStatusAuthFailed,
			OpenAIBPSCredentialStatusExpired:
			state.Status = saved.Status
			state.ErrorCode = saved.ErrorCode
			state.LastSeenAt = saved.LastSeenAt
		}
	}
	// 本地过期判定：撤销是更强的结论（token 已不可用，再谈过期没有意义），
	// 其它状态则以本地 exp 为准。保留已有错误码与发现时间，便于排障。
	if state.Status != OpenAIBPSCredentialStatusRevoked &&
		state.ExpiresAt != nil && !state.ExpiresAt.After(now) {
		state.Status = OpenAIBPSCredentialStatusExpired
		if state.ErrorCode == "" {
			state.ErrorCode = openAIBPSExpiredErrorCode
		}
	}
	return state
}

// normalizeOpenAIBPSCredentialErrorCode 归一化上游错误码；非法值回退为默认码。
func normalizeOpenAIBPSCredentialErrorCode(errorCode string) string {
	code := strings.ToLower(strings.TrimSpace(errorCode))
	if !openAIBPSCredentialErrorCodePattern.MatchString(code) {
		return openAIBPSDefaultAuthErrorCode
	}
	return code
}

// openAIBPSCredentialStateExtraFromAccount 读取并解码 extra 中的诊断状态。
// 兼容 JSON 往返后的 map[string]any 与直接构造的结构体。
func openAIBPSCredentialStateExtraFromAccount(account *Account) (OpenAIBPSCredentialStateExtra, bool) {
	var extra OpenAIBPSCredentialStateExtra
	if account == nil || account.Extra == nil {
		return extra, false
	}
	raw, ok := account.Extra[OpenAIBPSCredentialStateExtraKey]
	if !ok || raw == nil {
		return extra, false
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return extra, false
	}
	if err := json.Unmarshal(encoded, &extra); err != nil {
		return extra, false
	}
	switch extra.Status {
	case OpenAIBPSCredentialStatusNormal,
		OpenAIBPSCredentialStatusExpired,
		OpenAIBPSCredentialStatusRevoked,
		OpenAIBPSCredentialStatusAuthFailed:
		return extra, true
	default:
		return extra, false
	}
}

// openAIBPSCredentialStateExtraIsCurrent 判断诊断是否仍对应当前凭证。
// token 指纹或账号 ID 任一不一致都视为陈旧；当前 token 为空时一律视为陈旧。
func openAIBPSCredentialStateExtraIsCurrent(saved OpenAIBPSCredentialStateExtra, account *Account) bool {
	accessToken := account.OpenAIBPSAccessToken()
	if accessToken == "" {
		return false
	}
	if saved.TokenFingerprint != "" &&
		saved.TokenFingerprint != OpenAIBPSCredentialTokenFingerprint(accessToken) {
		return false
	}
	return saved.AccountID == account.OpenAIBPSAccountID()
}
