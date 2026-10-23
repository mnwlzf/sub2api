package service

import (
	"encoding/base64"
	"encoding/json"
	"math"
	"strconv"
	"strings"
	"time"
)

// BPS 凭证的解析、覆盖语义与脱敏。
//
// BPS 账号只持有 ChatGPT Access Token（credentials.access_token），可选
// credentials.chatgpt_account_id 覆盖 JWT 中的默认工作区。JWT 只解析 payload
// 提取账号 ID 与 exp，不验签：签名是否有效由上游判定，本地解析只服务于展示与
// 本地过期判断，因此解析失败必须返回空字段而不是报错——否则一条畸形 token
// 就能阻断账号创建/编辑。
const (
	// openAIBPSAuthClaimsKey 是 ChatGPT JWT 中承载账号信息的嵌套 claim 键。
	openAIBPSAuthClaimsKey = "https://api.openai.com/auth"
	// openAIBPSFlattenedAccountIDClaim 是部分实现把嵌套 claim 展平后的键。
	openAIBPSFlattenedAccountIDClaim = openAIBPSAuthClaimsKey + ".chatgpt_account_id"
	// openAIBPSAccountIDClaim 是顶层账号 ID claim 的常见写法。
	openAIBPSAccountIDClaim = "chatgpt_account_id"
	// OpenAIBPSRedactedAccessTokenPlaceholder 是脱敏展示 access_token 的占位符。
	// 管理端更新账号时回传该占位符表示"保留原 Access Token"。
	OpenAIBPSRedactedAccessTokenPlaceholder = "***"
)

// OpenAIBPSCredentials 是 BPS 账号凭证的解析结果。
//
// AccountID 已应用覆盖语义（credentials.chatgpt_account_id 优先于 JWT）；
// AccessToken 已去除首尾空白与误粘贴的 "Bearer " 前缀，供上游鉴权头直接使用。
type OpenAIBPSCredentials struct {
	AccessToken string
	AccountID   string
	ExpiresAt   *time.Time
}

// ParseOpenAIBPSCredentials 解析 BPS 账号的 credentials map。
//
// 解析范围：
//   - access_token：必需，去除首尾空白与 "Bearer " 前缀；
//   - chatgpt_account_id：可选覆盖值，优先于 JWT 中的账号 ID；
//   - JWT payload（不验签）：提取账号 ID 与 exp，失败时保持空字段。
//
// 本函数不返回错误：凭证是否可用只由上游响应决定。
func ParseOpenAIBPSCredentials(credentials map[string]any) OpenAIBPSCredentials {
	var parsed OpenAIBPSCredentials
	if credentials == nil {
		return parsed
	}
	parsed.AccessToken = normalizeOpenAIBPSAccessToken(stringValue(credentials["access_token"]))
	claims := parseOpenAIBPSJWTPayload(parsed.AccessToken)
	parsed.ExpiresAt = openAIBPSExpiresAtFromClaims(claims)
	// 显式填写的账号 ID 覆盖 JWT 默认值：运营可以选择与 token 默认工作区不同的
	// 账号，本地解析不得反过来覆盖运营的选择。
	parsed.AccountID = strings.TrimSpace(stringValue(credentials["chatgpt_account_id"]))
	if parsed.AccountID == "" {
		parsed.AccountID = openAIBPSAccountIDFromClaims(claims)
	}
	return parsed
}

// OpenAIBPSAccessToken 返回用于上游鉴权的 Access Token（已归一化）。
func (a *Account) OpenAIBPSAccessToken() string {
	if a == nil {
		return ""
	}
	return ParseOpenAIBPSCredentials(a.Credentials).AccessToken
}

// OpenAIBPSAccountID 返回上游 chatgpt-account-id 头的取值。
// credentials.chatgpt_account_id 覆盖优先，缺省时回退到 JWT payload。
func (a *Account) OpenAIBPSAccountID() string {
	if a == nil {
		return ""
	}
	return ParseOpenAIBPSCredentials(a.Credentials).AccountID
}

// OpenAIBPSExpiresAt 返回 JWT payload 中的 exp（UTC）；无 exp 或解析失败返回 nil。
func (a *Account) OpenAIBPSExpiresAt() *time.Time {
	if a == nil {
		return nil
	}
	return ParseOpenAIBPSCredentials(a.Credentials).ExpiresAt
}

// RedactOpenAIBPSCredentials 返回可安全返回给管理端的凭证副本。
//
// access_token 替换为占位符（空值直接省略），chatgpt_account_id 等非敏感字段
// 保留；refresh_token/id_token 不属于 BPS 凭证模型，历史脏数据也不返回前端。
// 不修改入参。
func RedactOpenAIBPSCredentials(credentials map[string]any) map[string]any {
	if credentials == nil {
		return nil
	}
	out := make(map[string]any, len(credentials))
	for key, value := range credentials {
		switch key {
		case "refresh_token", "id_token":
			continue
		case "access_token":
			if strings.TrimSpace(stringValue(value)) == "" {
				continue
			}
			out[key] = OpenAIBPSRedactedAccessTokenPlaceholder
		default:
			out[key] = value
		}
	}
	return out
}

// normalizeOpenAIBPSAccessToken 去空白并剥离误粘贴的 "Bearer " 前缀。
// 上游要求 Authorization: Bearer <token>，保存带前缀的值会导致双重前缀。
func normalizeOpenAIBPSAccessToken(token string) string {
	token = strings.TrimSpace(token)
	if len(token) > len("bearer ") && strings.EqualFold(token[:len("bearer ")], "bearer ") {
		token = strings.TrimSpace(token[len("bearer "):])
	}
	return token
}

// parseOpenAIBPSJWTPayload 只解码 JWT 的 payload 段，绝不校验签名。
// 任何格式问题都返回 nil，调用方按"无元数据"处理。
func parseOpenAIBPSJWTPayload(token string) map[string]any {
	parts := strings.Split(token, ".")
	if len(parts) != 3 || parts[1] == "" {
		return nil
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		// 少数实现会带 base64 padding，兼容一次。
		payload, err = base64.URLEncoding.DecodeString(parts[1])
		if err != nil {
			return nil
		}
	}
	var claims map[string]any
	if err := json.Unmarshal(payload, &claims); err != nil {
		return nil
	}
	return claims
}

// openAIBPSAccountIDFromClaims 按"嵌套 auth → 展平键 → 顶层键"的顺序提取账号 ID。
func openAIBPSAccountIDFromClaims(claims map[string]any) string {
	if claims == nil {
		return ""
	}
	if auth, ok := claims[openAIBPSAuthClaimsKey].(map[string]any); ok {
		if id := strings.TrimSpace(stringValue(auth[openAIBPSAccountIDClaim])); id != "" {
			return id
		}
	}
	for _, key := range []string{openAIBPSFlattenedAccountIDClaim, openAIBPSAccountIDClaim} {
		if id := strings.TrimSpace(stringValue(claims[key])); id != "" {
			return id
		}
	}
	return ""
}

// openAIBPSExpiresAtFromClaims 提取 JWT exp（Unix 秒）并做年份边界归一。
// 越界值按"无到期信息"处理，避免 time.Time 溢出产生假过期。
func openAIBPSExpiresAtFromClaims(claims map[string]any) *time.Time {
	if claims == nil {
		return nil
	}
	raw, ok := claims["exp"]
	if !ok || raw == nil {
		return nil
	}
	var seconds float64
	switch value := raw.(type) {
	case float64:
		seconds = value
	case json.Number:
		parsed, err := value.Float64()
		if err != nil {
			return nil
		}
		seconds = parsed
	case int64:
		seconds = float64(value)
	case int:
		seconds = float64(value)
	case string:
		parsed, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
		if err != nil {
			return nil
		}
		seconds = parsed
	default:
		return nil
	}
	if math.IsNaN(seconds) || math.IsInf(seconds, 0) {
		return nil
	}
	// time.Unix 的合法年份范围：公元 1 年 ~ 9999 年。
	if seconds < -62135596800 || seconds >= 253402300800 {
		return nil
	}
	expires := time.Unix(int64(seconds), 0).UTC()
	return &expires
}
