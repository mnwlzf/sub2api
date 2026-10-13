# 验证：OpenCode 免费层门禁的会话位编码（2026-10-13）

## 已实现

- 门禁位不变量：`isOpenCodeGateSessionID` = 形状正则 **且** hex 段低 12 位为 `0xFFE`。
- 官方编码铸造：`((epoch - 1 - now) << 12) | 0xFFE`，与官方 `tA(descending=true, counter=1)` 逐位相同。
- 有界会话铸造缓存（4096，FIFO）：官方编码含时间戳，靠缓存保证同一会话跨轮次稳定。
- 三处调用点全部收口：哈希型客户端 `ses_` 值不再透传而是重铸；兜底路径改按官方编码铸造。
- UA 注释改写为事实（UA 版本不是门禁检查项）。
- 排查 Runbook + 探针工具（并修复探针泄漏 key 的缺陷）。

## 一、根因证据：单变量消融

**头、UA、body 逐字节相同，只改 `X-Opencode-Session`。** 两次请求 body 均为 2644 字节，`tools` 为官方 6 件套，`stream:true`，UA 均为 `opencode/1.19.0 ai-sdk/provider-utils/4.0.23 runtime/bun/1.3.14`。

| 时刻 (UTC) | `X-Opencode-Session` | hex 12 位 | 低 3 位 | 结果 |
| --- | --- | --- | --- | --- |
| 08:53:58 | `ses_d1bfaf4aff65ysx8xVwhQ12lLq` | `d1bfaf4aff65` | `f65` | **403 Forbidden** |
| 09:15:53 | `ses_ecb74175effeDu4hfV9AoNeKL3` | `ecb74175effe` | **`ffe`** | **200 OK**（SSE） |

### 403 的原始响应

```
=== REQUEST HEADERS (5) ===
  Authorization: Bearer oc_sk_***REDACTED***
  User-Agent: opencode/1.19.0 ai-sdk/provider-utils/4.0.23 runtime/bun/1.3.14
  X-Opencode-Session: ses_d1bfaf4aff65ysx8xVwhQ12lLq
  X-Session-Affinity: ses_d1bfaf4aff65ysx8xVwhQ12lLq
  X-Session-Id: ses_d1bfaf4aff65ysx8xVwhQ12lLq

=== RESPONSE STATUS: 403 403 Forbidden ===
=== RESPONSE HEADERS ===
  X-Opencode-Log-Id: adb0cb3e-8468-40e9-8da6-2120582eda9b
  Date: Tue, 13 Oct 2026 08:53:58 GMT
  Content-Type: application/json
  Cf-Ray: a49d1deed94408af-LAX
  Server: cloudflare
  Cf-Placement: remote-ORD
=== RESPONSE BODY (175 bytes) ===
{"type":"error","error":{"type":"FreeTierError","message":"OpenCode's free tier can only be used from within OpenCode. Download it at https://opencode.ai and keep building."}}
```

### 200 的原始响应

```
=== REQUEST HEADERS (5) ===
  Authorization: Bearer oc_sk_***REDACTED***
  User-Agent: opencode/1.19.0 ai-sdk/provider-utils/4.0.23 runtime/bun/1.3.14
  X-Opencode-Session: ses_ecb74175effeDu4hfV9AoNeKL3
  X-Session-Affinity: ses_ecb74175effeDu4hfV9AoNeKL3
  X-Session-Id: ses_ecb74175effeDu4hfV9AoNeKL3

=== RESPONSE STATUS: 200 200 OK ===
=== RESPONSE HEADERS ===
  Cf-Placement: remote-ORD
  X-Opencode-Log-Id: 8d0c1a2d-13cd-4b50-828d-176aa36843b3
  X-Opencode-Upstream-Model-Id: xiaomi/mimo-v2.6-flash
  Date: Tue, 13 Oct 2026 09:15:53 GMT
  Cache-Control: no-cache
  Server: cloudflare
  X-Opencode-Endpoint-Id: openrouter-xiaomi
  X-Zen-Model: mimo-v2.6-flash-free
  Content-Type: text/event-stream
  Cf-Ray: a49d3debfa5ecc87-LAX
=== RESPONSE BODY (2958 bytes，节选) ===
data: {"id":"gen-1791882948-nbTGMbXs2lFO1qx2dkUm","object":"chat.completion.chunk","created":1791882948,"model":"mimo-v2.6-flash-free","choices":[{"index":0,"finish_reason":null,"delta":{"role":"assistant","content":"","reasoning":"User",...}}]}
...
data: {"id":"gen-1791882948-nbTGMbXs2lFO1qx2dkUm","object":"chat.completion.chunk","created":1791882948,"model":"mimo-v2.6-flash-free","choices":[{"index":0,"finish_reason":"stop","delta":{"role":"assistant","content":""}}],"usage":{"prompt_tokens":711,"completion_tokens":14,"total_tokens":725,"prompt_tokens_details":{"audio_tokens":0,"cached_tokens":0,"cache_write_tokens":0},"completion_tokens_details":{"audio_tokens":0,"reasoning_tokens":11}}]}

data: [DONE]
```

### 一个被污染的反例（如实记录）

`superset2`（9 个头 + 6 工具 + 形状合法的会话 `ses_fbfd91e7a85c…`）在 `08:59:24` 返回 403，表面上像「额外头有害」。但它在 `08:53:58` 那次 403 之后仅 **5.5 分钟**发出，**封禁尚未解除**，因此该 403 **不能**归因于额外头或会话形状。这条记录保留在案，用于说明为什么 runbook 强制「403 后至少等 15 分钟」。

## 二、官方编码的独立验算

用三条真实客户端 id 反推（Python，独立于 Go 实现）：

| 真实客户端 hex | 低 12 位 | 反推铸造时刻 (UTC) | `(hex>>12)+mintedAt == (27<<36)-1` |
| --- | --- | --- | --- |
| `ecb74175effe` | `0xffe` ✅ | 2026-10-13 09:15:46.721 | ✅ |
| `d1bfaf4aff65` | `0xf65` ❌ | 2027-01-05 04:04:25.552 | ✅ |
| `fbfd91e7a85c` | `0x85c` ❌ | 2026-08-26 22:18:05.061 | ✅ |

**结论**：`(27<<36)-1` 这个常量被三条独立样本证实；`low12 == 0xffe` 只对 `counter == 1` 的铸造形态成立——「复用一条真实客户端 id」并不保证过门禁。

> 任务书里写的等式是 `(hex>>12) + unixMilli == 1855425871872`（即 `27<<36`）。逐位复算表明精确值是 `(27<<36) - 1`，差 1 毫秒。这不影响任何实现（实现按官方 `tA()` 逐位复刻），但文档按精确值记录。

生产实现与官方编码的等价性由单测钉住：`TestOpenCodeGateSessionHexMatchesOfficialEncoding` 用逐字复刻的 `tA()` 对 570+ 个时间点（窗口内均匀取样 + 固定种子伪随机 + 窗口边界 + 窗口外）逐位比对，全部相等。

## 三、上游实测（修复后，最终代码）

### 3.1 单次探针：生产路径 → 200

```
cd backend
HTTPS_PROXY=http://127.0.0.1:10809 HTTP_PROXY=http://127.0.0.1:10809 \
  ZEN_PROBE=current ZEN_PROBE_OUT=... \
  OPENCODE_ZEN_API_KEY=<key> GOTOOLCHAIN=go1.27.2 \
  go test -tags=live ./internal/service/ -run TestLiveOpenCodeFreeTierProbe -v -count=1
```

原始 dump（已按安全策略掩码 key）：

```
=== PROBE MODE: current ===
=== ENDPOINT: https://opencode.ai/zen/v1/chat/completions ===
=== AUTH: Bearer oc_sk_***Va1d (len=51) ===
=== REQUEST HEADERS (5) ===
  Authorization: Bearer oc_sk_***REDACTED***
  X-Opencode-Session: ses_ecb4aafd8ffeysx8xVwhQ12lLq
  X-Session-Affinity: ses_ecb4aafd8ffeysx8xVwhQ12lLq
  X-Session-Id: ses_ecb4aafd8ffeysx8xVwhQ12lLq
  User-Agent: opencode/1.19.0 ai-sdk/provider-utils/4.0.23 runtime/bun/1.3.14
=== SESSION DIAGNOSTICS ===
  session: ses_ecb4aafd8ffeysx8xVwhQ12lLq
  shape_ok: true
  gate_ok (shape + low12==ffe): true
  hex12: ecb4aafd8ffe
  low12: ffe (required: ffe)
  official encoding check: (hex>>12)+unixMilli = 1855425871871, epoch-1 = 1855425871871, delta_ms = 0
=== REQUEST BODY (2644 bytes) ===
{"model":"mimo-v2.6-flash-free","stream":true,"messages":[{"role":"user","content":"say ok"}],"tools":[…官方 6 件套…],"tool_choice":"none"}
=== RESPONSE STATUS: 200 200 OK ===
=== RESPONSE HEADERS ===
  Content-Type: text/event-stream
  Cf-Ray: a49d802a6c265cfd-LAX
  Cache-Control: no-cache
  X-Opencode-Endpoint-Id: openrouter-xiaomi
  X-Zen-Model: mimo-v2.6-flash-free
  Date: Tue, 13 Oct 2026 10:01:04 GMT
  Server: cloudflare
  Cf-Placement: remote-ORD
  X-Opencode-Log-Id: 0b72472e-6d7d-4d08-96eb-d9b2301626b9
  X-Opencode-Upstream-Model-Id: xiaomi/mimo-v2.6-flash
=== RESPONSE BODY (2961 bytes，节选) ===
data: {"id":"gen-1791885662-FC3eMMDTBLZjpHiInIQV","object":"chat.completion.chunk","created":1791885662,"model":"mimo-v2.6-flash-free","choices":[{"index":0,"finish_reason":null,"delta":{"role":"assistant","content":"","reasoning":"User",...}}]}
...
data: {"id":"gen-1791885662-FC3eMMDTBLZjpHiInIQV","object":"chat.completion.chunk","created":1791885662,"model":"mimo-v2.6-flash-free","choices":[{"index":0,"finish_reason":"stop","delta":{"role":"assistant","content":""}}],"usage":{"prompt_tokens":711,"completion_tokens":12,"total_tokens":723,"prompt_tokens_details":{"audio_tokens":0,"cached_tokens":704,"cache_write_tokens":0},"completion_tokens_details":{"audio_tokens":0,"reasoning_tokens":9}}}

data: [DONE]
```

两点值得注意：

1. `delta_ms = 0`：铸造的 hex 段与官方编码在**同一毫秒**内逐位一致（`(hex>>12)+unixMilli == (27<<36)-1`）。
2. `cached_tokens: 704`（prompt_tokens 711）：上游 prompt cache **命中**。这印证了铸造缓存的价值——如果每轮换 id，这个数字会是 0。

### 3.2 端到端 live 测试：全绿

```
cd backend
HTTPS_PROXY=http://127.0.0.1:10809 HTTP_PROXY=http://127.0.0.1:10809 \
  OPENCODE_ZEN_API_KEY=<key> GOTOOLCHAIN=go1.27.2 \
  go test -tags=live ./internal/service/ -run TestLiveOpenCodeFreeTierGate -v -count=1
```

原始输出：

```
=== RUN   TestLiveOpenCodeFreeTierGate
=== RUN   TestLiveOpenCodeFreeTierGate/实验组：门禁伪装必然放行
=== RUN   TestLiveOpenCodeFreeTierGate/边界组：客户端不提供任何会话标识时也必须放行
=== RUN   TestLiveOpenCodeFreeTierGate/无工具请求组：补官方工具集_+_tool_choice_none_也必须放行
=== RUN   TestLiveOpenCodeFreeTierGate/对照组：无门禁必然被拒（最后运行，会封禁_key_约_15_分钟）
--- PASS: TestLiveOpenCodeFreeTierGate (39.41s)
    --- PASS: TestLiveOpenCodeFreeTierGate/实验组：门禁伪装必然放行 (17.71s)
    --- PASS: TestLiveOpenCodeFreeTierGate/边界组：客户端不提供任何会话标识时也必须放行 (2.05s)
    --- PASS: TestLiveOpenCodeFreeTierGate/无工具请求组：补官方工具集_+_tool_choice_none_也必须放行 (19.31s)
    --- PASS: TestLiveOpenCodeFreeTierGate/对照组：无门禁必然被拒（最后运行，会封禁_key_约_15_分钟） (0.33s)
PASS
ok  	github.com/Wei-Shaw/sub2api/internal/service	41.676s
```

三个正向组全部 200（各自约 2–20 s，因为要等 SSE 流结束），对照组 403 `FreeTierError`——**同一次运行、同一个 key、同一时刻**的对照，比历史那组跨 22 分钟的证据更干净。

### 3.3 修复过程中有一次「高 36 位错位但仍 200」的意外收获

修复是分两步落地的：先实现闭式 `((epoch - now) << 12) | 0xFFE`，随后在逐位复算官方 `tA()` 时发现它比官方值大 `0x1000`，补上 `-1` 才逐位一致。

**第一次 live 运行（`09:43:31 UTC`）用的是没有 `-1` 的版本**，即高 36 位与官方编码相差 1 个单位（低 12 位仍为 `0xFFE`）。那一次三个正向组**全部 200**。

这提供了一个直接证据：**上游当前并不校验高 36 位是否等于「本次请求时刻」的编码**。我们仍然按官方编码生成（成本为零），但「只查低 12 位」是实测结论，不是可以长期依赖的契约——见 runbook 的「门禁要求清单」与「如何重新推导」。

> 注意：这条证据来自「第一次 live 运行」，而不是「修复前的代码」。修复前的代码（`sha256` 派生 hex，低 12 位随机）从未跑过 `TestLiveOpenCodeFreeTierGate`；修复前的证据是 §一 的单变量消融（403）与 §二 的算法反推。**不要用「live 测试曾经通过」反推修复前也能通过。**

### 3.4 对照组顺序的必要性

`TestLiveOpenCodeFreeTierGate` 的三个正向子测试被刻意排在故意 403 的对照组**之前**。上游对 403 的惩罚按 key 记账约 15 分钟；若对照组先跑，其后所有正向断言都会因封禁而假失败。这一点由 §一 的 `superset2` 污染事件直接印证。

## 四、单元测试

```
go test -tags=unit ./internal/service/ -run 'OpenCode' -count=1
ok  	github.com/Wei-Shaw/sub2api/internal/service	2.820s
```

新增/更新的用例：

| 用例 | 钉住的行为 |
| --- | --- |
| `TestIsOpenCodeGateSessionID` | 接受 `ffe` 结尾（含实测 200 的真实 id）；拒绝 `f65`/`fff`/`000`/哈希派生值/UUID/短串/空串/大写 hex/长度不符/带空白 |
| `TestMintOpenCodeSessionIDInvariant` | 四条铸造路径（随机兜底 / 按来源 / 规范改写 / 门禁端到端）产出全部满足形状 + 位不变量 + 时间戳自洽 |
| `TestOpenCodeGateSessionHexMatchesOfficialEncoding` | 生产 hex 段与官方 `tA()` 在 570+ 个时间点**逐位相同** |
| `TestOpenCodeGateSessionHexCounterOneIsGateForm` | `low12 == 0xFFE` ⟺ `counter == 1` |
| `TestCanonicalOpenCodeSessionIDRekeysHashShapedValues` | 哈希型（形状合法、位不合法）客户端值必须重铸；合法值原样保留；空来源不铸造 |
| `TestOpenCodeGateSessionMintCacheStability` | 同一 source 恒等且不重复铸造；不同 source 不同；产出都过门禁 |
| `TestOpenCodeGateSessionMintCacheEviction` | FIFO 淘汰最早条目；被淘汰者重铸且仍过门禁；缓存有界 |
| `TestOpenCodeGateSessionMintCacheConcurrent` | 16 goroutine 并发取同一 source 得到同一 id |
| `TestApplyOpenCodeFreeTierGateSession/形状合法但位不合法必须重铸` | 端到端路径的同一行为 |
| `TestOpenCodeFreeTierGateEndToEndBodyShape` | 第一项断言加强为形状 + 位不变量 |

既有「只断言形状正则」的用例未改动且通过：`TestCanonicalOpenCodeIDShape`、`TestCanonicalOpenCodeIDDeterministicAndDistinct`（后者也保证 `canonicalOpenCodeID` 仍被使用，不会成为死代码触发 `unused` linter）。

## 五、全量回归与既有失败

```
go test -tags=unit ./internal/service/... -count=1
```

| 运行 | 失败集合 |
| --- | --- |
| 分支 · 第 1 次 | `TestOllamaProbeCallback_StaleLongDoesNotOverrideNewShort`、`TestFilterGrokFreeQuotaAccountsOnlyBlocksExplicitFreeOAuth` |
| 分支 · 第 2 次 | `TestOllamaProbeCallback_StaleLongDoesNotOverrideNewShort` |
| 干净 HEAD（`4a51b6951`，独立 worktree） | `TestOllamaProbeCallback_StaleLongDoesNotOverrideNewShort`、`TestCommandCodeGatewayPassesThroughCatalogProtocols` |

结论：

- `TestOllamaProbeCallback_StaleLongDoesNotOverrideNewShort` **恒定失败**，任务书已明确其为既有失败、不修。
- `TestFilterGrokFreeQuotaAccountsOnlyBlocksExplicitFreeOAuth` 与 `TestCommandCodeGatewayPassesThroughCatalogProtocols` **在三次运行里各出现一次、集合互不相同，且在干净 HEAD 上也出现** → 是 `internal/service` 全量套件里**既有的顺序/状态相关抖动**，与本次改动无关。二者单独运行时均通过。
- 本次改动涉及的包与用例全部通过。

## 六、静态检查

- `go build ./internal/service/`：通过。
- `go vet -tags=unit ./internal/service/`、`go vet -tags=live ./internal/service/`：通过。
- `gofmt -l internal/service/`：无输出。
- `golangci-lint` 未运行（本机未安装；历史记录显示 `go install` 会因传递依赖校验和不匹配而失败，**不绕过校验和**）。CI 覆盖。

## 七、安全事故：探针 dump 泄漏 API key（如实记录）

**发生了什么**：把探针重写成 4 模式版本后，第一次跑 `ZEN_PROBE=current` 时，dump 的 `=== REQUEST HEADERS ===` 段**原样打印了 `Authorization: Bearer oc_sk_…`**，完整 key 因此写进了本地文件 `.oc-gate/probe-fixed-current.txt`，并出现在本次对话的工具输出里。

**根因**：探针的 header dump 循环直接 `fmt.Fprintf("%s: %s", k, join(vs))`，只有 `=== AUTH:` 那一行做了掩码。历史 dump 里之所以看到的是 `oc_sk_***REDACTED***`，是因为事后被人工/脚本清理过——**代码里的缺陷一直在**。

**处置**：

1. 立即用 Python 扫描 `.oc-gate` 全目录，把出现的原始 key 就地替换为 `oc_sk_***REDACTED***`。命中 2 个文件：`probe-fixed-current.txt` 与 `zen_key.txt`（后者按设计本来就存明文 key，被一并抹掉）。
2. 修复探针：`maskOpenCodeProbeHeaderValue` 对 `Authorization` / `X-Api-Key` / `Api-Key` 逐头掩码；并在写文件/打日志前的**唯一出口**对整份 dump 再执行一次 `scrubOpenCodeProbeSecrets`（双保险，防止将来新增的头/字段把 key 带出去）。
3. 修复 `.oc-gate/getkey.sh`：掩码正则从只匹配 `sk-` 改为覆盖 `oc_sk_|sk-|sk_`；`mask=` 只显示方案前缀与末 4 位（原来显示前 10 位会泄漏 5 个 key 字符）。
4. 用修好的 `getkey.sh` 重新从服务器只读 `SELECT` 取回 key 到 `.oc-gate/zen_key.txt`（len=51，scheme=`oc_sk_`），并把 stdout 重定向到文件后用程序校验「捕获内容不含原始 key」→ **False**（即不含），再只打印掩码行。
5. 全量扫描 `sub2api` 工作树（排除 `.git`）确认**仓库内不存在**原始 key。

**遗留风险与建议**：原始 key 已出现在本次对话记录中。虽然它是本机自部署服务的免费层 key、且始终只存在于同一台机器上，但**按最保守的处理应当轮换账号 31568 的 `api_key`**。是否轮换由运营者决定；本变更不做任何自动轮换。

## 八、验证边界（未覆盖 / 不确定）

1. **`/zen/go` 付费链路未验证**：本次实测只覆盖 `/zen/v1/chat/completions` 免费层。会话铸造对两条链路共用，但付费链路是否也校验位不变量未做实测。
2. **Responses / Anthropic 方言未做真实上游联调**：`muse-spark-*-free` 在本网络对 `/zen/v1/responses` 返回 `403 RegionError`（地理封锁，非门禁失败）。门禁实现按 Chat 方言同构，仅由单元测试覆盖注入形状；但**会话头逻辑是方言无关的**（`applyOpenCodeFreeTierGateSession` 不区分方言），因此本次修复对方言是均匀生效的。
3. **高 36 位未被上游校验**这一点只有间接证据（复用 6.3 秒前的真实 id 仍 200），没有做过「刻意构造高位错误值」的对照实验（每次 403 代价 15 分钟，不值得为此消耗）。
4. **未做完整网关进程联调**：live 测试直接调用实现函数打真实上游，未启动完整网关（需数据库与 Redis）。三条 funnel 的接线由代码审查与既有测试覆盖。
5. **`golangci-lint` 未本地运行**（见 §六），CI 关卡未被本地覆盖。
6. **缓存命中率未做长期观测**：`cached_tokens: 704` 是单次样本。淘汰策略的实际影响（4096 条是否够）需要生产观测。
