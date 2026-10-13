//go:build live

package service

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

// 本文件是 OpenCode 免费层门禁的**排查探针**，不是回归测试：它只发一次真实请求，
// 把完整的请求头 / body / 响应头 / 响应体原样打出来，用于单变量消融。
//
// 用法（详见 docs/opencode-free-tier-gate-runbook.md）：
//
//	cd backend
//	HTTPS_PROXY=http://127.0.0.1:10809 HTTP_PROXY=http://127.0.0.1:10809 \
//	  ZEN_PROBE=<mode> ZEN_PROBE_OUT=/tmp/probe.txt \
//	  OPENCODE_ZEN_API_KEY=<key> GOTOOLCHAIN=go1.27.2 \
//	  go test -tags=live ./internal/service/ -run TestLiveOpenCodeFreeTierProbe -v -count=1
//
// mode 取值：
//
//	current   : 完全由生产实现产出的头与 body。**预期 200**。这是修复是否成立的
//	            第一手证据，且不会触发封禁。
//	superset  : 与 current 相同的生产产出，另加参考插件发送的全部额外头
//	            （Accept / Content-Type / X-Opencode-Project / Request / Client /
//	            Session-Id）。**预期 200**，用于证明这些头非必需。
//	shapeonly : 生产 body + 形状合法但 hex 段低位不是 0xFFE 的会话值（即修复前
//	            用 sha256 派生的形态）。**预期 403 FreeTierError**。
//	            这是复现根因的对照组，会触发约 15 分钟封禁，务必最后跑。
//	nogate    : 完全不施加门禁的裸请求。**预期 403 FreeTierError**，同样会封禁。
//
// ⚠️ 上游对 403 的惩罚是按 key 记账、约 15 分钟。因此：
//   - 一个 403 之后至少等 15 分钟再发下一个请求，否则后续结果全部被污染；
//   - 先跑 current / superset（预期 200），最后才跑 shapeonly / nogate；
//   - 实测中曾把紧跟在 403 之后 5 分钟的 superset 请求误读成「额外头有问题」，
//     实际是封禁未解除——排查时务必核对每次响应的 Date 头与上一次 403 的间隔。
func TestLiveOpenCodeFreeTierProbe(t *testing.T) {
	mode := strings.TrimSpace(os.Getenv("ZEN_PROBE"))
	if mode == "" {
		t.Skip("ZEN_PROBE not set (current|superset|shapeonly|nogate)")
	}
	apiKey := strings.TrimSpace(os.Getenv("OPENCODE_ZEN_API_KEY"))
	if apiKey == "" {
		apiKey = "public"
	}

	const endpoint = "https://opencode.ai/zen/v1/chat/completions"
	const model = "mimo-v2.6-flash-free"
	plain := []byte(`{"model":"` + model + `","stream":true,"messages":[{"role":"user","content":"say ok"}]}`)

	account := openCodeGateTestAccount(nil)
	headers := http.Header{}
	var body []byte

	// gatedBody 由生产实现产出，三个模式共用，保证「只改一个变量」。
	gatedBody := func() []byte {
		gated, err := applyOpenCodeFreeTierGateBody(account, plain, openCodeGateDialectChat)
		if err != nil {
			t.Fatalf("applyOpenCodeFreeTierGateBody: %v", err)
		}
		return gated
	}

	switch mode {
	case "current":
		body = gatedBody()
		// 模拟真实链路：applyOpenCodeSessionHeader 先落一个 UUID 值，
		// 再由门禁改写为满足门禁的会话 id。
		headers.Set(openCodeSessionHeader, "3f2504e0-4f89-11d3-9a0c-0305e82c3301")
		applyOpenCodeFreeTierGateSession(account, headers, body)
		headers.Set("User-Agent", openCodeUpstreamUserAgent)

	case "superset":
		body = gatedBody()
		headers.Set(openCodeSessionHeader, "3f2504e0-4f89-11d3-9a0c-0305e82c3301")
		applyOpenCodeFreeTierGateSession(account, headers, body)
		headers.Set("User-Agent", openCodeUpstreamUserAgent)
		// 参考插件额外发送、但实测非必需的头。
		headers.Set("Accept", "*/*")
		headers.Set("Content-Type", "application/json")
		headers.Set("X-Opencode-Project", "global")
		headers.Set("X-Opencode-Request", canonicalOpenCodeID("msg", "probe:"+model))
		headers.Set("X-Opencode-Client", "cli")
		headers.Set("X-Opencode-Session-Id", headers.Get(openCodeSessionHeader))

	case "shapeonly":
		// 复现修复前形态：形状合法、hex 段低位不为 0xFFE。用 canonicalOpenCodeID
		// （sha256 派生 hex，即旧算法 / 上游插件 canonicalID 的同构实现）构造，
		// 并搜索出一个确实位不合法的样本，避免 1/4096 的偶然命中让对照组失真。
		body = gatedBody()
		headers.Set("User-Agent", openCodeUpstreamUserAgent)
		ses := ""
		for i := 0; i < 256; i++ {
			candidate := canonicalOpenCodeID("ses", "probe:"+model+"\x00say ok "+strconv.Itoa(i))
			if !isOpenCodeGateSessionID(candidate) {
				ses = candidate
				break
			}
		}
		if ses == "" {
			t.Fatalf("在 256 个哈希派生样本里没有找到位不合法的值（算法或正则已变）")
		}
		headers.Set(openCodeSessionHeader, ses)
		headers.Set(openCodeSessionAffinityHeader, ses)
		headers.Set(openCodeSessionIDHeader, ses)

	case "nogate":
		body = plain
		headers.Set("User-Agent", openCodeUpstreamUserAgent)

	default:
		t.Fatalf("unknown ZEN_PROBE=%q (current|superset|shapeonly|nogate)", mode)
	}

	req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	for k, vs := range headers {
		for _, v := range vs {
			req.Header.Set(k, v)
		}
	}

	var dump strings.Builder
	fmt.Fprintf(&dump, "=== PROBE MODE: %s ===\n", mode)
	fmt.Fprintf(&dump, "=== ENDPOINT: %s ===\n", endpoint)
	fmt.Fprintf(&dump, "=== AUTH: Bearer %s (len=%d) ===\n", maskOpenCodeProbeKey(apiKey), len(apiKey))
	fmt.Fprintf(&dump, "=== REQUEST HEADERS (%d) ===\n", len(req.Header))
	for k, vs := range req.Header {
		// 头必须逐个过掩码：Authorization 里就是 API key。
		// 历史教训：直接打印 req.Header 会把完整 key 明文写进 dump 文件。
		masked := make([]string, 0, len(vs))
		for _, v := range vs {
			masked = append(masked, maskOpenCodeProbeHeaderValue(k, v))
		}
		fmt.Fprintf(&dump, "  %s: %s\n", k, strings.Join(masked, ", "))
	}
	writeOpenCodeProbeSessionDiagnostics(&dump, req.Header.Get(openCodeSessionHeader))
	fmt.Fprintf(&dump, "=== REQUEST BODY (%d bytes) ===\n%s\n", len(body), string(body))

	client := &http.Client{Timeout: 90 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		fmt.Fprintf(&dump, "=== TRANSPORT ERROR: %v ===\n", err)
		t.Log("\n" + dump.String())
		t.Fatalf("transport error: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)

	fmt.Fprintf(&dump, "=== RESPONSE STATUS: %d %s ===\n", resp.StatusCode, resp.Status)
	fmt.Fprintf(&dump, "=== RESPONSE HEADERS ===\n")
	for k, vs := range resp.Header {
		fmt.Fprintf(&dump, "  %s: %s\n", k, strings.Join(vs, ", "))
	}
	out := string(raw)
	if len(out) > 8000 {
		out = out[:8000] + fmt.Sprintf("\n...[TRUNCATED %d bytes]", len(raw)-8000)
	}
	fmt.Fprintf(&dump, "=== RESPONSE BODY (%d bytes) ===\n%s\n", len(raw), out)

	// 兜底：无论上面哪条路径漏了掩码，这里把整份 dump 里的原始 key 再抹一遍。
	// 这是唯一允许写出/打印 dump 的出口，保证 key 不可能进入日志文件。
	dumpText := scrubOpenCodeProbeSecrets(dump.String(), apiKey)

	if p := strings.TrimSpace(os.Getenv("ZEN_PROBE_OUT")); p != "" {
		if werr := os.WriteFile(p, []byte(dumpText), 0o644); werr != nil {
			t.Logf("write dump failed: %v", werr)
		}
	}
	t.Log("\n" + dumpText)
}

// scrubOpenCodeProbeSecrets 把整份 dump 里出现的原始 key 替换为掩码形态。
// 作为写文件/打日志前的最后一道关卡，防止任何新增的头或 body 字段把 key 带出去。
func scrubOpenCodeProbeSecrets(dump, apiKey string) string {
	if apiKey == "" || apiKey == "public" {
		return dump
	}
	return strings.ReplaceAll(dump, apiKey, maskOpenCodeProbeKey(apiKey))
}

// maskOpenCodeProbeHeaderValue 掩码携带凭据的请求头取值。
func maskOpenCodeProbeHeaderValue(name, value string) string {
	switch {
	case strings.EqualFold(name, "Authorization"):
		if len(value) > 7 && strings.EqualFold(value[:7], "Bearer ") {
			return "Bearer " + maskOpenCodeProbeKey(strings.TrimSpace(value[7:]))
		}
		return maskOpenCodeProbeKey(strings.TrimSpace(value))
	case strings.EqualFold(name, "X-Api-Key"), strings.EqualFold(name, "Api-Key"):
		return maskOpenCodeProbeKey(strings.TrimSpace(value))
	default:
		return value
	}
}

// writeOpenCodeProbeSessionDiagnostics 把会话 id 的门禁判定结果写进 dump：
// 形状、hex 段、低 12 位是否等于 0xFFE，以及官方编码的自洽校验。
// 排查时先看这几行，能立刻区分「形状问题」与「位不变量问题」。
func writeOpenCodeProbeSessionDiagnostics(dump *strings.Builder, session string) {
	fmt.Fprintf(dump, "=== SESSION DIAGNOSTICS ===\n")
	if session == "" {
		fmt.Fprintf(dump, "  session: <absent>\n  shape_ok: false\n  gate_ok: false\n")
		return
	}
	fmt.Fprintf(dump, "  session: %s\n", session)
	fmt.Fprintf(dump, "  shape_ok: %v\n", openCodeCanonicalSessionRe.MatchString(session))
	fmt.Fprintf(dump, "  gate_ok (shape + low12==%s): %v\n", openCodeGateSessionLowHex, isOpenCodeGateSessionID(session))
	if !openCodeCanonicalSessionRe.MatchString(session) {
		return
	}
	hexPart := session[4:16]
	fmt.Fprintf(dump, "  hex12: %s\n  low12: %s (required: %s)\n", hexPart, session[13:16], openCodeGateSessionLowHex)
	if value, perr := strconv.ParseUint(hexPart, 16, 64); perr == nil {
		derived := int64(value >> 12)
		// 官方编码的自洽等式：(hex>>12) + mintedAtMillis == epoch - 1。
		// 若 delta_ms 明显偏离 0（几秒以上），说明这个 id 不是当前时刻铸造的，
		// 而是复用的历史样本。
		fmt.Fprintf(dump, "  official encoding check: (hex>>12)+unixMilli = %d, epoch-1 = %d, delta_ms = %d\n",
			derived+time.Now().UnixMilli(), openCodeGateSessionEpochMillis-1,
			(openCodeGateSessionEpochMillis-1)-(derived+time.Now().UnixMilli()))
	}
}

// maskOpenCodeProbeKey 掩码 API key，**必须**覆盖 oc_sk_ / sk- 两种前缀方案。
// 历史教训：只匹配 sk- 的正则会让 oc_sk_ 前缀的完整 key 明文泄漏到日志里。
func maskOpenCodeProbeKey(key string) string {
	if key == "" {
		return "***"
	}
	scheme := ""
	for _, candidate := range []string{"oc_sk_", "sk-", "sk_"} {
		if strings.HasPrefix(key, candidate) {
			scheme = candidate
			break
		}
	}
	if scheme == "" {
		scheme = "***"
	}
	if len(key) <= len(scheme)+4 {
		return scheme + "***"
	}
	return scheme + "***" + key[len(key)-4:]
}
