package service

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

// 出站线序收口的行为锁：官方 Codex HTTP/WS 业务路径都不发送下划线会话头、
// installation-id 头与 accept-language；WS 额外保留 OpenAI-Beta 与
// x-client-request-id。改动这两个名单会直接改变上游可观测的客户端特征，
// 因此必须有测试拦住。
func TestProjectCodexOutboundHeadersHTTP(t *testing.T) {
	h := make(http.Header)
	h.Set("session_id", "underscored")
	h.Set("conversation_id", "conversation")
	h.Set("session-id", "hyphen-session")
	h.Set("x-codex-installation-id", "install")
	h.Set("accept-language", "zh-CN")
	h.Set("OpenAI-Beta", "responses=experimental")
	h.Set("x-client-request-id", "request")
	h.Set("version", "0.156.1")

	projectCodexOutboundHeaders(h, false)

	require.Empty(t, h.Get("session_id"))
	require.Empty(t, h.Get("conversation_id"))
	require.Empty(t, h.Get("x-codex-installation-id"))
	require.Empty(t, h.Get("accept-language"))
	require.Empty(t, h.Get("OpenAI-Beta"), "HTTP 业务头集合不含 OpenAI-Beta")
	require.Equal(t, "hyphen-session", h.Get("session-id"))
	require.Equal(t, "hyphen-session", h.Get("thread-id"), "官方总是同时发 session-id 与 thread-id")
	require.Equal(t, "request", h.Get("x-client-request-id"))
	require.Equal(t, "0.156.1", h.Get("version"), "version 是官方头，不得删除")
}

func TestProjectCodexOutboundHeadersWebSocket(t *testing.T) {
	h := make(http.Header)
	h.Set("session_id", "underscored")
	h.Set("conversation_id", "conversation")
	h.Set("x-codex-installation-id", "install")
	h.Set("accept-language", "zh-CN")
	h.Set("OpenAI-Beta", "responses_websockets=2026-02-06")
	h.Set("x-client-request-id", "request")

	projectCodexOutboundHeaders(h, true)

	require.Empty(t, h.Get("session_id"))
	require.Empty(t, h.Get("conversation_id"))
	require.Empty(t, h.Get("x-codex-installation-id"))
	require.Empty(t, h.Get("accept-language"))
	require.Equal(t, "responses_websockets=2026-02-06", h.Get("OpenAI-Beta"), "WS 握手保留 OpenAI-Beta")
	require.Equal(t, "request", h.Get("x-client-request-id"), "WS 握手保留 x-client-request-id")
}

// 关闭强制统一身份时必须完全不动头集合（回滚语义）。
func TestProjectCodexOutboundHeadersDisabledKeepsHeaders(t *testing.T) {
	previous := codexIdentityEnforcement.Load()
	SetCodexIdentityEnforcementEnabled(false)
	t.Cleanup(func() { SetCodexIdentityEnforcementEnabled(previous) })

	h := make(http.Header)
	h.Set("session_id", "underscored")
	h.Set("accept-language", "zh-CN")

	projectCodexOutboundHeaders(h, false)

	require.Equal(t, "underscored", h.Get("session_id"))
	require.Equal(t, "zh-CN", h.Get("accept-language"))
}

func TestStripCodexTurnMetadataHeaderKeys(t *testing.T) {
	raw := `{"installation_id":"i","session_id":"s","tool_namespaces_info":{"ns":{"name":"ns","functions":{"f":{"name":"f"}}}},"workspaces":{"C:\\work":{}}}`

	stripped := stripCodexTurnMetadataHeaderKeys(raw)

	require.NotContains(t, stripped, "tool_namespaces_info")
	require.Contains(t, stripped, `"installation_id":"i"`)
	require.Contains(t, stripped, "workspaces")
	require.Empty(t, stripCodexTurnMetadataHeaderKeys(""), "空值原样返回")
	require.Equal(t, "not-json", stripCodexTurnMetadataHeaderKeys("not-json"), "非法 JSON 原样返回")
}
