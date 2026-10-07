//go:build unit

package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// 出站验收测试：断言注入内容确实出现在“最终发送给上游的字节”里，而不是只测
// 适配器返回值。sendCCUpstreamRequest 是所有 Chat 出站路径（Chat→Chat、
// Responses→Chat、Messages→Chat）的公共汇聚点，因此这里覆盖三条路径的共同行为。

func promptInjectionTestService(upstream *httpUpstreamRecorder, enabled bool) *OpenAIGatewayService {
	return &OpenAIGatewayService{
		cfg: &config.Config{Gateway: config.GatewayConfig{
			TextPromptInjectionEnabled: enabled,
		}},
		httpUpstream: upstream,
	}
}

func promptInjectionTestAccount() *Account {
	return &Account{
		ID:       4242,
		Platform: PlatformOpenAI,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"base_url": "https://api.openai.com/v1",
			"api_key":  "sk-test-not-a-real-key",
		},
	}
}

func promptInjectionTestGin(t *testing.T) *gin.Context {
	t.Helper()
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader("{}"))
	return c
}

func promptInjectionTestRecorder() *httpUpstreamRecorder {
	return &httpUpstreamRecorder{
		resp: &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"id":"x","choices":[]}`)),
		},
	}
}

func TestSendCCUpstreamRequest_InjectsPromptIntoFinalOutboundBody(t *testing.T) {
	upstream := promptInjectionTestRecorder()
	svc := promptInjectionTestService(upstream, true)
	c := promptInjectionTestGin(t)
	WithFrozenPromptPolicy(c, &FrozenPromptPolicy{
		Enabled:           true,
		VersionID:         7,
		Body:              "server-prompt",
		SupportedProfiles: []string{PromptProfileChatHTTP},
	})

	body := []byte(`{"model":"gpt-5","messages":[{"role":"system","content":"client-system"},{"role":"user","content":"hi"}]}`)
	resp, err := svc.sendCCUpstreamRequest(
		context.Background(), c, promptInjectionTestAccount(),
		"https://api.openai.com/v1/chat/completions", body,
		false, "token", "", "", PromptProfileChatHTTP,
	)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())

	// 断言最终出站字节：客户端原有 system 保留在前，注入的 system 紧随其后。
	require.Equal(t, "client-system", gjson.GetBytes(upstream.lastBody, "messages.0.content").String())
	require.Equal(t, "system", gjson.GetBytes(upstream.lastBody, "messages.1.role").String())
	require.Equal(t, "server-prompt", gjson.GetBytes(upstream.lastBody, "messages.1.content").String())
	require.Equal(t, "hi", gjson.GetBytes(upstream.lastBody, "messages.2.content").String())
	require.Equal(t, 3, len(gjson.GetBytes(upstream.lastBody, "messages").Array()))
}

func TestSendCCUpstreamRequest_FeatureOffLeavesBodyUnchanged(t *testing.T) {
	upstream := promptInjectionTestRecorder()
	svc := promptInjectionTestService(upstream, false)
	c := promptInjectionTestGin(t)
	// 即使有人往 context 里放了策略，总开关关闭时也不得注入。
	WithFrozenPromptPolicy(c, &FrozenPromptPolicy{
		Enabled:           true,
		VersionID:         7,
		Body:              "server-prompt",
		SupportedProfiles: []string{PromptProfileChatHTTP},
	})

	body := []byte(`{"model":"gpt-5","messages":[{"role":"user","content":"hi"}]}`)
	resp, err := svc.sendCCUpstreamRequest(
		context.Background(), c, promptInjectionTestAccount(),
		"https://api.openai.com/v1/chat/completions", body,
		false, "token", "", "", PromptProfileChatHTTP,
	)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())

	require.Equal(t, 1, len(gjson.GetBytes(upstream.lastBody, "messages").Array()))
	require.Equal(t, "hi", gjson.GetBytes(upstream.lastBody, "messages.0.content").String())
}

func TestSendCCUpstreamRequest_RetryDoesNotAccumulatePrompt(t *testing.T) {
	// 模拟同一个逻辑请求的两次尝试（换号重试）：策略已冻结，每次从不可变原始 body
	// 派生，因此最终出站都只包含一条注入消息，不会累积。
	upstream := promptInjectionTestRecorder()
	svc := promptInjectionTestService(upstream, true)
	c := promptInjectionTestGin(t)
	WithFrozenPromptPolicy(c, &FrozenPromptPolicy{
		Enabled:           true,
		VersionID:         7,
		Body:              "server-prompt",
		SupportedProfiles: []string{PromptProfileChatHTTP},
	})

	body := []byte(`{"model":"gpt-5","messages":[{"role":"user","content":"hi"}]}`)
	for attempt := 0; attempt < 2; attempt++ {
		resp, err := svc.sendCCUpstreamRequest(
			context.Background(), c, promptInjectionTestAccount(),
			"https://api.openai.com/v1/chat/completions", body,
			false, "token", "", "", PromptProfileChatHTTP,
		)
		require.NoError(t, err)
		require.NoError(t, resp.Body.Close())
	}

	require.Len(t, upstream.bodies, 2)
	// 两次尝试的最终出站字节完全一致。
	require.Equal(t, string(upstream.bodies[0]), string(upstream.bodies[1]))
	require.Equal(t, 2, len(gjson.GetBytes(upstream.bodies[1], "messages").Array()))
	require.Equal(t, "server-prompt", gjson.GetBytes(upstream.bodies[1], "messages.0.content").String())
}

func TestSendCCUpstreamRequest_UnsupportedProfileKeepsLegacyBody(t *testing.T) {
	// Messages→Chat 回退传入空 profile：保持既有行为，不注入、不报错。
	upstream := promptInjectionTestRecorder()
	svc := promptInjectionTestService(upstream, true)
	c := promptInjectionTestGin(t)
	WithFrozenPromptPolicy(c, &FrozenPromptPolicy{
		Enabled:           true,
		VersionID:         7,
		Body:              "server-prompt",
		SupportedProfiles: []string{PromptProfileChatHTTP},
	})

	body := []byte(`{"model":"gpt-5","messages":[{"role":"user","content":"hi"}]}`)
	resp, err := svc.sendCCUpstreamRequest(
		context.Background(), c, promptInjectionTestAccount(),
		"https://api.openai.com/v1/chat/completions", body,
		false, "token", "", "", "",
	)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())

	require.Equal(t, 1, len(gjson.GetBytes(upstream.lastBody, "messages").Array()))
}

func TestSendCCUpstreamRequest_ProfileNotAllowedFailsLoudly(t *testing.T) {
	// 版本只允许 responses_http，却走到 chat_http 出站：必须失败，不能静默不注入。
	upstream := promptInjectionTestRecorder()
	svc := promptInjectionTestService(upstream, true)
	c := promptInjectionTestGin(t)
	WithFrozenPromptPolicy(c, &FrozenPromptPolicy{
		Enabled:           true,
		VersionID:         7,
		Body:              "server-prompt",
		SupportedProfiles: []string{PromptProfileResponsesHTTP},
	})

	body := []byte(`{"model":"gpt-5","messages":[{"role":"user","content":"hi"}]}`)
	_, err := svc.sendCCUpstreamRequest(
		context.Background(), c, promptInjectionTestAccount(),
		"https://api.openai.com/v1/chat/completions", body,
		false, "token", "", "", PromptProfileChatHTTP,
	)
	require.Error(t, err)
	require.ErrorIs(t, err, ErrPromptProfileUnsupported)
	// 注入失败时不得向上游发出请求。
	require.Empty(t, upstream.bodies)
}
