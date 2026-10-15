package service

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// openCodeGateTestAccount 构造 OpenCode 平台账号，credentials 可覆盖门禁模式。
func openCodeGateTestAccount(credentials map[string]any) *Account {
	creds := map[string]any{"api_key": "sk-test"}
	for k, v := range credentials {
		creds[k] = v
	}
	return &Account{
		ID:          1,
		Platform:    PlatformOpenCodeGo,
		Type:        AccountTypeAPIKey,
		Credentials: creds,
	}
}

// openCodeGateTestSessionID 是 2026-10-13 单变量实测中返回 200 的真实客户端 id：
// hex 段 ecb74175effe 反解出的铸造时刻为 2026-10-13T09:15:46Z，低 12 位为 ffe。
//
// 注意：门禁判据自 2026-10-14 起还校验高 36 位（铸造时刻窗口），因此这个样本会
// 随时间老化。判定它的测试必须用 isOpenCodeGateSessionIDAt 注入参照时刻，不要用
// 墙钟，否则测试会随时间漂移。需要「当前合法」的样本时用
// openCodeGateTestSessionIDMintedAt。
const openCodeGateTestSessionID = "ses_ecb74175effeABCDEFGHIJKLMN"

// openCodeGateRejectedSessionID 是形状合法但 hex 段低位不合法的样例：hex 段
// d1bfaf4aff65 低 12 位为 f65，2026-10-13 实测返回 403 FreeTierError。
// 任何用 sha256 派生 hex 的实现（含本仓库 2026-10-11 之前的算法）都会产出
// 这种值，因此必须被识别为非法并重铸。
const openCodeGateRejectedSessionID = "ses_d1bfaf4aff65ABCDEFGHIJKLMN"

// openCodeGateTestSessionIDMintedAt 用生产铸造函数在指定时刻构造一个门禁合法的
// 会话 id（后缀固定，只让 hex 段随铸造时刻变化），把「形状 + 完整编码」判据的
// 时间窗钉成确定性断言。
func openCodeGateTestSessionIDMintedAt(mintedMillis int64) string {
	return "ses_" + openCodeGateSessionHexPartAt(mintedMillis) + "ABCDEFGHIJKLMN"
}

func TestCanonicalOpenCodeIDShape(t *testing.T) {
	got := canonicalOpenCodeID("ses", "conversation-1")

	require.Len(t, got, 30, "规范 ID 长度必须恒为 30")
	require.True(t, strings.HasPrefix(got, "ses_"), "必须以 ses_ 开头")

	body := strings.TrimPrefix(got, "ses_")
	require.Len(t, body, 26)

	hexPart, suffix := body[:12], body[12:]
	require.Regexp(t, `^[0-9a-f]{12}$`, hexPart, "前 12 位必须是小写十六进制")
	require.Regexp(t, `^[0-9A-Za-z]{14}$`, suffix, "后 14 位必须是字母数字")

	// 必须与上游实际接受的正则一致。
	require.True(t, openCodeCanonicalSessionRe.MatchString(got))
}

func TestCanonicalOpenCodeIDDeterministicAndDistinct(t *testing.T) {
	require.Equal(t,
		canonicalOpenCodeID("ses", "same-source"),
		canonicalOpenCodeID("ses", "same-source"),
		"同一来源必须恒等映射，否则上游 prompt cache 永不命中",
	)
	require.NotEqual(t,
		canonicalOpenCodeID("ses", "source-a"),
		canonicalOpenCodeID("ses", "source-b"),
	)
	require.NotEqual(t,
		canonicalOpenCodeID("ses", "x"),
		canonicalOpenCodeID("msg", "x"),
		"前缀必须参与结果",
	)
}

// TestOpenCodeGateSessionLowHexMatchesValue 钉住字符串常量与数值常量恒等：反解
// 校验按位比较用数值形式，测试与探针用字符串形式，两者漂移会静默改变判据。
func TestOpenCodeGateSessionLowHexMatchesValue(t *testing.T) {
	require.Equal(t, openCodeGateSessionLowHex, fmt.Sprintf("%03x", openCodeGateSessionLowValue))
}

// TestIsOpenCodeGateSessionID 钉住门禁会话 id 的完整判定：形状（正则）与完整
// 官方 tA() 编码（hex 段低 12 位 == ffe，且高 36 位反解出的铸造时刻落在窗口内）。
//
// 时间窗相关用例通过 nowMillis 注入参照时刻；nowMillis == 0 表示用真实墙钟。
// 真实客户端样本按各自反解出的铸造时刻求值（它们会老化，墙钟判定会让测试漂移）。
func TestIsOpenCodeGateSessionID(t *testing.T) {
	const (
		// 四枚真实客户端 id（hex 段来自实测抓包 / 二进制样本），反解出的铸造时刻
		// 见下方 minted* 常量；四者低 12 位均为 ffe。
		officialSampleECB = "ses_ecb74175effeABCDEFGHIJKLMN"
		officialSampleF39 = "ses_f390f32e4ffeABCDEFGHIJKLMN"
		officialSampleFC7 = "ses_fc7b694ecffeABCDEFGHIJKLMN"
		officialSampleFC4 = "ses_fc42c626effeABCDEFGHIJKLMN"

		mintedECB = int64(1791882946721) // 2026-10-13T09:15:46Z
		mintedF39 = int64(1790044065051) // 2026-09-22T02:27:45Z
		mintedFC7 = int64(1787650730771) // 2026-08-25T09:38:50Z
		mintedFC4 = int64(1787710119313) // 2026-08-26T02:08:39Z
	)
	now := time.Now().UnixMilli()

	tests := []struct {
		name      string
		id        string
		nowMillis int64 // 0 表示用真实墙钟
		want      bool
	}{
		// ── 接受：形状 + 完整官方编码 ─────────────────────────────────
		{"实测返回 200 的真实客户端 id（按铸造时刻求值）", officialSampleECB, mintedECB, true},
		{"同上，铸造后约 28 小时被复用（上游实测仍 200，窗口须容纳）", officialSampleECB, mintedECB + 28*60*60*1000, true},
		{"真实客户端样本 f390f32e4ffe（按铸造时刻求值）", officialSampleF39, mintedF39, true},
		{"真实客户端样本 fc7b694ecffe（按铸造时刻求值）", officialSampleFC7, mintedFC7, true},
		{"真实客户端样本 fc42c626effe（按铸造时刻求值）", officialSampleFC4, mintedFC4, true},
		{"生产铸造函数产出的当前值", openCodeGateTestSessionIDMintedAt(now), now, true},
		{"窗口内：47 小时前铸造", openCodeGateTestSessionIDMintedAt(now - 47*60*60*1000), now, true},
		{"窗口内：30 分钟后的未来偏差", openCodeGateTestSessionIDMintedAt(now + 30*60*1000), now, true},
		{"反解时刻恰为参照时刻", openCodeGateTestSessionIDMintedAt(now), now, true},

		// ── 拒绝：形状合法但高 36 位反解不出合法铸造时刻 ────────────────
		{"窗口外：49 小时前铸造", openCodeGateTestSessionIDMintedAt(now - 49*60*60*1000), now, false},
		{"窗口外：2 小时后的未来偏差", openCodeGateTestSessionIDMintedAt(now + 2*60*60*1000), now, false},
		{"随机 hex 只把末 3 位凑成 ffe（2026-10-14 实测 403）", "ses_012345678ffeABCDEFGHIJKLMN", now, false},
		{"hex 段 000000000ffe：反解时刻 = epoch-1，远在未来", "ses_000000000ffeABCDEFGHIJKLMN", now, false},
		{"hex 段 fffffffffffe：反解时刻为 2026-08-13，已过期", "ses_fffffffffffeABCDEFGHIJKLMN", now, false},
		{"实测返回 403 的真实客户端 id（末 3 位 f65）", openCodeGateRejectedSessionID, now, false},
		{"末 3 位 fff", "ses_000000000fffABCDEFGHIJKLMN", now, false},
		{"末 3 位全 0", "ses_000000000000ABCDEFGHIJKLMN", now, false},
		{"哈希派生值（旧算法输出，末 3 位 9ab）", "ses_0123456789abABCDEFGHIJKLMN", now, false},

		// ── 拒绝：形状非法 ───────────────────────────────────────────
		{"UUID", "3f2504e0-4f89-11d3-9a0c-0305e82c3301", now, false},
		{"短串", "abc", now, false},
		{"空串", "", now, false},
		{"缺 ses_ 前缀", "msg_ecb74175effeABCDEFGHIJKLMN", now, false},
		{"hex 段含大写", "ses_ECB74175EFFEABCDEFGHIJKLMN", now, false},
		{"后缀含非字母数字", "ses_ecb74175effeABCDEFGHIJKLM!", now, false},
		{"总长不足", "ses_ecb74175effeABCDEFGHIJKLM", now, false},
		{"总长超出", "ses_ecb74175effeABCDEFGHIJKLMNO", now, false},
		{"前后有空白（不 trim，由调用方负责）", " ses_ecb74175effeABCDEFGHIJKLMN", now, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reference := tt.nowMillis
			if reference == 0 {
				reference = time.Now().UnixMilli()
			}
			require.Equal(t, tt.want, isOpenCodeGateSessionIDAt(tt.id, reference),
				"isOpenCodeGateSessionIDAt(%q, %d)", tt.id, reference)
		})
	}
}

// TestIsOpenCodeGateSessionIDRealSamplesAgeOut 说明真实样本在墙钟下的行为：样本
// 铸造时刻距今超过窗口（48 小时）后会被判为需要重铸。这不是回归——重铸只会损失
// 一次 prompt cache 命中，绝不会产生 403；把这条行为钉住，避免将来有人误以为
// 「真实 id 被拒」是 bug 而把窗口放大到失去筛选力。
func TestIsOpenCodeGateSessionIDRealSamplesAgeOut(t *testing.T) {
	const mintedECB = int64(1791882946721) // 2026-10-13T09:15:46Z

	require.True(t, isOpenCodeGateSessionIDAt(openCodeGateTestSessionID, mintedECB),
		"按铸造时刻求值必须合法")
	require.False(t,
		isOpenCodeGateSessionIDAt(openCodeGateTestSessionID, mintedECB+49*60*60*1000),
		"铸造后 49 小时（超出 48 小时窗口）必须判为需重铸")
}

// assertOpenCodeGateSessionIDValid 断言 id 同时满足形状与位不变量，并用官方算法
// 自洽校验编码里含的时间戳：(hex >> 12) + mintedAtMillis == epoch - 1。
func assertOpenCodeGateSessionIDValid(t *testing.T, id string) {
	t.Helper()
	require.Regexp(t, `^ses_[0-9a-f]{12}[0-9A-Za-z]{14}$`, id, "形状必须合法")
	require.True(t, isOpenCodeGateSessionID(id), "位不变量必须满足，实际 %q", id)
	require.Equal(t, openCodeGateSessionLowHex, id[13:16], "hex 段低 12 位必须为 0xffe")

	hexValue, err := strconv.ParseUint(id[4:16], 16, 64)
	require.NoError(t, err)
	// 精确恒等式为 (hex>>12) + mintedAt == epoch-1；测试与铸造之间存在毫秒级
	// 时间差，故允许 2 秒容差。
	delta := (openCodeGateSessionEpochMillis - 1) - (int64(hexValue>>12) + time.Now().UnixMilli())
	require.LessOrEqual(t, delta, int64(2000), "编码时间戳必须落在当前时刻附近")
	require.GreaterOrEqual(t, delta, int64(-2000), "编码时间戳必须落在当前时刻附近")
}

// officialOpenCodeSessionHexForTest 逐字复刻从 opencode 1.19.0 二进制
// （偏移 105055473）挖出的官方编码 tA()，仅用于交叉验证生产实现逐位一致：
//
//	$ = now*0x1000n + counter
//	U = descending ? ~$ : $
//	hex = U 的低 48 位
//
// 生产实现对应 descending=true、counter=1（低 12 位为 0xFFE 的唯一形态）。
func officialOpenCodeSessionHexForTest(nowMillis, counter int64) string {
	dollar := uint64(nowMillis)*0x1000 + uint64(counter)
	value := (^dollar) & 0xFFFFFFFFFFFF
	return fmt.Sprintf("%012x", value)
}

// TestOpenCodeGateSessionHexMatchesOfficialEncoding 证明生产实现的 hex 段与官方
// 客户端算法**逐位相同**（counter == 1 时），而不仅是「低 12 位凑巧对」。
// 覆盖整个 27<<36 窗口内外，包含窗口边界与 2028 年之后的时钟。
func TestOpenCodeGateSessionHexMatchesOfficialEncoding(t *testing.T) {
	windowStart := int64(26) << 36
	windowEnd := int64(27) << 36
	cases := []int64{
		0, 1, 1000,
		windowStart - 1, windowStart, windowStart + 1,
		windowEnd - 1, windowEnd, windowEnd + 1,
		1791882946721, // 实测返回 200 的真实客户端 id 的铸造时刻
		1791881638000, // 实测返回 403 的那次请求时刻
	}
	// 窗口内均匀取样。
	step := (windowEnd - windowStart) / 64
	for now := windowStart; now < windowEnd; now += step {
		cases = append(cases, now)
	}
	// 伪随机取样（固定种子，避免抖动）。
	span := uint64(windowEnd - windowStart)
	seed := uint64(0x9E3779B97F4A7C15)
	for i := 0; i < 512; i++ {
		seed = seed*6364136223846793005 + 1442695040888963407
		cases = append(cases, windowStart+int64(seed%span))
	}

	for _, now := range cases {
		got := openCodeGateSessionHexPartAt(now)
		want := officialOpenCodeSessionHexForTest(now, 1)
		require.Equal(t, want, got, "now=%d 时生产实现必须与官方编码逐位一致", now)
		require.Equal(t, openCodeGateSessionLowHex, got[9:12], "now=%d 低 12 位必须为 0xffe", now)
	}
}

// TestOpenCodeGateSessionHexCounterOneIsGateForm 说明「低 12 位 == 0xFFE」为什么
// 等价于「官方客户端的 counter == 1」：counter 每递增 1，低 12 位就减 1。
func TestOpenCodeGateSessionHexCounterOneIsGateForm(t *testing.T) {
	const now = int64(1791882946721)
	require.Equal(t, openCodeGateSessionLowHex, officialOpenCodeSessionHexForTest(now, 1)[9:12],
		"counter=1 的低 12 位必须是 0xffe")
	require.Equal(t, "ffd", officialOpenCodeSessionHexForTest(now, 2)[9:12],
		"counter=2 的低 12 位必须是 0xffd")
	require.Equal(t, "fff", officialOpenCodeSessionHexForTest(now, 4096)[9:12],
		"counter 溢出 4096 后低 12 位回到 0xfff")
	require.Equal(t, openCodeGateSessionHexPartAt(now), officialOpenCodeSessionHexForTest(now, 1))
}

// TestMintOpenCodeSessionIDInvariant 钉住**每一条铸造路径**的产出都满足形状 +
// 位不变量。任何一条路径漏掉不变量都会让对应请求稳定 403，因此逐路径覆盖。
func TestMintOpenCodeSessionIDInvariant(t *testing.T) {
	t.Run("随机后缀铸造（最后兜底路径）", func(t *testing.T) {
		seen := make(map[string]bool, 64)
		for i := 0; i < 64; i++ {
			id := mintOpenCodeSessionID()
			assertOpenCodeGateSessionIDValid(t, id)
			require.False(t, seen[id], "随机后缀不应碰撞：%q", id)
			seen[id] = true
		}
	})

	t.Run("按来源铸造（缓存路径）", func(t *testing.T) {
		for i := 0; i < 64; i++ {
			assertOpenCodeGateSessionIDValid(t, mintOpenCodeSessionIDForSource("mint-source-"+strconv.Itoa(i)))
		}
	})

	t.Run("规范会话改写路径", func(t *testing.T) {
		for i := 0; i < 64; i++ {
			assertOpenCodeGateSessionIDValid(t, canonicalOpenCodeSessionID("canonical-source-"+strconv.Itoa(i)))
		}
	})

	t.Run("门禁端到端路径（含 affinity 镜像）", func(t *testing.T) {
		account := openCodeGateTestAccount(nil)
		for i := 0; i < 32; i++ {
			headers := http.Header{}
			headers.Set(openCodeSessionHeader, "e2e-uuid-"+strconv.Itoa(i))
			body := []byte(`{"model":"mimo-v2.6-flash-free","messages":[{"role":"user","content":"hi ` + strconv.Itoa(i) + `"}]}`)

			applyOpenCodeFreeTierGateSession(account, headers, body)

			session := headers.Get(openCodeSessionHeader)
			assertOpenCodeGateSessionIDValid(t, session)
			require.Equal(t, session, headers.Get(openCodeSessionAffinityHeader))
			require.Equal(t, session, headers.Get(openCodeSessionIDHeader))
		}
	})
}

// openCodeGateHashShapedSessionID 返回一个由 canonicalOpenCodeID（与上游插件的
// canonicalID 同构，即 sha256 派生 hex）产出、且 hex 段低 12 位不为 ffe 的样本。
//
// 用搜索而不是硬编码字面量：样本永远与算法保持一致，算法变了测试仍然成立。
// 命中概率为 4095/4096，256 个来源里必然有一个，且选中结果是算法的纯函数，
// 因此不存在抖动。
func openCodeGateHashShapedSessionID(t *testing.T) string {
	t.Helper()
	for i := 0; i < 256; i++ {
		id := canonicalOpenCodeID("ses", "plugin-supplied-"+strconv.Itoa(i))
		if openCodeCanonicalSessionRe.MatchString(id) && !isOpenCodeGateSessionID(id) {
			return id
		}
	}
	t.Fatal("256 个哈希派生样本里没有位不合法的值（正则或算法已变，需复核）")
	return ""
}

// openCodeGateHashShapedFFESessionID 返回一个由 canonicalOpenCodeID（sha256 派生
// hex，与上游插件的 canonicalID 同构）产出、hex 段低 12 位**恰好**为 ffe、且高
// 36 位反解出的铸造时刻落在窗口外的样本。
//
// 这正是 2026-10-14 收紧要堵的洞：只查低 12 位的旧判据会把它当合法值原样透传，
// 而上游实测对这类值返回 403 FreeTierError（封 key 约 15 分钟）。
//
// 命中概率 1/4096，因此用搜索而不是硬编码字面量：样本永远与算法保持一致。
// 来源固定 → 结果是纯函数，不存在抖动。
func openCodeGateHashShapedFFESessionID(t *testing.T) string {
	t.Helper()
	for i := 0; i < 1<<18; i++ {
		id := canonicalOpenCodeID("ses", "plugin-supplied-ffe-"+strconv.Itoa(i))
		if !openCodeCanonicalSessionRe.MatchString(id) || id[13:16] != openCodeGateSessionLowHex {
			continue
		}
		// 若该样本反解出的铸造时刻碰巧落在窗口内（概率约 6e-7），换下一个，
		// 保证断言的是「低 12 位为 ffe 但高 36 位非法」这一形态。
		if isOpenCodeGateSessionID(id) {
			continue
		}
		return id
	}
	t.Fatal("1<<18 个哈希派生样本里没有找到「低 12 位为 ffe 但高 36 位非法」的值（正则或算法已变，需复核）")
	return ""
}

// TestCanonicalOpenCodeSessionIDRekeysHashShapedValues 钉住本次修复的核心行为：
// 形状合法但位不合法的 ses_ 值（任何哈希派生实现的产物，含上游插件）必须被
// 重铸，绝不能原样透传——否则上游的 403 FreeTierError 会被带进来。
func TestCanonicalOpenCodeSessionIDRekeysHashShapedValues(t *testing.T) {
	hashShaped := openCodeGateHashShapedSessionID(t)
	require.True(t, openCodeCanonicalSessionRe.MatchString(hashShaped), "前提：形状合法")
	require.False(t, isOpenCodeGateSessionID(hashShaped), "前提：位不合法")

	got := canonicalOpenCodeSessionID(hashShaped)
	require.NotEqual(t, hashShaped, got, "哈希型客户端会话值必须被重铸，不得原样透传")
	require.True(t, isOpenCodeGateSessionID(got), "重铸结果必须满足门禁，实际 %q", got)

	// 重铸仍须确定：同一来源跨轮次稳定，否则上游 prompt cache 击穿。
	require.Equal(t, got, canonicalOpenCodeSessionID(hashShaped))

	// 形状合法且编码完整（含时间窗）时必须原样保留（不破坏调用方自建的会话
	// 连续性）。用生产铸造函数产出的当前值，避免依赖会老化的历史样本。
	official := openCodeGateTestSessionIDMintedAt(time.Now().UnixMilli())
	require.Equal(t, official, canonicalOpenCodeSessionID(official))
	// 非 ses_ 形状同样要重铸。
	require.True(t, isOpenCodeGateSessionID(canonicalOpenCodeSessionID("3f2504e0-4f89-11d3-9a0c-0305e82c3301")))
	// 空来源不铸造（由调用方决定是否兜底）。
	require.Empty(t, canonicalOpenCodeSessionID("   "))
}

// TestCanonicalOpenCodeSessionIDRekeysHashShapedFFEValues 钉住 2026-10-14 收紧
// 要堵的那个洞：低 12 位**恰好**为 ffe、但高 36 位是哈希噪声的客户端值必须被
// 重铸。只查低 12 位的旧判据会原样透传它，而上游实测对这类值返回
// 403 FreeTierError（会封 key 约 15 分钟）。
func TestCanonicalOpenCodeSessionIDRekeysHashShapedFFEValues(t *testing.T) {
	ffeShaped := openCodeGateHashShapedFFESessionID(t)
	require.True(t, openCodeCanonicalSessionRe.MatchString(ffeShaped), "前提：形状合法")
	require.Equal(t, openCodeGateSessionLowHex, ffeShaped[13:16],
		"前提：低 12 位恰为 ffe（旧判据会放行）")
	require.False(t, isOpenCodeGateSessionID(ffeShaped),
		"前提：高 36 位反解出的铸造时刻必须落在窗口外")

	got := canonicalOpenCodeSessionID(ffeShaped)
	require.NotEqual(t, ffeShaped, got, "低 12 位凑成 ffe 的哈希值必须被重铸，不得原样透传")
	require.True(t, isOpenCodeGateSessionID(got), "重铸结果必须满足门禁，实际 %q", got)

	// 重铸仍须确定：同一来源跨轮次稳定，否则上游 prompt cache 击穿。
	require.Equal(t, got, canonicalOpenCodeSessionID(ffeShaped))
}

// TestOpenCodeGateSessionMintCacheStability 钉住缓存语义：同一 source 恒等、
// 不同 source 不同、每条产出都过门禁，且命中缓存时不重复铸造。
func TestOpenCodeGateSessionMintCacheStability(t *testing.T) {
	cache := newOpenCodeGateSessionMintCache(openCodeGateSessionMintCacheSize)
	calls := 0
	mint := func() string {
		calls++
		return mintOpenCodeSessionID()
	}

	first := cache.getOrMint("conversation-a", mint)
	require.Equal(t, 1, calls)
	require.Equal(t, first, cache.getOrMint("conversation-a", mint), "同一 source 必须返回同一 id")
	require.Equal(t, 1, calls, "命中缓存时不得重复铸造")

	second := cache.getOrMint("conversation-b", mint)
	require.NotEqual(t, first, second, "不同 source 必须得到不同 id")
	require.Equal(t, 2, calls)

	assertOpenCodeGateSessionIDValid(t, first)
	assertOpenCodeGateSessionIDValid(t, second)
}

// TestOpenCodeGateSessionMintCacheEviction 钉住 FIFO 淘汰：容量满后淘汰最早的
// 条目，被淘汰的 source 下一轮重铸——仍满足门禁，只是损失一次 prompt cache 命中。
func TestOpenCodeGateSessionMintCacheEviction(t *testing.T) {
	cache := newOpenCodeGateSessionMintCache(2)

	a1 := cache.getOrMint("a", mintOpenCodeSessionID)
	_ = cache.getOrMint("b", mintOpenCodeSessionID)
	require.Len(t, cache.ids, 2)

	// 容量已满，插入 c 必须淘汰最早的 a。
	_ = cache.getOrMint("c", mintOpenCodeSessionID)
	require.Len(t, cache.ids, 2, "缓存必须有界")
	require.NotContains(t, cache.ids, "a", "FIFO 应淘汰最早的条目")

	a2 := cache.getOrMint("a", mintOpenCodeSessionID)
	require.NotEqual(t, a1, a2, "被淘汰的 source 必须重铸")
	assertOpenCodeGateSessionIDValid(t, a2)
	assertOpenCodeGateSessionIDValid(t, cache.getOrMint("b", mintOpenCodeSessionID))
	assertOpenCodeGateSessionIDValid(t, cache.getOrMint("c", mintOpenCodeSessionID))
}

// TestOpenCodeGateSessionMintCacheConcurrent 钉住并发安全：同一 source 的并发
// 调用必须拿到同一个 id（否则 prompt cache 会因并发抖动而击穿）。
func TestOpenCodeGateSessionMintCacheConcurrent(t *testing.T) {
	cache := newOpenCodeGateSessionMintCache(openCodeGateSessionMintCacheSize)

	const workers = 16
	results := make([]string, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			results[idx] = cache.getOrMint("shared-conversation", mintOpenCodeSessionID)
		}(i)
	}
	wg.Wait()

	for i := 1; i < workers; i++ {
		require.Equal(t, results[0], results[i], "并发下同一 source 必须只铸造一次")
	}
	assertOpenCodeGateSessionIDValid(t, results[0])
}

func TestIsOpenCodeFreeTierModel(t *testing.T) {
	tests := []struct {
		model string
		want  bool
	}{
		{"mimo-v2.6-flash-free", true},
		{"deepseek-v4-flash-free", true},
		{"muse-spark-1.3-contributor-free", true},
		{"ling-3.0-flash-fin-free", true},
		{"nemotron-3-ultra-free", true},
		{"MIMO-V2.6-FLASH-FREE", true},
		{"opencode/mimo-v2.6-flash-free", true},
		{"opencode-go/mimo-v2.6-flash-free", true},
		{"  mimo-v2.6-flash-free  ", true},
		// 付费模型，含与免费模型同前缀者，绝不能误判。
		{"deepseek-v4-flash", false},
		{"mimo-v2.5", false},
		{"muse-spark-1.3-contributor", false},
		{"glm-5.3", false},
		{"", false},
		{"free", false},
	}
	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			require.Equal(t, tt.want, isOpenCodeFreeTierModel(tt.model))
		})
	}
}

func TestShouldApplyOpenCodeFreeTierGate(t *testing.T) {
	tests := []struct {
		name    string
		account *Account
		model   string
		want    bool
	}{
		{
			name:    "缺省 auto 对免费模型启用",
			account: openCodeGateTestAccount(nil),
			model:   "mimo-v2.6-flash-free",
			want:    true,
		},
		{
			name:    "缺省 auto 对付费模型不启用",
			account: openCodeGateTestAccount(nil),
			model:   "deepseek-v4-flash",
			want:    false,
		},
		{
			name:    "auto 显式配置",
			account: openCodeGateTestAccount(map[string]any{"free_tier_gate": "auto"}),
			model:   "mimo-v2.6-flash-free",
			want:    true,
		},
		{
			name:    "always 对付费模型也启用",
			account: openCodeGateTestAccount(map[string]any{"free_tier_gate": "always"}),
			model:   "glm-5.3",
			want:    true,
		},
		{
			name:    "off 对免费模型也关闭",
			account: openCodeGateTestAccount(map[string]any{"free_tier_gate": "off"}),
			model:   "mimo-v2.6-flash-free",
			want:    false,
		},
		{
			name:    "非法取值回落 auto",
			account: openCodeGateTestAccount(map[string]any{"free_tier_gate": "sometimes"}),
			model:   "mimo-v2.6-flash-free",
			want:    true,
		},
		{
			name:    "非 OpenCode 平台不启用",
			account: &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey},
			model:   "mimo-v2.6-flash-free",
			want:    false,
		},
		{
			name:    "空账号不启用",
			account: nil,
			model:   "mimo-v2.6-flash-free",
			want:    false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, shouldApplyOpenCodeFreeTierGate(tt.account, tt.model))
		})
	}
}

// requireGateTools 断言 tools 中同时存在 bash 与 read，且各自只出现一次。
func requireGateTools(t *testing.T, body []byte, namePath string) {
	t.Helper()
	names := map[string]int{}
	for _, item := range gjson.GetBytes(body, "tools").Array() {
		if name := strings.TrimSpace(item.Get(namePath).String()); name != "" {
			names[name]++
		}
	}
	require.Equal(t, 1, names["bash"], "bash 必须恰好存在一次")
	require.Equal(t, 1, names["read"], "read 必须恰好存在一次")
}

func TestApplyOpenCodeFreeTierGateBodyChatDialect(t *testing.T) {
	account := openCodeGateTestAccount(nil)
	body := []byte(`{"model":"mimo-v2.6-flash-free","stream":false,"messages":[{"role":"user","content":"hi"}]}`)

	out, err := applyOpenCodeFreeTierGateBody(account, body, openCodeGateDialectChat)
	require.NoError(t, err)

	require.True(t, gjson.GetBytes(out, "stream").Bool(), "stream 必须被强制为 true")
	requireGateTools(t, out, "function.name")

	// chat 方言必须是 function 嵌套形状。
	first := gjson.GetBytes(out, "tools.0")
	require.Equal(t, "function", first.Get("type").String())
	require.True(t, first.Get("function.parameters").Exists())

	// 原有 messages 不得被改动。
	require.Equal(t, "hi", gjson.GetBytes(out, "messages.0.content").String())
}

func TestApplyOpenCodeFreeTierGateBodyResponsesDialect(t *testing.T) {
	account := openCodeGateTestAccount(nil)
	body := []byte(`{"model":"muse-spark-1.3-contributor-free","stream":true,"input":[{"role":"user","content":"hi"}]}`)

	out, err := applyOpenCodeFreeTierGateBody(account, body, openCodeGateDialectResponses)
	require.NoError(t, err)

	requireGateTools(t, out, "name")
	// responses 方言是扁平形状，不得出现 function 包装。
	first := gjson.GetBytes(out, "tools.0")
	require.Equal(t, "function", first.Get("type").String())
	require.False(t, first.Get("function").Exists(), "responses 方言不得使用 function 包装")
	require.True(t, first.Get("parameters").Exists())
}

func TestApplyOpenCodeFreeTierGateBodyAnthropicDialect(t *testing.T) {
	account := openCodeGateTestAccount(nil)
	body := []byte(`{"model":"mimo-v2.6-flash-free","stream":true,"max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`)

	out, err := applyOpenCodeFreeTierGateBody(account, body, openCodeGateDialectAnthropic)
	require.NoError(t, err)

	requireGateTools(t, out, "name")
	// Anthropic 自定义工具参数键为 input_schema，无 type 包装。
	first := gjson.GetBytes(out, "tools.0")
	require.True(t, first.Get("input_schema").Exists(), "Anthropic 方言必须使用 input_schema")
	require.False(t, first.Get("parameters").Exists())
	require.False(t, first.Get("type").Exists())
}

func TestApplyOpenCodeFreeTierGateBodyOnlyFillsMissingTool(t *testing.T) {
	account := openCodeGateTestAccount(nil)
	body := []byte(`{"model":"mimo-v2.6-flash-free","stream":true,"messages":[],"tools":[{"type":"function","function":{"name":"bash","description":"mine","parameters":{"type":"object","properties":{"cmd":{"type":"string"}}}}}]}`)

	out, err := applyOpenCodeFreeTierGateBody(account, body, openCodeGateDialectChat)
	require.NoError(t, err)

	requireGateTools(t, out, "function.name")
	// 已有的 bash 定义必须原样保留，不能被覆盖成官方 schema。
	require.Equal(t, "mine", gjson.GetBytes(out, "tools.0.function.description").String())
	require.True(t, gjson.GetBytes(out, "tools.0.function.parameters.properties.cmd").Exists())
	// 客户端已带工具时只补门禁硬性要求的 bash+read，这里只缺 read。
	require.Equal(t, 2, len(gjson.GetBytes(out, "tools").Array()))
}

// TestOpenCodeGateInjectsOnlyRequiredToolsWhenClientHasTools 钉住 C 方案的核心行为：
// 客户端自带工具时只补门禁硬性要求的 bash+read，绝不把客户端不认识、模型却可能
// 调用的其余官方工具塞进请求（否则会被客户端以 unsupported call 拒绝）。
func TestOpenCodeGateInjectsOnlyRequiredToolsWhenClientHasTools(t *testing.T) {
	account := openCodeGateTestAccount(nil)
	// Codex 风格工具名：客户端不认识 bash/read，但门禁要求它们存在。
	body := []byte(`{"model":"muse-spark-1.3-contributor-free","stream":true,"input":[{"role":"user","content":"hi"}],"tools":[{"type":"function","name":"shell","parameters":{"type":"object"}},{"type":"function","name":"read_file","parameters":{"type":"object"}},{"type":"function","name":"apply_patch","parameters":{"type":"object"}}]}`)

	out, err := applyOpenCodeFreeTierGateBody(account, body, openCodeGateDialectResponses)
	require.NoError(t, err)

	names := []string{}
	for _, item := range gjson.GetBytes(out, "tools").Array() {
		names = append(names, item.Get("name").String())
	}
	// 客户端 3 个 + 只补 bash、read。
	require.Equal(t, []string{"apply_patch", "bash", "read", "read_file", "shell"}, names,
		"客户端已带工具时不得注入 bash/read 之外的官方工具")
	// 客户端已带工具，不得补 tool_choice。
	require.False(t, gjson.GetBytes(out, "tool_choice").Exists())

	// 补入的 bash/read 必须带劝阻描述：客户端不认识它们，模型一调就被
	// "unsupported call" 拒绝，还会沿用它们的参数形状把后续调用带偏。
	for _, item := range gjson.GetBytes(out, "tools").Array() {
		switch item.Get("name").String() {
		case "bash", "read":
			require.Contains(t, item.Get("description").String(), "DO NOT CALL",
				"补入的 %s 必须带劝阻描述", item.Get("name").String())
		case "shell", "read_file", "apply_patch":
			require.NotContains(t, item.Get("description").String(), "DO NOT CALL",
				"客户端自带工具的描述不得被改动")
		}
	}
}

func TestApplyOpenCodeFreeTierGateBodyKeepsClientTools(t *testing.T) {
	account := openCodeGateTestAccount(nil)
	body := []byte(`{"model":"mimo-v2.6-flash-free","stream":true,"messages":[],"tools":[{"type":"function","function":{"name":"bash","parameters":{"type":"object"}}},{"type":"function","function":{"name":"read","parameters":{"type":"object"}}},{"type":"function","function":{"name":"my_tool","parameters":{"type":"object"}}}]}`)

	out, err := applyOpenCodeFreeTierGateBody(account, body, openCodeGateDialectChat)
	require.NoError(t, err)

	requireGateTools(t, out, "function.name")
	// 客户端已同时提供 bash 与 read，无需补齐任何工具。
	require.Equal(t, 3, len(gjson.GetBytes(out, "tools").Array()), "不得重复注入或丢弃客户端工具")
	names := map[string]bool{}
	for _, item := range gjson.GetBytes(out, "tools").Array() {
		names[item.Get("function.name").String()] = true
	}
	require.True(t, names["my_tool"], "客户端自定义工具不得丢失")
}

// TestOpenCodeGateToolsAreSortedByName 钉住与官方客户端一致的字母序。
func TestOpenCodeGateToolsAreSortedByName(t *testing.T) {
	account := openCodeGateTestAccount(nil)
	// 客户端故意用倒序提供，验证最终仍按字母序排列。
	body := []byte(`{"model":"mimo-v2.6-flash-free","stream":true,"messages":[],"tools":[{"type":"function","function":{"name":"write","parameters":{"type":"object"}}},{"type":"function","function":{"name":"bash","parameters":{"type":"object"}}}]}`)

	out, err := applyOpenCodeFreeTierGateBody(account, body, openCodeGateDialectChat)
	require.NoError(t, err)

	names := make([]string, 0, 3)
	for _, item := range gjson.GetBytes(out, "tools").Array() {
		names = append(names, item.Get("function.name").String())
	}
	// 客户端 2 个 + 只补 read。
	require.Equal(t, []string{"bash", "read", "write"}, names)
}

// TestOpenCodeGateToolChoiceNoneForToolLessRequests 钉住无工具请求的保护：
// 补齐官方工具集的同时必须禁止工具调用，否则模型会调用客户端无法执行的工具。
func TestOpenCodeGateToolChoiceNoneForToolLessRequests(t *testing.T) {
	account := openCodeGateTestAccount(nil)

	t.Run("原本无工具时补 tool_choice none", func(t *testing.T) {
		body := []byte(`{"model":"mimo-v2.6-flash-free","stream":true,"messages":[{"role":"user","content":"summarize"}]}`)

		out, err := applyOpenCodeFreeTierGateBody(account, body, openCodeGateDialectChat)
		require.NoError(t, err)

		require.Equal(t, "none", gjson.GetBytes(out, "tool_choice").String())
		require.Equal(t, 6, len(gjson.GetBytes(out, "tools").Array()))
	})

	t.Run("原本有工具时不改 tool_choice", func(t *testing.T) {
		body := []byte(`{"model":"mimo-v2.6-flash-free","stream":true,"messages":[],"tool_choice":"auto","tools":[{"type":"function","function":{"name":"bash","parameters":{"type":"object"}}}]}`)

		out, err := applyOpenCodeFreeTierGateBody(account, body, openCodeGateDialectChat)
		require.NoError(t, err)

		require.Equal(t, "auto", gjson.GetBytes(out, "tool_choice").String(),
			"客户端显式声明的 tool_choice 不得被覆盖")
	})

	// 回归：OpenCode 的 /responses 只接受 tool_choice:"auto"，发送 "none" 会被
	// 上游 400 拒绝（生产环境稳定复现）。因此非 chat 方言不得注入 tool_choice。
	t.Run("Responses 方言不注入 tool_choice", func(t *testing.T) {
		body := []byte(`{"model":"muse-spark-1.3-contributor-free","stream":true,"input":[{"role":"user","content":"hi"}]}`)

		out, err := applyOpenCodeFreeTierGateBody(account, body, openCodeGateDialectResponses)
		require.NoError(t, err)

		require.False(t, gjson.GetBytes(out, "tool_choice").Exists(),
			"OpenCode /responses 只接受 tool_choice:auto，不得注入 none")
		// 无工具请求仍须补齐门禁硬性要求，否则 403。
		requireGateTools(t, out, "name")
	})

	t.Run("Anthropic 方言不注入 tool_choice", func(t *testing.T) {
		body := []byte(`{"model":"mimo-v2.6-flash-free","stream":true,"max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`)

		out, err := applyOpenCodeFreeTierGateBody(account, body, openCodeGateDialectAnthropic)
		require.NoError(t, err)

		require.False(t, gjson.GetBytes(out, "tool_choice").Exists(),
			"非 chat 方言不注入 tool_choice")
		requireGateTools(t, out, "name")
	})
}

// TestApplyOpenCodeFreeTierGateSessionMirrorsAffinityHeaders 钉住与官方客户端一致的
// affinity 头镜像。门禁本身不校验它们（实测只发 X-Opencode-Session 仍 200），
// 补齐是为了让出站流量形态一致。
func TestApplyOpenCodeFreeTierGateSessionMirrorsAffinityHeaders(t *testing.T) {
	account := openCodeGateTestAccount(nil)
	headers := http.Header{}
	headers.Set(openCodeSessionHeader, "3f2504e0-4f89-11d3-9a0c-0305e82c3301")
	body := []byte(`{"model":"mimo-v2.6-flash-free"}`)

	applyOpenCodeFreeTierGateSession(account, headers, body)

	session := headers.Get(openCodeSessionHeader)
	require.True(t, isOpenCodeGateSessionID(session), "镜像的会话值必须满足形状与位不变量，实际 %q", session)
	require.Equal(t, session, headers.Get(openCodeSessionAffinityHeader))
	require.Equal(t, session, headers.Get(openCodeSessionIDHeader))
}

func TestApplyOpenCodeFreeTierGateBodyPaidModelUntouched(t *testing.T) {
	account := openCodeGateTestAccount(nil)
	original := `{"model":"deepseek-v4-flash","stream":false,"messages":[]}`

	out, err := applyOpenCodeFreeTierGateBody(account, []byte(original), openCodeGateDialectChat)
	require.NoError(t, err)
	require.Equal(t, original, string(out), "auto 模式下付费模型的出站 body 必须逐字节不变")
}

func TestApplyOpenCodeFreeTierGateBodyOffModeUntouched(t *testing.T) {
	account := openCodeGateTestAccount(map[string]any{"free_tier_gate": "off"})
	original := `{"model":"mimo-v2.6-flash-free","stream":false,"messages":[]}`

	out, err := applyOpenCodeFreeTierGateBody(account, []byte(original), openCodeGateDialectChat)
	require.NoError(t, err)
	require.Equal(t, original, string(out))
}

func TestApplyOpenCodeFreeTierGateBodyMalformedIsPassthrough(t *testing.T) {
	account := openCodeGateTestAccount(nil)

	for _, tt := range []struct {
		name string
		body string
	}{
		{"非 JSON", `not json at all`},
		{"截断 JSON", `{"model":"mimo-v2.6-flash-free","stream":`},
		{"空 body", ``},
	} {
		t.Run(tt.name, func(t *testing.T) {
			out, err := applyOpenCodeFreeTierGateBody(account, []byte(tt.body), openCodeGateDialectChat)
			require.NoError(t, err, "畸形 body 不得让请求在网关侧失败")
			require.Equal(t, tt.body, string(out))
		})
	}
}

func TestApplyOpenCodeFreeTierGateBodyToolsNotArrayIsPassthrough(t *testing.T) {
	account := openCodeGateTestAccount(nil)
	body := []byte(`{"model":"mimo-v2.6-flash-free","stream":true,"messages":[],"tools":"weird"}`)

	out, err := applyOpenCodeFreeTierGateBody(account, body, openCodeGateDialectChat)
	require.NoError(t, err)
	// stream 仍被强制，但 tools 形状未知时不得被改写。
	require.True(t, gjson.GetBytes(out, "stream").Bool())
	require.Equal(t, "weird", gjson.GetBytes(out, "tools").String())
}

func TestApplyOpenCodeFreeTierGateSession(t *testing.T) {
	// 用生产铸造函数产出的当前值，避免依赖会老化的历史样本（判据含时间窗）。
	canonical := openCodeGateTestSessionIDMintedAt(time.Now().UnixMilli())

	t.Run("客户端已提供合法值时原样保留", func(t *testing.T) {
		account := openCodeGateTestAccount(nil)
		headers := http.Header{}
		headers.Set(openCodeSessionHeader, canonical)
		body := []byte(`{"model":"mimo-v2.6-flash-free"}`)

		applyOpenCodeFreeTierGateSession(account, headers, body)

		require.Equal(t, canonical, headers.Get(openCodeSessionHeader))
	})

	// 本次修复的核心：形状合法但位不合法（哈希派生的 ses_）不得透传。
	t.Run("形状合法但位不合法必须重铸", func(t *testing.T) {
		account := openCodeGateTestAccount(nil)
		headers := http.Header{}
		headers.Set(openCodeSessionHeader, openCodeGateRejectedSessionID)
		body := []byte(`{"model":"mimo-v2.6-flash-free"}`)

		applyOpenCodeFreeTierGateSession(account, headers, body)

		got := headers.Get(openCodeSessionHeader)
		require.NotEqual(t, openCodeGateRejectedSessionID, got, "位不合法（f65）的值不得原样透传")
		require.True(t, isOpenCodeGateSessionID(got), "重铸结果必须满足形状与位不变量，实际 %q", got)
		require.Equal(t, got, headers.Get(openCodeSessionAffinityHeader))
		require.Equal(t, got, headers.Get(openCodeSessionIDHeader))
	})

	// 2026-10-14 收紧堵的洞：低 12 位恰好为 ffe、但高 36 位非法的哈希派生值
	// 不得透传。只查低 12 位的旧判据会把它原样送出，上游实测 403。
	t.Run("低 12 位恰为 ffe 的哈希派生值必须重铸", func(t *testing.T) {
		ffeShaped := openCodeGateHashShapedFFESessionID(t)
		account := openCodeGateTestAccount(nil)
		headers := http.Header{}
		headers.Set(openCodeSessionHeader, ffeShaped)
		body := []byte(`{"model":"mimo-v2.6-flash-free"}`)

		applyOpenCodeFreeTierGateSession(account, headers, body)

		got := headers.Get(openCodeSessionHeader)
		require.NotEqual(t, ffeShaped, got, "只把末 3 位凑成 ffe 的值不得原样透传")
		require.True(t, isOpenCodeGateSessionID(got), "重铸结果必须满足门禁，实际 %q", got)
		require.Equal(t, got, headers.Get(openCodeSessionAffinityHeader))
		require.Equal(t, got, headers.Get(openCodeSessionIDHeader))
	})

	t.Run("UUID 被改写为规范形状", func(t *testing.T) {
		account := openCodeGateTestAccount(nil)
		headers := http.Header{}
		headers.Set(openCodeSessionHeader, "3f2504e0-4f89-11d3-9a0c-0305e82c3301")
		body := []byte(`{"model":"mimo-v2.6-flash-free"}`)

		applyOpenCodeFreeTierGateSession(account, headers, body)

		got := headers.Get(openCodeSessionHeader)
		require.True(t, isOpenCodeGateSessionID(got), "改写结果必须满足门禁，实际 %q", got)
		require.NotEqual(t, "3f2504e0-4f89-11d3-9a0c-0305e82c3301", got)
	})

	t.Run("同一来源跨轮次稳定", func(t *testing.T) {
		account := openCodeGateTestAccount(nil)
		body := []byte(`{"model":"mimo-v2.6-flash-free"}`)

		first := http.Header{}
		first.Set(openCodeSessionHeader, "same-uuid-value")
		applyOpenCodeFreeTierGateSession(account, first, body)

		second := http.Header{}
		second.Set(openCodeSessionHeader, "same-uuid-value")
		applyOpenCodeFreeTierGateSession(account, second, body)

		require.Equal(t, first.Get(openCodeSessionHeader), second.Get(openCodeSessionHeader))
	})

	t.Run("付费模型不改写", func(t *testing.T) {
		account := openCodeGateTestAccount(nil)
		headers := http.Header{}
		headers.Set(openCodeSessionHeader, "3f2504e0-4f89-11d3-9a0c-0305e82c3301")
		body := []byte(`{"model":"deepseek-v4-flash"}`)

		applyOpenCodeFreeTierGateSession(account, headers, body)

		require.Equal(t, "3f2504e0-4f89-11d3-9a0c-0305e82c3301", headers.Get(openCodeSessionHeader))
	})

	t.Run("无会话头时自行补出规范值", func(t *testing.T) {
		// 回归 Zen 缺口：shouldGenerateOpenCodeSession 要求路径含 /zen/go，
		// Zen 账号（/zen/v1）不满足，故 applyOpenCodeSessionHeader 不会补会话头。
		// 门禁必须自行补，否则出站请求无会话头，免费模型必然 403。
		account := openCodeGateTestAccount(nil)
		headers := http.Header{}
		body := []byte(`{"model":"mimo-v2.6-flash-free","messages":[{"role":"user","content":"hi"}]}`)

		applyOpenCodeFreeTierGateSession(account, headers, body)

		got := headers.Get(openCodeSessionHeader)
		require.True(t, isOpenCodeGateSessionID(got), "必须补出门禁合法的会话值，实际 %q", got)
	})

	t.Run("无会话头时按内容派生且跨轮次稳定", func(t *testing.T) {
		account := openCodeGateTestAccount(nil)
		first := http.Header{}
		firstTurn := []byte(`{"model":"mimo-v2.6-flash-free","messages":[{"role":"user","content":"hello"}]}`)
		applyOpenCodeFreeTierGateSession(account, first, firstTurn)

		second := http.Header{}
		// 后续轮次首条用户消息不变，仅追加历史：会话值必须保持一致。
		laterTurn := []byte(`{"model":"mimo-v2.6-flash-free","messages":[{"role":"user","content":"hello"},{"role":"assistant","content":"hi"},{"role":"user","content":"more"}]}`)
		applyOpenCodeFreeTierGateSession(account, second, laterTurn)

		require.Equal(t, first.Get(openCodeSessionHeader), second.Get(openCodeSessionHeader),
			"同一会话跨轮次必须稳定，否则 prompt cache 永不命中")
	})

	t.Run("付费模型无会话头时不补", func(t *testing.T) {
		account := openCodeGateTestAccount(nil)
		headers := http.Header{}
		body := []byte(`{"model":"deepseek-v4-flash","messages":[{"role":"user","content":"hi"}]}`)

		applyOpenCodeFreeTierGateSession(account, headers, body)

		require.Empty(t, headers.Get(openCodeSessionHeader), "auto 模式下付费模型不得被注入会话头")
	})
}

func TestNormalizeOpenCodeFreeTierGateCredentials(t *testing.T) {
	require.NoError(t, NormalizeOpenCodeFreeTierGateCredentials(nil))
	require.NoError(t, NormalizeOpenCodeFreeTierGateCredentials(map[string]any{}))

	// 未携带该字段时必须是 no-op，使旧账号继续按 auto 处理。
	creds := map[string]any{"api_key": "sk-1"}
	require.NoError(t, NormalizeOpenCodeFreeTierGateCredentials(creds))
	require.NotContains(t, creds, "free_tier_gate")

	for _, mode := range []string{"auto", "always", "off", " AUTO ", "Off"} {
		creds := map[string]any{"free_tier_gate": mode}
		require.NoError(t, NormalizeOpenCodeFreeTierGateCredentials(creds), mode)
		normalized, ok := creds["free_tier_gate"].(string)
		require.True(t, ok)
		require.Contains(t, []string{"auto", "always", "off"}, normalized)
	}

	for _, bad := range []any{"sometimes", "", 1, true, []any{"auto"}} {
		creds := map[string]any{"free_tier_gate": bad}
		err := NormalizeOpenCodeFreeTierGateCredentials(creds)
		require.Error(t, err, "非法取值 %v 必须被拒绝", bad)
	}
}

func TestDefaultOpenCodeZenFreeModelIDsRouting(t *testing.T) {
	// 免费层 ID 必须出现在平台目录回退中，否则 /v1/models 与白名单预填看不到它们。
	catalog := DefaultOpenCodeGoModelIDs()
	for _, id := range DefaultOpenCodeZenFreeModelIDs() {
		require.Contains(t, catalog, id)
	}

	// muse-spark-*-free 必须命中 muse-spark-* → Responses 规则。
	require.Equal(t, APIProtocolResponses, matchProtocolRules("muse-spark-1.3-contributor-free", DefaultOpenCodeGoProtocolRules()))
	// 其余免费模型走 Chat Completions。
	require.Equal(t, APIProtocolChatCompletions, matchProtocolRules("mimo-v2.6-flash-free", DefaultOpenCodeGoProtocolRules()))
	require.Equal(t, APIProtocolChatCompletions, matchProtocolRules("deepseek-v4-flash-free", DefaultOpenCodeGoProtocolRules()))
}

// TestOpenCodeFreeTierGateEndToEndBodyShape 把四项硬要求一起断言，作为门禁的
// 验收测试：会话形状 + 位不变量、UA 版本带、bash+read、stream:true。
func TestOpenCodeFreeTierGateEndToEndBodyShape(t *testing.T) {
	account := openCodeGateTestAccount(nil)
	headers := http.Header{}
	headers.Set(openCodeSessionHeader, "3f2504e0-4f89-11d3-9a0c-0305e82c3301")
	body := []byte(`{"model":"mimo-v2.6-flash-free","stream":false,"messages":[{"role":"user","content":"hi"}]}`)

	out, err := applyOpenCodeFreeTierGateBody(account, body, openCodeGateDialectChat)
	require.NoError(t, err)
	applyOpenCodeFreeTierGateSession(account, headers, body)

	// 1. 会话形状 + 位不变量（hex 段低 12 位必须为 0xFFE）
	session := headers.Get(openCodeSessionHeader)
	require.True(t, openCodeCanonicalSessionRe.MatchString(session), "会话形状必须合法，实际 %q", session)
	require.True(t, isOpenCodeGateSessionID(session), "会话位不变量必须满足，实际 %q", session)
	require.Equal(t, openCodeGateSessionLowHex, session[13:16])
	// 2. UA 版本必须落在上游当前接受的闭区间 [1.18.0, 1.18.33]
	requireOpenCodeUAVersionInBand(t, openCodeUpstreamUserAgent)
	// 3. bash + read
	requireGateTools(t, out, "function.name")
	// 4. stream:true
	require.True(t, gjson.GetBytes(out, "stream").Bool())

	// body 必须是合法 JSON（门禁改写不得破坏载荷）。
	var decoded map[string]any
	require.NoError(t, json.Unmarshal(out, &decoded))
}

// openCodeUAVersionBandLow / High 是上游免费层门禁当前接受的 UA 版本闭区间。
//
// 2026-10-15 单变量实测（同一 key / body / 出口，只改 UA 版本号）：
//
//	1.17.9                 -> 426 UpgradeRequired
//	1.18.0/1.18.1/1.18.31/1.18.32/1.18.33 -> 200
//	1.18.34/1.18.35/1.19.0/1.20.0         -> 403 FreeTierError
//
// 上界是上游 2026-10-14 13:45-13:53 UTC 收紧版本带时钉下的，官方最新 1.19.0
// 客户端同样被拒（上游回归）。**上界会变**：若上游再次调整，必须重测并同时
// 更新这两个常量、openCodeUpstreamUserAgent 与 runbook。
var (
	openCodeUAVersionBandLow  = [3]int{1, 18, 0}
	openCodeUAVersionBandHigh = [3]int{1, 18, 33}
)

// TestOpenCodeUpstreamUserAgentVersionBand 拦住「顺手把版本号升到官方最新」
// 这类危险操作（2026-10-13 正是这样把 UA 升到 1.19.0，次日上游收紧版本带后
// 全站 403）：常量版本必须落在实测闭区间内。
func TestOpenCodeUpstreamUserAgentVersionBand(t *testing.T) {
	requireOpenCodeUAVersionInBand(t, openCodeUpstreamUserAgent)

	// 断言这个测试真的能拦住越界版本（防「断言写松了」导致形同虚设）。
	require.False(t, openCodeUAVersionInBand("opencode/1.19.0"), "1.19.0 必须被判为越界（上界外）")
	require.False(t, openCodeUAVersionInBand("opencode/1.18.34"), "1.18.34 必须被判为越界（上界外）")
	require.False(t, openCodeUAVersionInBand("opencode/1.17.9"), "1.17.9 必须被判为越界（下界外）")
	require.True(t, openCodeUAVersionInBand("opencode/1.18.0"), "下界闭区间端点必须通过")
	require.True(t, openCodeUAVersionInBand("opencode/1.18.33"), "上界闭区间端点必须通过")
	require.True(t, openCodeUAVersionInBand("opencode/1.18.31 ai-sdk/provider-utils/4.0.23 runtime/bun/1.3.14"),
		"带后缀的完整身份串必须能解析出版本")
	require.False(t, openCodeUAVersionInBand("curl/8.0"), "非 opencode/ 形态必须被拒")
}

// requireOpenCodeUAVersionInBand 断言 UA 中的 opencode/<version> 落在
// [openCodeUAVersionBandLow, openCodeUAVersionBandHigh] 内。
func requireOpenCodeUAVersionInBand(t *testing.T, ua string) {
	t.Helper()
	version, ok := parseOpenCodeUAVersion(ua)
	require.True(t, ok, "UA 必须形如 opencode/<major>.<minor>.<patch>，实际 %q", ua)
	require.GreaterOrEqual(t, compareOpenCodeVersions(version, openCodeUAVersionBandLow), 0,
		"UA 版本 %v 低于上游下限 %v（上游返回 426 UpgradeRequired）", version, openCodeUAVersionBandLow)
	require.LessOrEqual(t, compareOpenCodeVersions(version, openCodeUAVersionBandHigh), 0,
		"UA 版本 %v 高于上游上界 %v（上游返回 403 FreeTierError）：不要顺手把版本号升到官方最新版",
		version, openCodeUAVersionBandHigh)
}

// openCodeUAVersionInBand 是 requireOpenCodeUAVersionInBand 的非断言版本，
// 供测试自证使用。
func openCodeUAVersionInBand(ua string) bool {
	version, ok := parseOpenCodeUAVersion(ua)
	if !ok {
		return false
	}
	return compareOpenCodeVersions(version, openCodeUAVersionBandLow) >= 0 &&
		compareOpenCodeVersions(version, openCodeUAVersionBandHigh) <= 0
}

// parseOpenCodeUAVersion 从 UA 首段解析 opencode/<major>.<minor>.<patch>。
func parseOpenCodeUAVersion(ua string) ([3]int, bool) {
	fields := strings.Fields(ua)
	if len(fields) == 0 {
		return [3]int{}, false
	}
	raw := strings.TrimPrefix(fields[0], "opencode/")
	if raw == fields[0] {
		return [3]int{}, false
	}
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		return [3]int{}, false
	}
	var out [3]int
	for i, part := range parts {
		n, err := strconv.Atoi(part)
		if err != nil || n < 0 {
			return [3]int{}, false
		}
		out[i] = n
	}
	return out, true
}

// compareOpenCodeVersions 按 major/minor/patch 数值比较，返回 -1/0/1。
func compareOpenCodeVersions(a, b [3]int) int {
	for i := range a {
		if a[i] != b[i] {
			if a[i] < b[i] {
				return -1
			}
			return 1
		}
	}
	return 0
}
