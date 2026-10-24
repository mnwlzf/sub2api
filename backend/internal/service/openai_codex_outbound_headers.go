package service

import (
	"net/http"
	"strings"

	"github.com/tidwall/sjson"
)

// 本文件是 Codex OAuth/SetupToken 出站请求的「官方线序收口」。
//
// 背景：官方 Codex 客户端在 ChatGPT 业务路径上只发送一组固定头。代理链为了
// 兼容历史行为会在入站白名单透传之外继续补齐一些头，这些头对上游功能没有
// 贡献，却是真实客户端永远不会产生的可判别特征（上游可据此做风控/降载）。
// 参考实现：sub4api（huoji120/sub4api，LGPL-3.0）的
// backend/internal/service/openai_codex_identity.go（projectCodexOutboundHeaders），
// 其逐项依据 codex-rs HEAD f4e18a95bb：
//
//   - session_id、conversation_id：官方会话头只有连字符形式
//     session-id / thread-id（codex-api/src/requests/headers.rs:5-11），
//     下划线形式已在 fix: drop underscored id headers (#22193) 中删除。
//   - x-codex-installation-id：官方仅作为 client_metadata 键发送
//     （core/src/responses_metadata.rs:325-334），从不作为 HTTP 头。
//   - OpenAI-Beta：只在 build_websocket_headers 写入，属 WS 握手专有；
//     HTTP 业务头集合由 build_responses_headers 产生。
//   - accept-language：reqwest 不写该头，官方业务请求里不存在。
//
// 反例（官方确实发送，不得删）：version 来自内置 provider 的 http_headers；
// x-client-request-id 在 HTTP 上写为 thread_id。

// codexHTTPNonOfficialHeaders 是官方 Codex 在 ChatGPT 业务 HTTP 路径
// （POST /backend-api/codex/responses）上从不发送的请求头。
var codexHTTPNonOfficialHeaders = [...]string{
	"OpenAI-Beta",
	"session_id",
	"conversation_id",
	"x-codex-installation-id",
	"accept-language",
}

// codexWSNonOfficialHeaders 是 WS 握手上官方不发的同名子集。WS 与 HTTP 的差异
// 必须保留：OpenAI-Beta 与 x-client-request-id 是官方 WS 握手确实发送的，
// 故不在 WS 名单内。
var codexWSNonOfficialHeaders = [...]string{
	"session_id",
	"conversation_id",
	"x-codex-installation-id",
	"accept-language",
}

// projectCodexOutboundHeaders 把出站头收口成官方线序：删除官方在该传输上不发
// 的头，并保证连字符会话头确实存在（官方总是同时发 session-id 与 thread-id）。
//
// 必须在所有其它头改写（指纹收敛、账号 namespace、client_metadata 投影、账号级
// 覆写）之后调用——它是最后一道收口，任何更早的补齐都可能被后续步骤重新引入。
//
// 与强制统一身份共用 gateway.disable_codex_identity_enforcement：该开关的语义就是
// 「上游策略变动时退回旧线序」，因此关闭后这里保持原样不动。
func projectCodexOutboundHeaders(h http.Header, websocket bool) {
	if h == nil || !codexIdentityEnforcement.Load() {
		return
	}
	var nonOfficial []string
	if websocket {
		nonOfficial = codexWSNonOfficialHeaders[:]
	} else {
		nonOfficial = codexHTTPNonOfficialHeaders[:]
	}
	for _, name := range nonOfficial {
		deleteHeaderAllForms(h, name)
	}
	// 官方 build_session_headers 在 session-id 与 thread-id 均存在时必然同时写出
	// 两个连字符头；这里只把已有的值补成对，不凭空造 ID（造 ID 是上游派生层的职责）。
	if session := strings.TrimSpace(h.Get("session-id")); session != "" && strings.TrimSpace(h.Get("thread-id")) == "" {
		h.Set("thread-id", session)
	}
}

// stripCodexTurnMetadataHeaderKeys 从「作为 HTTP 头携带」的 turn metadata 中删掉
// 只应留在帧内 client_metadata 的字段。
//
// 官方 WS 握手把完整 blob 放在 response.create 的 client_metadata 里，握手头
// 里则是去掉 tool_namespaces_info 的投影；把大对象塞进 Upgrade 头既不符合官方
// 线序，也会撑大握手体积。解析失败时原样返回，避免把合法元数据改坏。
func stripCodexTurnMetadataHeaderKeys(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return raw
	}
	stripped, err := sjson.Delete(raw, "tool_namespaces_info")
	if err != nil {
		return raw
	}
	return stripped
}

// ensureCodexOfficialSessionHeaders 在缺少官方连字符会话头时按客户端会话键派生
// 账号命名空间内的 UUIDv7 会话/线程标识，并补齐 x-client-request-id（官方在 HTTP
// 上写 thread_id）。
//
// 官方 build_session_headers 总是同时发送 session-id 与 thread-id；第三方客户端
// 只带下划线 session_id 或只带 prompt_cache_key 时，出站会完全没有会话头，既丢失
// 会话隔离，也与官方形状不符。这里只补缺失项，已有值（客户端或指纹收敛）一律不覆盖。
func ensureCodexOfficialSessionHeaders(h http.Header, account *Account, apiKeyID int64, promptCacheKey string) {
	if h == nil || account == nil || codexAccountIdentityNamespace(account) == "" {
		return
	}
	seed := strings.TrimSpace(promptCacheKey)
	if seed == "" {
		seed = strings.TrimSpace(h.Get("session-id"))
	}
	if seed == "" {
		seed = strings.TrimSpace(h.Get("thread-id"))
	}
	if seed == "" {
		return
	}
	if strings.TrimSpace(h.Get("session-id")) == "" {
		if session := scopeCodexAccountIdentityValue(account, apiKeyID, "session", seed); session != "" {
			h.Set("session-id", session)
		}
	}
	if strings.TrimSpace(h.Get("thread-id")) == "" {
		if thread := scopeCodexAccountIdentityValue(account, apiKeyID, "thread", seed); thread != "" {
			h.Set("thread-id", thread)
		}
	}
	if strings.TrimSpace(h.Get("x-client-request-id")) == "" {
		if thread := strings.TrimSpace(h.Get("thread-id")); thread != "" {
			h.Set("x-client-request-id", thread)
		}
	}
}
