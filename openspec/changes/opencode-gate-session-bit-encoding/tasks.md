## 1. 位不变量与铸造

- [x] 1.1 新增常量 `openCodeGateSessionLowHex = "ffe"`（门禁位不变量：hex 段低 12 位）
- [x] 1.2 新增常量 `openCodeGateSessionEpochMillis = int64(27) << 36`（官方递减计数起点，≈2028-10-13）
- [x] 1.3 新增常量 `openCodeGateSessionMintCacheSize = 4096`（铸造缓存容量）
- [x] 1.4 实现 `isOpenCodeGateSessionID(id) bool`：形状正则 **且** `id[13:16] == "ffe"`，两者都满足才合法
- [x] 1.5 实现 `openCodeGateSessionHexPart()` / `openCodeGateSessionHexPartAt(nowMillis)`：`((epoch-1-now) << 12) | 0xFFE`，取低 48 位
- [x] 1.6 实现 `mintOpenCodeSessionID()`：官方编码 hex + `crypto/rand` 生成的 14 位 base62 后缀
- [x] 1.7 实现 `mintOpenCodeSessionIDForSource(source)`：后缀复用 `canonicalOpenCodeID` 的确定性派生，hex 段按官方编码
- [x] 1.8 实现 `openCodeGateSessionMintCache`（`sync.Mutex` + map + FIFO order）与 `getOrMint(source, mint)`，锁内完成查/铸/写
- [x] 1.9 新增进程级缓存 `openCodeGateSessionMints`
- [x] 1.10 在 `canonicalOpenCodeID` 注释中标注「只保证形状，不保证门禁位不变量」

## 2. 三处调用点

- [x] 2.1 `canonicalOpenCodeSessionID`：透传判定从 `openCodeCanonicalSessionRe.MatchString` 改为 `isOpenCodeGateSessionID`（哈希型客户端值必须重铸）
- [x] 2.2 `canonicalOpenCodeSessionID`：`canonicalOpenCodeID("ses", source)` 改为 `mintOpenCodeSessionIDForSource(source)`（走缓存）
- [x] 2.3 `applyOpenCodeFreeTierGateSession`：兜底 `canonicalOpenCodeID("ses", uuid.NewString())` 改为 `mintOpenCodeSessionID()`
- [x] 2.4 移除 `openai_opencode_freetier.go` 中不再使用的 `github.com/google/uuid` 导入
- [x] 2.5 更新文件头「四组信号」注释：信号 1 补充位不变量，并记录 2026-10-11 上游变更与 `83f2b6f67a`

## 3. UA 注释修正

- [x] 3.1 改写 `openai_opencode_session.go:18-32`：明确 UA 版本**不是**门禁检查项（实测 1.18.31 与 1.19.0 同样 200），本次 403 的真实根因是会话位编码
- [x] 3.2 保留「低于版本下限返回 426 UpgradeRequired（1.17.9 拒绝、1.18.0 通过）」这条实测结论
- [x] 3.3 保留 UA 取值 `opencode/1.19.0 ai-sdk/provider-utils/4.0.23 runtime/bun/1.3.14`，说明「保持最新无害」

## 4. 单元测试

- [x] 4.1 `TestIsOpenCodeGateSessionID`：接受 `ffe` 结尾（含实测 200 的真实 id），拒绝 `f65`/`fff`/`000`/哈希派生值/UUID/短串/空串/大写 hex/长度不符/带空白
- [x] 4.2 `TestMintOpenCodeSessionIDInvariant`：四条铸造路径（随机兜底、按来源、规范改写、门禁端到端）各跑多次，全部满足形状 + 位不变量 + 时间戳自洽
- [x] 4.3 `TestOpenCodeGateSessionHexMatchesOfficialEncoding`：用逐字复刻的官方 `tA()` 对 570+ 个时间点逐位比对，证明闭式与官方编码等价（含窗口边界与窗口外）
- [x] 4.4 `TestOpenCodeGateSessionHexCounterOneIsGateForm`：钉住「`low12 == 0xFFE` ⟺ `counter == 1`」
- [x] 4.5 `TestCanonicalOpenCodeSessionIDRekeysHashShapedValues`：哈希型（形状合法、位不合法）客户端值必须重铸，合法值原样保留，空来源不铸造
- [x] 4.6 `TestOpenCodeGateSessionMintCacheStability`：同一 source 恒等且不重复铸造、不同 source 不同、产出都过门禁
- [x] 4.7 `TestOpenCodeGateSessionMintCacheEviction`：容量 2 时 FIFO 淘汰最早条目，被淘汰者重铸且仍过门禁，缓存有界
- [x] 4.8 `TestOpenCodeGateSessionMintCacheConcurrent`：16 个 goroutine 并发取同一 source 必须得到同一 id
- [x] 4.9 更新既有用例：`TestApplyOpenCodeFreeTierGateSession/客户端已提供合法值时原样保留` 改用门禁合法样例，新增「形状合法但位不合法必须重铸」子测试
- [x] 4.10 更新 `TestOpenCodeFreeTierGateEndToEndBodyShape`：第一项断言从「形状」加强为「形状 + 位不变量」
- [x] 4.11 确认既有「只断言形状正则」的用例仍通过（`TestCanonicalOpenCodeIDShape` / `TestCanonicalOpenCodeIDDeterministicAndDistinct` 未改动且通过）

## 5. 排查工具

- [x] 5.1 重写 `openai_opencode_freetier_probe_live_test.go`：4 个模式 `current` / `superset` / `shapeonly` / `nogate`，并在文件头写明每个模式的预期状态码与封禁风险
- [x] 5.2 探针 dump 新增 `=== SESSION DIAGNOSTICS ===` 段：形状、`hex12`、`low12`、`gate_ok`、官方编码自洽校验
- [x] 5.3 **修复探针 dump 泄漏 API key 的缺陷**：`Authorization` 逐头掩码 + 写文件前对整份 dump 再抹一遍原始 key（双保险）
- [x] 5.4 移除探针里重复实现的 `gateValidSessionID()` / `zenSessionEpochMillis`，改为调用生产实现，消除算法漂移
- [x] 5.5 `TestLiveOpenCodeFreeTierGate` 子测试重排：三个正向组在前、故意 403 的对照组在最后（对照组会封禁 key 约 15 分钟），并加注释说明顺序是有意的
- [x] 5.6 live 测试的会话断言从「形状正则」加强为 `isOpenCodeGateSessionID`
- [x] 5.7 修复 `.oc-gate/getkey.sh` 的掩码正则：从只匹配 `sk-` 改为覆盖 `oc_sk_`/`sk-`/`sk_`，且 `mask=` 只显示方案前缀与末 4 位

## 6. 验证

- [x] 6.1 `go build ./...` 通过
- [x] 6.2 `go vet ./internal/service/`（unit 与 live 两种构建标签）通过
- [x] 6.3 `gofmt -l` 无输出
- [x] 6.4 相关单测通过：`go test -tags=unit ./internal/service/ -run 'OpenCode' -count=1` → ok
- [x] 6.5 全量 `go test -tags=unit ./internal/service/...`：连续两次仅既有失败 `TestOllamaProbeCallback_StaleLongDoesNotOverrideNewShort`；首次出现的 `TestFilterGrokFreeQuotaAccountsOnlyBlocksExplicitFreeOAuth` 在重跑中未复现，且与本次改动无关（见 verification.md）
- [x] 6.6 **上游实测 200**：`ZEN_PROBE=current` 单次请求返回 `200 OK`，`low12=ffe`，官方编码自洽 `delta_ms=0`
- [x] 6.7 **上游实测全绿**：`go test -tags=live -run TestLiveOpenCodeFreeTierGate` → PASS（3 个正向组 200 + 对照组 403）
- [x] 6.8 用干净 HEAD 工作树复现既有失败，确认与本次改动无关

## 7. 文档

- [x] 7.1 `proposal.md`：问题、根因、单变量证据、方案、非目标、影响面
- [x] 7.2 `design.md`：官方编码算法与等价证明、缓存策略、为什么重铸、为什么不加非必需头、UA 不是检查项
- [x] 7.3 `verification.md`：403→200 单变量对照表、响应体、cf-ray、live 测试原始输出、泄漏事件如实记录、验证边界
- [x] 7.4 `specs/opencode-gate-session-bit-encoding/spec.md`：位不变量、铸造、缓存、重铸、覆盖全部调用点
- [x] 7.5 `.openspec.yaml`
- [x] 7.6 **`docs/opencode-free-tier-gate-runbook.md`**：症状分流、单变量排查法、工具清单、门禁要求清单、重新推导算法的方法、历史变更记录

## 8. 交付

- [x] 8.1 分支 `fix/opencode-gate-session-bit-encoding`
- [x] 8.2 推送并提 PR：<https://github.com/mnwlzf/sub2api/pull/21>（**未自行合并**）
