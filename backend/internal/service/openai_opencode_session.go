package service

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/tidwall/gjson"
)

const (
	openCodeSessionHeader         = "X-OpenCode-Session"
	openCodeInboundBodyContextKey = "opencode_inbound_body"

	// openCodeUpstreamUserAgent 是发往官方 OpenCode 上游的规范 User-Agent。
	// opencode.ai 前置 Cloudflare 按客户端 UA 签名做 bot 拦截：Python-urllib 等
	// 编程库 UA 会被 CF error code 1010 拒绝并返回 403。网关透传白名单放行
	// user-agent，客户端自报身份原样到达上游会命中 WAF；该 403 再被计入账号
	// 凭证类 strike，健康账号因此被自动禁用。取值沿用真实 opencode 客户端的
	// UA 格式（opencode/<version>）。
	//
	// **UA 版本是免费层门禁的硬性检查项，且上游只接受一个闭区间：**
	//
	//	1.18.0 <= version <= 1.18.33  -> 放行（200）
	//	version <  1.18.0             -> 426 UpgradeRequired
	//	version >= 1.18.34            -> 403 FreeTierError
	//
	// 上界由 2026-10-15 单变量实测二分确定（同一 key / body / 出口，只改 UA
	// 版本号：1.18.32、1.18.33 -> 200；1.18.34、1.18.35 -> 403；1.18.31 复测
	// -> 200），UA 的 `ai-sdk/... runtime/bun/...` 后缀对判定无影响（裸
	// `opencode/1.18.0` 与带后缀同样 200）。下界 1.18.0 见 1.17.9 -> 426。
	//
	// 2026-10-14 13:45-13:53 UTC 上游收紧了版本带：此前 1.19.0 可用（2026-10-13
	// 我们把 UA 从 1.18.31 升到 1.19.0 当时无害），收紧后官方最新版 1.19.0
	// 落到带外 → 全站 403 FreeTierError。官方 1.19.0 客户端自身同样被拒，
	// 属**上游回归**。因此「顺手把版本号升到官方最新」是危险操作：上游可能
	// 反过来只接受旧版本带；改版本号前必须重测当前区间，并同步更新
	// openai_opencode_freetier_test.go 的版本带断言。
	//
	// 取值 1.18.31 的理由：落在带内中段（距下界 31 个 patch，距上界 2 个
	// patch），且是真实存在的官方 release。子版本取自官方发布产物实测：
	// ai-sdk/provider-utils 为 4.0.23（二进制内 `var QF="4.0.23"`），
	// Bun runtime 为 1.3.14（`bun/1.3.14`）。
	//
	// 2026-10-13 免费层稳定 403 FreeTierError 还叠加了会话 id **编码**的问题
	// （hex 段必须是官方 tA() 时间戳编码，低 12 位为 0xFFE 只是其结构推论），
	// 见 openCodeGateSessionLowHex 与 docs/opencode-free-tier-gate-runbook.md。
	openCodeUpstreamUserAgent = "opencode/1.18.31 ai-sdk/provider-utils/4.0.23 runtime/bun/1.3.14"
)

// rememberOpenCodeInboundBody keeps the client request body so protocol
// conversion (Responses↔Anthropic↔Chat Completions) can still recover
// prompt_cache_key / metadata.user_id after those fields are dropped.
func rememberOpenCodeInboundBody(c *gin.Context, body []byte) {
	if c == nil || len(body) == 0 {
		return
	}
	c.Set(openCodeInboundBodyContextKey, body)
}

func openCodeInboundBodies(c *gin.Context) [][]byte {
	if c == nil {
		return nil
	}
	raw, ok := c.Get(openCodeInboundBodyContextKey)
	if !ok {
		return nil
	}
	body, ok := raw.([]byte)
	if !ok || len(body) == 0 {
		return nil
	}
	return [][]byte{body}
}

// applyOpenCodeSessionHeader sets x-opencode-session on outbound inference
// requests. OpenCode Go requires a per-conversation value from 2026-09-05
// (MissingSessionID). Prefer caller headers, then the documented body session
// fields (OpenAI prompt_cache_key / Anthropic metadata.user_id), then any
// already applied account override. A generated UUID is last-resort only for
// probes and clients that omit every stable identifier — a new UUID each turn
// would miss upstream prompt cache.
func applyOpenCodeSessionHeader(c *gin.Context, account *Account, targetURL string, headers http.Header, bodies ...[]byte) {
	if account == nil || account.Type != AccountTypeAPIKey || headers == nil {
		return
	}
	if !shouldSendOpenCodeSessionHeader(account, targetURL) {
		return
	}

	payloads := append(openCodeInboundBodies(c), bodies...)
	sessionID := resolveOpenCodeSessionID(c, headers, shouldGenerateOpenCodeSession(account, targetURL), payloads...)
	if sessionID == "" {
		return
	}
	for key := range headers {
		if strings.EqualFold(key, openCodeSessionHeader) {
			delete(headers, key)
		}
	}
	headers.Set(openCodeSessionHeader, sessionID)
}

func shouldSendOpenCodeSessionHeader(account *Account, targetURL string) bool {
	if account != nil && account.IsOpenCodeGoPlan() {
		return true
	}
	return isOfficialOpenCodeHost(targetURL)
}

func shouldGenerateOpenCodeSession(account *Account, targetURL string) bool {
	if account != nil && account.IsOpenCodeGoPlan() {
		return true
	}
	parsed, err := url.Parse(targetURL)
	if err != nil {
		return false
	}
	return strings.EqualFold(parsed.Scheme, "https") &&
		strings.EqualFold(parsed.Hostname(), "opencode.ai") &&
		strings.Contains(parsed.Path, "/zen/go")
}

func isOfficialOpenCodeHost(targetURL string) bool {
	parsed, err := url.Parse(targetURL)
	if err != nil {
		return false
	}
	return strings.EqualFold(parsed.Scheme, "https") && strings.EqualFold(parsed.Hostname(), "opencode.ai")
}

func isOfficialCommandCodeHost(targetURL string) bool {
	parsed, err := url.Parse(targetURL)
	if err != nil {
		return false
	}
	return strings.EqualFold(parsed.Scheme, "https") && strings.EqualFold(parsed.Hostname(), "api.commandcode.ai")
}

// applyOpenCodeUpstreamUserAgent 将发往官方 OpenCode / Command Code 上游的出站
// User-Agent 收敛为规范客户端身份，覆盖客户端透传值与平台默认 UA。判定规则与
// x-opencode-session 同源：opencode 平台账号，或（任意平台账号）目标为官方主机。
// 必须先于 Account.ApplyHeaderOverrides 调用——账号 header_overrides 中显式
// 配置的 user-agent 仍拥有最终决定权。
func applyOpenCodeUpstreamUserAgent(account *Account, targetURL string, headers http.Header) {
	if headers == nil {
		return
	}

	userAgent := ""
	switch {
	case isOfficialCommandCodeHost(targetURL):
		userAgent = CodexCanonicalUserAgent()
	case account != nil && account.IsOpenCodeGo(), isOfficialOpenCodeHost(targetURL):
		userAgent = openCodeUpstreamUserAgent
	default:
		return
	}
	if userAgent == "" {
		return
	}

	// 先删任意大小写变体再写入：透传链路与覆写直写 map 可能残留非 canonical 键。
	for key := range headers {
		if strings.EqualFold(key, "User-Agent") {
			delete(headers, key)
		}
	}
	headers.Set("User-Agent", userAgent)
}

func resolveOpenCodeSessionID(c *gin.Context, headers http.Header, generate bool, bodies ...[]byte) string {
	if c != nil && c.Request != nil {
		if sessionID := sanitizeSessionID(c.GetHeader(openCodeSessionHeader)); sessionID != "" {
			return sessionID
		}
		if sessionID := sanitizeSessionID(explicitOpenAIHeaderSessionID(c)); sessionID != "" {
			return sessionID
		}
		if sessionID := sanitizeSessionID(ClaudeCodeSessionIDFromHeader(c)); sessionID != "" {
			return sessionID
		}
	}
	for _, body := range bodies {
		if sessionID := sanitizeSessionID(openCodeSessionIDFromPayload(body)); sessionID != "" {
			return sessionID
		}
	}
	if sessionID := sanitizeSessionID(existingOpenCodeSessionHeader(headers)); sessionID != "" {
		return sessionID
	}
	if generate {
		return uuid.NewString()
	}
	return ""
}

// openCodeSessionIDFromPayload reads the stable conversation id from documented
// request-body fields. Kimi Code / OpenAI clients send prompt_cache_key on
// Chat Completions and Responses; Anthropic clients send metadata.user_id on
// Messages. Neither field changes model behavior.
func openCodeSessionIDFromPayload(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	view := openAIRequestPayloadView(body)
	if sessionID := strings.TrimSpace(view.Get("prompt_cache_key").String()); sessionID != "" {
		return sessionID
	}
	return openCodeSessionIDFromMetadataUserID(view.Get("metadata.user_id").String())
}

func openCodeSessionIDFromMetadataUserID(userID string) string {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return ""
	}
	if strings.HasPrefix(userID, "{") {
		if sessionID := strings.TrimSpace(gjson.Get(userID, "session_id").String()); sessionID != "" {
			return sessionID
		}
	}
	return userID
}

func openCodeSessionHintBody(promptCacheKey string) []byte {
	key := strings.TrimSpace(promptCacheKey)
	if key == "" {
		return nil
	}
	return []byte(`{"prompt_cache_key":` + strconv.Quote(key) + `}`)
}

func existingOpenCodeSessionHeader(headers http.Header) string {
	if headers == nil {
		return ""
	}
	for key, values := range headers {
		if strings.EqualFold(key, openCodeSessionHeader) && len(values) > 0 {
			return values[0]
		}
	}
	return ""
}
