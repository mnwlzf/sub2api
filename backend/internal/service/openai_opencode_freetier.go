package service

import (
	cryptorand "crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
)

// OpenCode Zen 免费层门禁伪装。
//
// 上游对免费层模型（模型 ID 以 -free 结尾）实施「仅允许官方 OpenCode CLI
// 发起」的客户端身份门禁，判定是四组独立信号的与，缺一即 403 FreeTierError
// "OpenCode's free tier can only be used from within OpenCode"：
//
//  1. X-Opencode-Session 必须是 ses_ + 12 位小写 hex + 14 位字母数字，
//     **且 hex 段低 12 位必须等于 0xFFE**（形状 + 位不变量，两者缺一即 403）
//  2. User-Agent 必须为 opencode/<version> 且 version >= 1.18.0
//     （1.17.9 返回 426 UpgradeRequired）
//  3. body 的 tools 必须同时含 bash 与 read（只含其一仍 403）
//  4. body 的 stream 必须为 true（false 返回 403）
//
// 信号 1 的位不变量是 2026-10-11 上游把 /zen/* 改成指向新 inference 服务的
// 薄代理（anomalyco/opencode commit 83f2b6f67a）时引入的：门禁从「只查形状」
// 升级为「查形状 + 查位不变量」。此前用 sha256 派生 hex（等价随机）必然被拒，
// 详见 openCodeGateSessionLowHex 与 mintOpenCodeSessionID。
//
// 四条信号均为对 https://opencode.ai/zen/v1/chat/completions 的实测结论
// （Authorization: Bearer public，模型 mimo-v2.6-flash-free；信号 2/3/4 为
// 2026-09-30，信号 1 的位不变量为 2026-10-13）。X-Opencode-Request / Client /
// Project / Accept / X-Session-Affinity / X-Session-Id 经实测非必需，故不依赖
// 它们通过门禁（affinity 头仍然镜像，仅为形态对齐）。
//
// 注入的 bash / read 工具对模型可见，模型理论上可能发起同名工具调用，因此
// 伪装默认只作用于 -free 模型（见 free_tier_gate=auto），并可被账号级开关
// 收窄或关闭。

const (
	// openCodeFreeTierGateCredential 是账号级门禁开关的凭据键。
	openCodeFreeTierGateCredential = "free_tier_gate"

	// openCodeGateModeAuto 仅对 -free 模型伪装（缺省）。
	openCodeGateModeAuto = "auto"
	// openCodeGateModeAlways 对该账号全部 OpenCode 请求伪装，用于上游规则
	// 收紧时的应急开关。
	openCodeGateModeAlways = "always"
	// openCodeGateModeOff 完全关闭伪装。
	openCodeGateModeOff = "off"

	// 门禁 body 方言。三条出站 funnel 各自明确知道自己转发的是哪种协议，
	// 因此方言由调用方显式传入，不做启发式嗅探。
	openCodeGateDialectChat      = "chat"
	openCodeGateDialectResponses = "responses"
	openCodeGateDialectAnthropic = "anthropic"
)

// openCodeCanonicalSessionRe 是上游接受的会话 ID 形状：ses_ + 12 位小写 hex
// + 14 位字母数字，总长恒为 30。
//
// 注意：形状只是必要条件。2026-10-11 起还必须满足位不变量
// （hex 段低 12 位 == 0xFFE），见 isOpenCodeGateSessionID。
var openCodeCanonicalSessionRe = regexp.MustCompile(`^ses_[0-9a-f]{12}[0-9A-Za-z]{14}$`)

const (
	// openCodeGateSessionLowHex 是门禁的位不变量：X-Opencode-Session 的 12 位
	// hex 字段，其低 12 位（即末 3 个 hex 字符）必须等于 0xFFE。
	//
	// 单变量实测（2026-10-13，头 / UA / body 逐字节相同，只改会话值）：
	//
	//	hex=d1bfaf4aff65（末 3 位 f65）→ 403 FreeTierError
	//	hex=ecb74175effe（末 3 位 ffe）→ 200 OK
	//
	// 这条不变量不满足时上游返回与「缺会话头」完全相同的 403，因此极易被误判
	// 成形状问题。排查入口见 docs/opencode-free-tier-gate-runbook.md。
	openCodeGateSessionLowHex = "ffe"

	// openCodeGateSessionEpochMillis 是官方客户端递减计数的起点常量：
	// 27 << 36 毫秒 = 1855425871872（≈ 2028-10-13）。
	//
	// 官方编码（从 opencode 1.19.0 二进制偏移 105055473 挖出的 tA()）：
	//
	//	$ = BigInt(Date.now())*0x1000n + counter;   // counter 自 1 起，同毫秒内递增
	//	U = descending ? ~$ : $;                    // Session ID 走 descending
	//	hex = U 的低 48 位
	//
	// counter == 1 时逐位等价于：
	//
	//	hex = ((epoch - 1 - now) << 12) | 0xFFE
	//
	// 该恒等式对任意 now 成立（模 2^48）。自洽校验等式为
	// (hex >> 12) + mintedAtMillis == epoch - 1；用真实客户端 id 反推即可验证。
	openCodeGateSessionEpochMillis = int64(27) << 36

	// openCodeGateSessionMintCacheSize 是会话铸造缓存的容量上限。
	// 4096 远大于典型并发会话数；淘汰只会让该会话下一轮换一次 id（仍过门禁，
	// 仅损失一次上游 prompt cache 命中），不构成正确性问题。
	openCodeGateSessionMintCacheSize = 4096
)

// openCodeGateTool 是官方 OpenCode 客户端内置工具的定义。
type openCodeGateTool struct {
	Name        string
	Description string
	Parameters  map[string]any
}

// openCodeGateTools 是官方客户端的 6 个核心工具，按名称字母序（官方客户端
// 即按此顺序发送）。与参考实现 cpa-plugin-opencodezen 的 officialGateTools 对齐。
//
// 门禁只校验工具名存在（bash 与 read 必须齐备，实测缺一即 403），补齐其余
// 4 个是为了让出站流量与官方客户端形态一致，降低上游后续收紧规则时的回归风险。
var openCodeGateTools = []openCodeGateTool{
	{
		Name:        "bash",
		Description: "Execute bash commands in the workspace environment",
		Parameters: openCodeGateObjectSchema(map[string]any{
			"command": openCodeGateStringSchema("Shell command string to execute"),
			"workdir": openCodeGateStringSchema("Working directory. Defaults to the active Location; relative paths resolve within it."),
		}, "command"),
	},
	{
		Name:        "edit",
		Description: "Edit a file by replacing text",
		Parameters: openCodeGateObjectSchema(map[string]any{
			"path":       openCodeGateStringSchema("File path to edit. Relative paths resolve within the active Location."),
			"oldString":  openCodeGateStringSchema("The string in the file to be replaced"),
			"newString":  openCodeGateStringSchema("The string to replace oldString with"),
			"replaceAll": openCodeGateBooleanSchema("Replace all occurrences of oldString (default false)"),
		}, "path", "oldString", "newString"),
	},
	{
		Name:        "glob",
		Description: "Find files matching a glob pattern",
		Parameters: openCodeGateObjectSchema(map[string]any{
			"pattern": openCodeGateStringSchema(`File pattern to include in the search (e.g. "*.js", "*.{ts,tsx}")`),
			"path":    openCodeGateStringSchema("Relative directory to search in. Defaults to the active Location."),
		}, "pattern"),
	},
	{
		Name:        "grep",
		Description: "Search file contents using regular expressions",
		Parameters: openCodeGateObjectSchema(map[string]any{
			"pattern": openCodeGateStringSchema("Regex pattern to search for in file contents"),
			"path":    openCodeGateStringSchema("Relative directory to search in. Defaults to the active Location."),
		}, "pattern"),
	},
	{
		Name:        "read",
		Description: "Read file contents",
		Parameters: openCodeGateObjectSchema(map[string]any{
			"path":   openCodeGateStringSchema("File path to read. Relative paths resolve within the active Location."),
			"offset": openCodeGateNumberSchema("The 1-based directory entry or text line offset"),
			"limit":  openCodeGateNumberSchema("The maximum number of lines to read (defaults to 2000)"),
		}, "path"),
	},
	{
		Name:        "write",
		Description: "Write or overwrite file contents",
		Parameters: openCodeGateObjectSchema(map[string]any{
			"path":    openCodeGateStringSchema("File path to write. Relative paths resolve within the active Location."),
			"content": openCodeGateStringSchema("Content to write to the file"),
		}, "path", "content"),
	},
}

func openCodeGateObjectSchema(properties map[string]any, required ...string) map[string]any {
	req := make([]any, 0, len(required))
	for _, name := range required {
		req = append(req, name)
	}
	return map[string]any{
		"type":       "object",
		"properties": properties,
		"required":   req,
	}
}

func openCodeGateStringSchema(description string) map[string]any {
	return map[string]any{"type": "string", "description": description}
}

func openCodeGateNumberSchema(description string) map[string]any {
	return map[string]any{"type": "number", "description": description}
}

func openCodeGateBooleanSchema(description string) map[string]any {
	return map[string]any{"type": "boolean", "description": description}
}

// openCodeGateSchemaRefPattern 匹配 JSON Schema 的引用关键字。
var openCodeGateSchemaRefPattern = regexp.MustCompile(`"\$(?:ref|defs|dynamicRef|dynamicAnchor|recursiveRef|recursiveAnchor)"\s*:`)

// openCodeGateLogSchemaRefs 在出站工具集里出现 JSON Schema 引用关键字时打一条诊断
// 日志。
//
// OpenCode 的 /responses 上游会以 400 "Recursive JSON schemas are not currently
// supported" 拒绝带引用的工具 schema。门禁自己补齐的工具是纯扁平 schema（不可能
// 出现引用），所以命中的一定是客户端带来的工具。记下工具名与引用目标，便于定位
// 是哪个客户端工具触发的。
func openCodeGateLogSchemaRefs(account *Account, dialect string, tools []json.RawMessage) {
	for _, tool := range tools {
		raw := string(tool)
		if !openCodeGateSchemaRefPattern.MatchString(raw) {
			continue
		}
		accountID := int64(0)
		if account != nil {
			accountID = account.ID
		}
		logger.LegacyPrintf("service.opencode_gate",
			"opencode free tier gate: tool %q carries JSON Schema reference keywords %v (account=%d dialect=%s); OpenCode /responses rejects recursive JSON schemas",
			openCodeGateToolName(tool, dialect),
			openCodeGateSchemaRefPattern.FindAllString(raw, 8),
			accountID, dialect)
	}
}

// canonicalOpenCodeID 把任意来源字符串确定性映射到上游规范 ID 形状
// （<prefix>_ + 12 位小写 hex + 14 位字母数字，共 30 字符）。
//
// 确定性是硬要求而非优化：会话 ID 每轮随机会让上游 prompt cache 永不命中，
// 因此同一来源必须恒等映射。
//
// **本函数只保证形状，不保证 ses_ 的门禁位不变量**（hex 段低 12 位必须是
// 0xFFE）。会话 id 必须走 canonicalOpenCodeSessionID / mintOpenCodeSessionID；
// 这里保留是因为门禁铸造复用了它的 14 位后缀派生（见
// openCodeGateSessionSuffix）。
func canonicalOpenCodeID(prefix, source string) string {
	sum := sha256.Sum256([]byte(source))
	hexPart := hex.EncodeToString(sum[:6])
	const alphabet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
	suffix := make([]byte, 14)
	for i := 0; i < 14; i++ {
		suffix[i] = alphabet[int(sum[6+i])%len(alphabet)]
	}
	return prefix + "_" + hexPart + string(suffix)
}

// openCodeGateSessionHexPart 返回门禁有效的 12 位小写 hex 段。
//
// 逐位复刻官方 tA(descending=true) 在 counter == 1 时的取值：
//
//	hex = ((epoch - 1 - now) << 12) | 0xFFE
//
// 低位 12 bit 恒为 0xFFE（门禁位不变量），高 36 bit 是自 epoch-1 起的递减毫秒
// 计数。这里的 -1 不是近似：官方值等于 ((epoch - now) << 12) - 2，少减一个
// 单位。门禁当前只读低 12 位，两者都能过；但保持逐位一致可以在门禁将来收紧到
// 高 36 位时不用再改一次，且让「与官方编码等价」成为可证命题。
//
// 即便系统时钟越过 epoch（remaining 为负）或严重偏移，模 2^48 掩码后低 12 位
// 仍是 0xFFE，不变量不会被破坏。
func openCodeGateSessionHexPart() string {
	return openCodeGateSessionHexPartAt(time.Now().UnixMilli())
}

// openCodeGateSessionHexPartAt 是 openCodeGateSessionHexPart 的可注入时间版本，
// 便于用官方编码逐位交叉验证。
func openCodeGateSessionHexPartAt(nowMillis int64) string {
	remaining := openCodeGateSessionEpochMillis - 1 - nowMillis
	value := uint64(remaining)<<12 | 0xFFE
	return fmt.Sprintf("%012x", value&0xFFFFFFFFFFFF)
}

// openCodeGateSessionSuffix 从 source 确定性派生 14 位 base62 后缀。
// 复用 canonicalOpenCodeID 的哈希派生（其 12 位 hex 段在此被丢弃，因为 hex
// 段必须由 openCodeGateSessionHexPart 按官方编码生成）。
func openCodeGateSessionSuffix(source string) string {
	return canonicalOpenCodeID("ses", source)[16:]
}

// openCodeGateRandomSessionSuffix 用 crypto/rand 生成 14 位 base62 后缀。
func openCodeGateRandomSessionSuffix() string {
	const alphabet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
	raw := make([]byte, 14)
	if _, err := cryptorand.Read(raw); err != nil {
		// crypto/rand.Read 在受支持平台上不返回错误；真出错时退化为时间派生值，
		// 保证门禁形状与位不变量仍成立，而不是 panic。
		sum := sha256.Sum256([]byte(strconv.FormatInt(time.Now().UnixNano(), 10)))
		copy(raw, sum[:14])
	}
	suffix := make([]byte, 14)
	for i, b := range raw {
		suffix[i] = alphabet[int(b)%len(alphabet)]
	}
	return string(suffix)
}

// mintOpenCodeSessionID 按官方编码铸造一个满足门禁位不变量的会话 id：
// ses_<12 位小写 hex><14 位 base62>，hex 段低 12 位恒为 0xFFE。
//
// 每次调用都会得到不同的值（编码含当前时间且后缀随机），因此只用于「既无
// 客户端会话标识、也无法从请求体推导稳定种子」的最后兜底；有稳定来源时走
// mintOpenCodeSessionIDForSource 以保住上游 prompt cache。
func mintOpenCodeSessionID() string {
	return "ses_" + openCodeGateSessionHexPart() + openCodeGateRandomSessionSuffix()
}

// mintOpenCodeSessionIDForSource 铸造与 source 绑定的稳定会话 id。
//
// 后缀由 source 确定性派生，因此即便缓存条目被淘汰或进程重启，同一 source
// 的后缀也不会变；hex 段由官方编码生成，保证位不变量。
func mintOpenCodeSessionIDForSource(source string) string {
	return openCodeGateSessionMints.getOrMint(source, func() string {
		return "ses_" + openCodeGateSessionHexPart() + openCodeGateSessionSuffix(source)
	})
}

// isOpenCodeGateSessionID 校验会话 id 是否同时满足门禁的**形状**与**位不变量**。
//
// 形状合法但 hex 段低位不是 0xFFE 的值必须被判为非法：任何用 sha256 派生 hex
// 的实现（含我们 2026-10-11 之前的算法、以及部分上游插件的 canonicalID）都会
// 产出这种值，原样透传会被上游以 403 FreeTierError 拒绝。
func isOpenCodeGateSessionID(id string) bool {
	if !openCodeCanonicalSessionRe.MatchString(id) {
		return false
	}
	// 正则已保证 id = "ses_" + 12 位小写 hex + 14 位字母数字，
	// 故 id[4:16] 是 hex 段，其末 3 个字符（低 12 位）必须为 ffe。
	return id[13:16] == openCodeGateSessionLowHex
}

// openCodeGateSessionMintCache 把「会话来源字符串」有界地映射到已铸造的会话 id，
// 保证同一会话跨轮次稳定。
//
// 为什么必须有缓存：官方编码把当前时间编进 hex 段，逐次铸造必然得到不同值。
// 同一会话每轮换 id 会让上游 prompt cache 永不命中（每轮全量重算，成本与延迟
// 都显著上升）。缓存把 source → id 固定下来；因为 id 本身已满足位不变量，
// 缓存不会影响门禁通过性。
//
// 淘汰策略：FIFO（先进先出），容量 openCodeGateSessionMintCacheSize。选 FIFO
// 而非 LRU 是因为状态完全在锁内、实现与推理都最简单，且 4096 远大于典型并发
// 会话数。被淘汰的会话下一轮会拿到新 id——仍然过门禁，只是损失一次 prompt
// cache 命中，不构成正确性问题。
type openCodeGateSessionMintCache struct {
	mu    sync.Mutex
	max   int
	ids   map[string]string
	order []string
}

func newOpenCodeGateSessionMintCache(max int) *openCodeGateSessionMintCache {
	return &openCodeGateSessionMintCache{max: max, ids: make(map[string]string, max)}
}

// getOrMint 返回 source 对应的稳定会话 id，缺失时在锁内调用 mint 铸造。
// 铸造在锁内完成，避免并发下同一 source 产生两个不同 id。
func (c *openCodeGateSessionMintCache) getOrMint(source string, mint func() string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if id, ok := c.ids[source]; ok {
		return id
	}
	id := mint()
	if c.max <= 0 {
		return id
	}
	if len(c.order) >= c.max {
		delete(c.ids, c.order[0])
		copy(c.order, c.order[1:])
		c.order = c.order[:len(c.order)-1]
	}
	c.ids[source] = id
	c.order = append(c.order, source)
	return id
}

// openCodeGateSessionMints 是进程级会话铸造缓存。
var openCodeGateSessionMints = newOpenCodeGateSessionMintCache(openCodeGateSessionMintCacheSize)

// isOpenCodeFreeTierModel 判断模型是否为免费层模型。判定大小写不敏感，并
// 先剥离 opencode-go/ 等路由前缀。
func isOpenCodeFreeTierModel(model string) bool {
	model = normalizeOpenCodeGoModelID(model)
	return model != "" && strings.HasSuffix(model, "-free")
}

// openCodeFreeTierGateMode 返回账号的门禁模式，缺省 auto，非法值回落 auto。
// 非法值不应出现（写入路径已校验），此处兜底是为了让历史数据保持可用。
func openCodeFreeTierGateMode(account *Account) string {
	if account == nil {
		return openCodeGateModeAuto
	}
	switch mode := strings.ToLower(strings.TrimSpace(account.GetCredential(openCodeFreeTierGateCredential))); mode {
	case openCodeGateModeAlways:
		return openCodeGateModeAlways
	case openCodeGateModeOff:
		return openCodeGateModeOff
	default:
		return openCodeGateModeAuto
	}
}

// shouldApplyOpenCodeFreeTierGate 判定本次请求是否需要门禁伪装。
// model 为空时 auto 模式返回 false：无法确认是否免费层，宁可不伪装。
func shouldApplyOpenCodeFreeTierGate(account *Account, model string) bool {
	if account == nil || !account.IsOpenCodeGo() {
		return false
	}
	switch openCodeFreeTierGateMode(account) {
	case openCodeGateModeOff:
		return false
	case openCodeGateModeAlways:
		return true
	default:
		return isOpenCodeFreeTierModel(model)
	}
}

// openCodeGateModelFromBody 读取出站 body 的模型名。取的是出站值而非客户端
// 原始值：门禁由上游对出站模型实施，因此判定必须与出站模型一致。
func openCodeGateModelFromBody(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	return strings.TrimSpace(gjson.GetBytes(body, "model").String())
}

// canonicalOpenCodeSessionID 把已解析出的会话来源规范化为上游接受的会话 id。
//
// 合法值（**形状与门禁位不变量都满足**）必须原样保留——重新铸造会破坏调用方
// 自建的会话连续性。其余一切值都要重铸，**包括形状合法但 hex 段低位不是
// 0xFFE 的值**：上游插件 / 客户端用哈希派生的 ses_ 值正是这种形态，原样透传会
// 把 403 FreeTierError 带进来（2026-10-13 单变量实测：hex=d1bfaf4aff65 → 403，
// hex=ecb74175effe → 200）。
func canonicalOpenCodeSessionID(source string) string {
	source = strings.TrimSpace(source)
	if isOpenCodeGateSessionID(source) {
		return source
	}
	if source == "" {
		return ""
	}
	return mintOpenCodeSessionIDForSource(source)
}

// applyOpenCodeFreeTierGateSession 在 applyOpenCodeSessionHeader 之后调用，
// 把已经落地的会话值改写为满足门禁的规范值（形状 + hex 段低位 0xFFE）。
// 复用既有解析链（客户端头 → prompt_cache_key / metadata.user_id → 账号覆写）
// 保证跨轮次稳定。
//
// 必须在 applyOpenCodeSessionHeader 之后调用，原因见 openCodeGateSessionSeed：
// Zen 模式下既有的自动生成条件不成立，会话头可能压根不存在，此时本函数负责
// 补一个，否则免费模型必然 403。
func applyOpenCodeFreeTierGateSession(account *Account, headers http.Header, body []byte) {
	if headers == nil {
		return
	}
	if !shouldApplyOpenCodeFreeTierGate(account, openCodeGateModelFromBody(body)) {
		return
	}
	current := existingOpenCodeSessionHeader(headers)
	if current == "" {
		current = openCodeGateSessionSeed(body)
	}
	canonical := canonicalOpenCodeSessionID(current)
	if canonical == "" {
		// 既无客户端会话标识、也无法从请求体推导稳定种子时的最后兜底：
		// 随机值会让 prompt cache 不命中，但至少满足门禁的形状与位不变量。
		canonical = mintOpenCodeSessionID()
	}
	for key := range headers {
		if strings.EqualFold(key, openCodeSessionHeader) {
			delete(headers, key)
		}
	}
	headers.Set(openCodeSessionHeader, canonical)
	// 官方客户端把会话值同时镜像到两个 affinity 头。门禁本身不校验它们
	// （实测只发 X-Opencode-Session 仍 200），补齐是为了让出站流量与官方
	// 客户端形态一致，降低上游后续收紧规则时的回归风险。
	headers.Set(openCodeSessionAffinityHeader, canonical)
	headers.Set(openCodeSessionIDHeader, canonical)
}

// openCodeGateSessionSeed 在既有一切会话来源都缺失时，从请求体推导一个跨轮次
// 稳定的会话种子。
//
// 为什么需要它：applyOpenCodeSessionHeader 只在 shouldGenerateOpenCodeSession
// 为真时生成兜底值，而该条件对 Zen 账号不成立——它要求目标路径含 /zen/go，
// Zen 的路径是 /zen/v1。因此「Zen 账号 + 客户端未提供会话标识」这一组合下，
// 出站请求完全没有 X-Opencode-Session，免费层门禁必然拒绝。
//
// 种子取模型名 + 首条用户消息，与 CPA 插件的 content fallback 同构：同一会话
// 跨轮次稳定（首条用户消息不变），从而不击穿上游 prompt cache。
func openCodeGateSessionSeed(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	if id := strings.TrimSpace(openCodeSessionIDFromPayload(body)); id != "" {
		return id
	}
	text := openCodeGateFirstUserText(body)
	if text == "" {
		return ""
	}
	return "content:" + openCodeGateModelFromBody(body) + "\x00" + text
}

// openCodeGateFirstUserText 取首条用户消息的文本，兼容 chat / responses 两种
// 载荷形状（content 为字符串或 parts 数组，responses 的 input 还可为裸字符串）。
func openCodeGateFirstUserText(body []byte) string {
	for _, path := range []string{"messages", "input"} {
		node := gjson.GetBytes(body, path)
		if node.Type == gjson.String {
			if text := strings.TrimSpace(node.String()); text != "" {
				return text
			}
			continue
		}
		if !node.IsArray() {
			continue
		}
		for _, item := range node.Array() {
			if item.Get("role").String() != "user" {
				continue
			}
			if text := openCodeGateContentText(item.Get("content")); text != "" {
				return text
			}
		}
	}
	return ""
}

func openCodeGateContentText(content gjson.Result) string {
	if content.Type == gjson.String {
		return strings.TrimSpace(content.String())
	}
	if content.IsArray() {
		for _, part := range content.Array() {
			if text := strings.TrimSpace(part.Get("text").String()); text != "" {
				return text
			}
		}
	}
	return ""
}

// applyOpenCodeFreeTierGateBody 对出站 body 施加门禁要求：强制 stream:true，
// 并按方言补齐工具（客户端已带工具时只补门禁硬性要求的 bash+read，否则补齐全
// 套官方工具）；原本不带工具的请求额外补 tool_choice 禁止工具调用。
// dialect 取 openCodeGateDialect* 之一。
//
// body 不是合法 JSON、或 tools 存在但不是数组时原样返回：门禁伪装是尽力而为
// 的上游兼容层，绝不能因为形状意外而让请求在网关侧失败。
func applyOpenCodeFreeTierGateBody(account *Account, body []byte, dialect string) ([]byte, error) {
	if len(body) == 0 {
		return body, nil
	}
	if !shouldApplyOpenCodeFreeTierGate(account, openCodeGateModelFromBody(body)) {
		return body, nil
	}
	if !gjson.ValidBytes(body) {
		return body, nil
	}

	out, err := sjson.SetBytes(body, "stream", true)
	if err != nil {
		return body, fmt.Errorf("opencode free tier gate: force stream: %w", err)
	}

	plan, err := openCodeGateToolsWithRequired(out, dialect)
	if err != nil {
		return body, err
	}
	if plan == nil {
		// tools 存在但不是数组：形状未知，不猜测也不改动。
		return out, nil
	}
	// 诊断：OpenCode /responses 会以 400 拒绝带 JSON Schema 引用的工具。
	// 门禁补齐的工具是纯扁平 schema，命中的必然是客户端带来的工具。
	openCodeGateLogSchemaRefs(account, dialect, plan.Tools)
	encoded, err := json.Marshal(plan.Tools)
	if err != nil {
		return body, fmt.Errorf("opencode free tier gate: encode tools: %w", err)
	}
	out, err = sjson.SetRawBytes(out, "tools", encoded)
	if err != nil {
		return body, fmt.Errorf("opencode free tier gate: write tools: %w", err)
	}
	// 原本不带工具的请求（压缩/总结、纯聊天客户端）在补齐工具集后禁止工具调用，
	// 否则模型会去调用客户端根本无法执行的工具。
	//
	// 但 OpenCode 的 /responses 端点只接受 tool_choice:"auto"，发送 "none" 会
	// 被上游 400 拒绝（实测生产环境稳定复现），因此只在接受该取值的方言上设置。
	if !plan.HadTools && openCodeGateSupportsToolChoiceNone(dialect) {
		out, err = sjson.SetBytes(out, "tool_choice", openCodeGateToolChoiceNone(dialect))
		if err != nil {
			return body, fmt.Errorf("opencode free tier gate: set tool_choice: %w", err)
		}
	}
	return out, nil
}

// openCodeGateToolPlan 是补齐工具集之后的计划。
type openCodeGateToolPlan struct {
	Tools    []json.RawMessage
	HadTools bool
}

// openCodeGateReservedToolNote 加在「客户端已自带工具」时补入的 bash/read 描述前面。
//
// 这两个工具只是为了让上游门禁通过：客户端（Codex 等）的注册表里并没有它们，
// 一旦模型调用，客户端会以 "unsupported call" 拒绝。实测 muse-spark + Codex
// 组合下模型会优先调用它们，而且被拒后还会沿用它们的参数形状去调客户端自己的
// 工具（bash 的 {command} 被套到 exec_command 上），把后续调用一起带偏。
// 因此描述里必须明确劝阻调用。
const openCodeGateReservedToolNote = "RESERVED RUNTIME MARKER — DO NOT CALL. This tool exists only to satisfy the upstream runtime handshake; the caller cannot execute it and will reject the call. Use the tools the caller provides instead. "

// openCodeGateRequiredToolNames 是门禁硬性要求的工具名：实测请求缺少其中任意
// 一个即返回 403 FreeTierError，换成客户端自定义的工具名（如 shell / read_file）
// 同样不满足。因此这两个名字必须出现在出站请求里。
var openCodeGateRequiredToolNames = []string{"bash", "read"}

// openCodeGateRequiredTools 是门禁硬性要求的最小工具集（bash + read）。
var openCodeGateRequiredTools = func() []openCodeGateTool {
	out := make([]openCodeGateTool, 0, len(openCodeGateRequiredToolNames))
	for _, name := range openCodeGateRequiredToolNames {
		for i := range openCodeGateTools {
			if openCodeGateTools[i].Name == name {
				out = append(out, openCodeGateTools[i])
				break
			}
		}
	}
	return out
}()

// openCodeGateSupportsToolChoiceNone 报告该方言能否安全使用「禁止工具调用」。
//
// OpenCode 的 /responses 端点只接受 tool_choice:"auto"，发送 "none" 会返回
// 400 invalid_request_error（"only `auto` is supported for `tool_choice`"）；
// /chat/completions 接受 "none"（实测 200）。因此只在 chat 方言上做该保护。
func openCodeGateSupportsToolChoiceNone(dialect string) bool {
	return dialect == openCodeGateDialectChat
}

// openCodeGateToolsToInject 返回本次需要补齐的工具集。
//
// 客户端已带工具时**只补门禁硬性要求的 bash+read**：注入客户端不认识、模型却
// 可能调用的工具名会被客户端以 "unsupported call" 拒绝，进而把整轮对话拖垮
// （实测 muse-spark + Codex 组合下模型会去调用注入的 bash/read，连拒数次后
// 吐出畸形 tool_call 导致整轮失败）。此时还会给它们加上劝阻描述，尽量让模型
// 不去调用。
//
// 客户端未带工具时，只有在**能禁止工具调用**的方言上才补齐全套官方工具（形态
// 更接近官方客户端，且模型不会真的去调）；无法禁止调用的方言仍只补最小集，
// 避免把模型可能误调的工具塞进请求。
func openCodeGateToolsToInject(hadTools bool, dialect string) []openCodeGateTool {
	if !hadTools && openCodeGateSupportsToolChoiceNone(dialect) {
		return openCodeGateTools
	}
	injected := make([]openCodeGateTool, 0, len(openCodeGateRequiredTools))
	for i := range openCodeGateRequiredTools {
		tool := openCodeGateRequiredTools[i]
		if hadTools {
			tool.Description = openCodeGateReservedToolNote + tool.Description
		}
		injected = append(injected, tool)
	}
	return injected
}

// openCodeGateToolsWithRequired 返回补齐后的工具集并按名称排序。
//
// 返回 nil 表示 tools 存在但不是数组：形状未知，调用方应放弃改动。
// HadTools 记录原始请求是否本来就带工具，用于决定补哪些工具与是否补
// tool_choice: none。
func openCodeGateToolsWithRequired(body []byte, dialect string) (*openCodeGateToolPlan, error) {
	raw := gjson.GetBytes(body, "tools")
	tools := make([]json.RawMessage, 0, len(openCodeGateTools))
	if raw.Exists() {
		if !raw.IsArray() {
			return nil, nil
		}
		for _, item := range raw.Array() {
			if item.Raw == "" {
				continue
			}
			tools = append(tools, json.RawMessage(item.Raw))
		}
	}
	hadTools := len(tools) > 0

	present := make(map[string]bool, len(tools))
	for _, item := range tools {
		if name := openCodeGateToolName(item, dialect); name != "" {
			present[name] = true
		}
	}
	wanted := openCodeGateToolsToInject(hadTools, dialect)
	for i := range wanted {
		if present[wanted[i].Name] {
			continue
		}
		encoded, err := openCodeGateToolJSON(&wanted[i], dialect)
		if err != nil {
			return nil, err
		}
		tools = append(tools, encoded)
	}
	// 官方客户端按工具名排序发送，保持一致。
	sort.SliceStable(tools, func(i, j int) bool {
		return openCodeGateToolName(tools[i], dialect) < openCodeGateToolName(tools[j], dialect)
	})
	return &openCodeGateToolPlan{Tools: tools, HadTools: hadTools}, nil
}

// openCodeGateToolName 按方言取出工具条目名。chat 方言为 function.name，
// responses 与 anthropic 方言为顶层 name。
func openCodeGateToolName(item json.RawMessage, dialect string) string {
	if dialect == openCodeGateDialectChat {
		return strings.TrimSpace(gjson.GetBytes(item, "function.name").String())
	}
	return strings.TrimSpace(gjson.GetBytes(item, "name").String())
}

// openCodeGateToolJSON 按方言构造官方工具定义。
func openCodeGateToolJSON(tool *openCodeGateTool, dialect string) (json.RawMessage, error) {
	var entry map[string]any
	switch dialect {
	case openCodeGateDialectChat:
		entry = map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        tool.Name,
				"description": tool.Description,
				"parameters":  tool.Parameters,
			},
		}
	case openCodeGateDialectAnthropic:
		// Anthropic 自定义工具无 type 包装，参数键为 input_schema。
		entry = map[string]any{
			"name":         tool.Name,
			"description":  tool.Description,
			"input_schema": tool.Parameters,
		}
	default:
		entry = map[string]any{
			"type":        "function",
			"name":        tool.Name,
			"description": tool.Description,
			"parameters":  tool.Parameters,
		}
	}
	encoded, err := json.Marshal(entry)
	if err != nil {
		return nil, fmt.Errorf("opencode free tier gate: encode tool %s: %w", tool.Name, err)
	}
	return encoded, nil
}

// openCodeGateToolChoiceNone 按方言返回「禁止调用工具」的取值。
// Anthropic 的 tool_choice 是对象而非字符串。
func openCodeGateToolChoiceNone(dialect string) any {
	if dialect == openCodeGateDialectAnthropic {
		return map[string]any{"type": "none"}
	}
	return "none"
}

// infraInvalidFreeTierGate 构造门禁模式非法时的 400 错误。
func infraInvalidFreeTierGate() error {
	return infraerrors.New(
		http.StatusBadRequest,
		"INVALID_OPENCODE_FREE_TIER_GATE",
		"free_tier_gate must be one of auto, always, off",
	)
}

// NormalizeOpenCodeFreeTierGateCredentials 校验并原地规范化
// credentials.free_tier_gate。未携带该字段时为 no-op，使旧账号继续按 auto
// 处理。非法取值返回 400 INVALID_OPENCODE_FREE_TIER_GATE。
func NormalizeOpenCodeFreeTierGateCredentials(credentials map[string]any) error {
	if credentials == nil {
		return nil
	}
	raw, ok := credentials[openCodeFreeTierGateCredential]
	if !ok || raw == nil {
		return nil
	}
	mode, ok := raw.(string)
	if !ok {
		return infraInvalidFreeTierGate()
	}
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case openCodeGateModeAuto:
		credentials[openCodeFreeTierGateCredential] = openCodeGateModeAuto
	case openCodeGateModeAlways:
		credentials[openCodeFreeTierGateCredential] = openCodeGateModeAlways
	case openCodeGateModeOff:
		credentials[openCodeFreeTierGateCredential] = openCodeGateModeOff
	default:
		return infraInvalidFreeTierGate()
	}
	return nil
}
