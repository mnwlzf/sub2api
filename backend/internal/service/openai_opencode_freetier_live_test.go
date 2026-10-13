//go:build live

package service

import (
	"bytes"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// TestLiveOpenCodeFreeTierGate 是对真实 OpenCode Zen 上游的端到端验收测试。
//
// 它不模拟上游：用实现本身产出的门禁头与 body 直接打 https://opencode.ai，
// 并同时验证「有门禁必 200」与「无门禁必 403」，从而证明伪装真的生效，而不是
// 只在单元测试里自洽。
//
// 仅在显式指定构建标签时运行（不进入常规 CI）：
//
//	go test -tags=live ./internal/service/ -run TestLiveOpenCodeFreeTierGate -v -count=1
//
// 密钥默认取官方免费层公开值 public，可用 OPENCODE_ZEN_API_KEY 覆盖。
//
// ⚠️ **子测试顺序是有意的**：上游对 403 的惩罚按 key 记账、约 15 分钟，因此三个
// 正向子测试（预期 200）全部排在前面，故意触发 403 的「对照组」排在最后。若把
// 对照组提前，它造成的封禁会让后面所有正向断言假失败。同理，本测试通过后
// 15 分钟内不要再跑其他 live 用例。
func TestLiveOpenCodeFreeTierGate(t *testing.T) {
	apiKey := strings.TrimSpace(os.Getenv("OPENCODE_ZEN_API_KEY"))
	if apiKey == "" {
		apiKey = "public"
	}
	const endpoint = "https://opencode.ai/zen/v1/chat/completions"
	const model = "mimo-v2.6-flash-free"

	client := &http.Client{Timeout: 60 * time.Second}

	do := func(t *testing.T, headers http.Header, body []byte) (int, string) {
		t.Helper()
		req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(body))
		require.NoError(t, err)
		req.Header.Set("Authorization", "Bearer "+apiKey)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", openCodeUpstreamUserAgent)
		for key, values := range headers {
			for _, v := range values {
				req.Header.Set(key, v)
			}
		}
		resp, err := client.Do(req)
		require.NoError(t, err)
		defer func() { _ = resp.Body.Close() }()
		raw, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		return resp.StatusCode, string(raw)
	}

	account := openCodeGateTestAccount(nil)
	plain := []byte(`{"model":"` + model + `","stream":true,"messages":[{"role":"user","content":"say ok"}]}`)

	// 注意：正向子测试全部排在前面，故意 403 的对照组在最后，见文件头注释。

	t.Run("实验组：门禁伪装必然放行", func(t *testing.T) {
		// 用真实实现产出 body 与头，而不是手写常量。
		gated, err := applyOpenCodeFreeTierGateBody(account, plain, openCodeGateDialectChat)
		require.NoError(t, err)
		require.True(t, gjson.GetBytes(gated, "stream").Bool(), "实现必须强制 stream:true")

		headers := http.Header{}
		// 模拟真实链路：applyOpenCodeSessionHeader 先落一个 UUID 值，
		// 再由门禁改写为满足门禁的会话 id。
		headers.Set(openCodeSessionHeader, "3f2504e0-4f89-11d3-9a0c-0305e82c3301")
		applyOpenCodeFreeTierGateSession(account, headers, gated)
		session := headers.Get(openCodeSessionHeader)
		require.True(t, isOpenCodeGateSessionID(session),
			"会话值必须同时满足形状与位不变量（hex 段低位 %s），实际 %q", openCodeGateSessionLowHex, session)

		status, body := do(t, headers, gated)
		require.Equal(t, http.StatusOK, status, "门禁伪装未通过上游校验，响应: %s", body)
		require.Contains(t, body, "data:", "预期收到 SSE 帧")
		require.NotContains(t, body, "FreeTierError")
	})

	t.Run("边界组：客户端不提供任何会话标识时也必须放行", func(t *testing.T) {
		// 这是实现过程中发现并修复的真实缺口：Zen 账号的
		// shouldGenerateOpenCodeSession 为 false（它只认 /zen/go 路径），
		// 因此 applyOpenCodeSessionHeader 不会补会话头，门禁必须自行派生。
		gated, err := applyOpenCodeFreeTierGateBody(account, plain, openCodeGateDialectChat)
		require.NoError(t, err)

		headers := http.Header{} // 刻意留空：模拟裸 curl / requests 客户端
		applyOpenCodeFreeTierGateSession(account, headers, gated)

		session := headers.Get(openCodeSessionHeader)
		require.True(t, isOpenCodeGateSessionID(session),
			"门禁必须自行派生出满足形状与位不变量的会话值，实际 %q", session)

		status, body := do(t, headers, gated)
		require.Equal(t, http.StatusOK, status,
			"无客户端会话标识时门禁仍未通过上游校验，响应: %s", body)
		require.NotContains(t, body, "FreeTierError")
	})

	t.Run("无工具请求组：补官方工具集 + tool_choice none 也必须放行", func(t *testing.T) {
		// 模拟压缩/总结类请求：客户端不带任何工具。门禁会补官方 6 个工具并设
		// tool_choice none。该组合无法从单元测试证明上游接受，必须打真实上游。
		toolLess := []byte(`{"model":"` + model + `","stream":true,"messages":[{"role":"user","content":"summarize: hello"}]}`)

		gated, err := applyOpenCodeFreeTierGateBody(account, toolLess, openCodeGateDialectChat)
		require.NoError(t, err)
		require.Equal(t, "none", gjson.GetBytes(gated, "tool_choice").String())
		require.Len(t, gjson.GetBytes(gated, "tools").Array(), 6)

		headers := http.Header{}
		applyOpenCodeFreeTierGateSession(account, headers, gated)

		status, body := do(t, headers, gated)
		require.Equal(t, http.StatusOK, status,
			"无工具请求 + tool_choice none 未通过上游校验，响应: %s", body)
		require.NotContains(t, body, "FreeTierError")
	})

	// 对照组放在最后：它必然触发 403，而上游对 403 的惩罚按 key 记账约 15 分钟。
	// 若此组排在前面，后面所有正向断言都会因封禁而假失败。
	t.Run("对照组：无门禁必然被拒（最后运行，会封禁 key 约 15 分钟）", func(t *testing.T) {
		status, body := do(t, http.Header{}, plain)
		require.Equal(t, http.StatusForbidden, status,
			"预期上游拒绝未伪装请求；若此处通过，说明上游门禁已放宽，本特性需要重新评估。响应: %s", body)
		require.Contains(t, body, "FreeTierError")
	})
}
