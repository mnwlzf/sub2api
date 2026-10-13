# 设计：OpenCode 免费层门禁的会话位编码

## 1. 问题定义

门禁的第一项要求此前被记录为「会话头必须匹配 `^ses_[0-9a-f]{12}[0-9A-Za-z]{14}$`」（形状）。2026-10-11 之后这条要求升级为**形状 + 位不变量**：

> 12 位 hex 字段的**低 12 位必须等于 `0xFFE`**。

两者都满足才算合法。只满足形状的值——包括任何用哈希派生 hex 的实现（我们 2026-10-11 之前的算法、参考插件的 `canonicalID`）——会被拒，且返回与「完全没发会话头」相同的 `403 FreeTierError`。

这正是本次事故的隐蔽之处：**失败信息不指向真正的原因**，症状与「形状不对」「头没发出去」完全重合。

## 2. 官方编码算法与等价化简

### 2.1 挖出的源码

opencode 1.19.0 发布产物（`opencode-linux-x64.tar.gz` 解包后的二进制）偏移 `105055473`：

```js
var LG={};u(LG,{descending:()=>oA,create:()=>tA,ascending:()=>u_});
var x4=0,cA=0;
function u_(){return tA(!1)}
function oA(){return tA(!0)}
function tA(_,Y=Date.now()){
  if(Y!==x4)x4=Y,cA=0;
  cA++;
  let $=BigInt(Y)*0x1000n+BigInt(cA),
      U=_?~$:$,
      A=Array.from({length:6},(Z,W)=>Number(U>>BigInt(40-8*W)&0xffn).toString(16).padStart(2,"0")).join(""),
      X=crypto.getRandomValues(new Uint8Array(14));
  return A+Array.from(X,(Z)=>"0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"[Z%62]).join("")
}
```

偏移 `105075250` 是 Session ID 的工厂：

```js
var B_=D.String.check(D.isStartsWith("ses")).pipe(D.brand("SessionID"),
  W_((_)=>{let Y=()=>_.make("ses_"+oA());return{create:Y,descending:($)=>$===void 0?Y():_.make($)}}));
```

即 `ses_` + `oA()`，走 `descending = true`。

### 2.2 语义

- `$ = now*0x1000 + counter`，`counter` 在毫秒变化时归零再自增，**同一毫秒内的第一个 id 的 `counter` 恒为 1**。
- `U = ~$`（`descending`）。`~` 在 BigInt 上是无限精度补码，取低 48 位即 12 位 hex。
- 后缀 14 位是 `crypto.getRandomValues(14 字节)` 的 base62，与 hex 段无关。

### 2.3 关键推论：`counter == 1` ⟺ `low12 == 0xFFE`

`counter` 每递增 1，`hex` 的低 12 位就减 1：

| counter | low12 |
| --- | --- |
| 1 | `0xFFE` |
| 2 | `0xFFD` |
| 3 | `0xFFC` |
| 4096 | `0xFFF` |

所以门禁查 `low12 == 0xFFE`，本质上是在查「**这个 id 是官方客户端在某个毫秒里铸造的第一个 id**」。随机/哈希派生的 hex 只有 `1/4096` 的概率命中。

### 2.4 逐位等价的闭式

官方 `counter == 1` 时：

```
hex_official = (2^48 - 2 - (now mod 2^36) * 0x1000) mod 2^48
```

生产实现：

```
hex_ours = ((epoch - 1 - now) << 12) | 0xFFE     ，epoch = 27 << 36
```

两者恒等，对**任意** `now` 成立（模 `2^48`）。证明：`(epoch-1)*0x1000 = (27*2^36 - 1)*4096 = 27*2^48 - 4096 ≡ -4096 (mod 2^48)`，故

```
hex_ours = (-now*0x1000 - 4096 + 4094) mod 2^48 = (-now*0x1000 - 2) mod 2^48 = hex_official
```

单元测试 `TestOpenCodeGateSessionHexMatchesOfficialEncoding` 用官方算法（`officialOpenCodeSessionHexForTest`，逐字复刻 `tA()`）对 **570 余个时间点**（窗口内均匀取样 + 固定种子伪随机 + 窗口边界 + 窗口外）逐位比对，钉住这条等价性。

> **为什么必须写 `-1`**：直接用 `epoch` 会得到比官方值大 `0x1000` 的 hex。门禁当前只读低 12 位，两者都能过（这也是事故期有人用 `((epoch-now)<<12)|0xFFE` 就测出 200 的原因）；但保持逐位一致让「与官方编码等价」成为可证命题，也避免门禁将来收紧到高 36 位时再踩一次。

### 2.5 高 36 位是否被校验

当前**没有证据**表明上游校验高 36 位：实测中复用一个 6.3 秒前由真实客户端铸造的 id（`ses_ecb74175effe…`）同样返回 200，说明高 36 位并不要求等于「本次请求时刻」的编码。

我们仍然按官方编码生成高 36 位——成本为零，且能抵御「上游开始校验时间窗」这一最可能的下一步收紧。**不把「只查低 12 位」当作可以依赖的契约。**

### 2.6 自洽校验等式

```
(hex >> 12) + mintedAtMillis == (27 << 36) - 1
```

用三条真实客户端 id 反推，等式成立且低 12 位均为 `0xFFE`：

| 真实客户端 hex | 反推铸造时刻 (UTC) |
| --- | --- |
| `ecb74175effe` | 2026-10-13 09:15:46.721 |
| `d1bfaf4aff65` | 2027-01-05 04:04:25.552（低 12 位 `f65`，非门禁形态） |
| `fbfd91e7a85c` | 2026-08-26 22:18:05.061（低 12 位 `85c`，非门禁形态） |

**注意**：`d1bfaf4aff65` 与 `fbfd91e7a85c` 也是「真实客户端 id」，但它们**不是门禁合法值**——它们由官方客户端的**非首次**铸造路径产生（`counter > 1`），或来自 `create`（升序）分支。这解释了为什么「复用真实客户端 id」并不能保证过门禁：**只有 `counter == 1` 的形态才行**。

## 3. 缓存策略

### 3.1 为什么必须有缓存

官方编码把**当前时间**编进 hex 段。逐次铸造必然得到不同值——而会话 id 每轮变化会让上游 prompt cache 永不命中（每轮全量重算，成本与延迟都显著上升）。旧的 `sha256(source)` 派生虽然位不合法，但至少是**确定性**的；换成官方编码后这个性质必须由缓存补回来。

### 3.2 设计与淘汰

```go
type openCodeGateSessionMintCache struct {
    mu    sync.Mutex
    max   int
    ids   map[string]string   // source → id
    order []string            // FIFO 插入顺序
}
```

- **容量**：`openCodeGateSessionMintCacheSize = 4096`。远大于典型并发会话数。
- **淘汰**：FIFO（先进先出）。选 FIFO 而非 LRU，是因为状态完全在锁内、实现与推理都最简单；LRU 需要额外链表且对「长时间不活跃但会回来的会话」同样会淘汰，收益不明确。
- **并发**：`getOrMint` 在锁内完成「查 + 铸造 + 写入」，保证并发下同一 `source` 只铸造一次（否则并发抖动同样会击穿 prompt cache）。
- **淘汰后果**：该会话下一轮拿到新 id——**仍然满足门禁**（新 id 同样按官方编码铸造），只是损失一次 prompt cache 命中。**不构成正确性问题**，这是可以接受 FIFO 的根本原因。
- **后缀的确定性**：后缀复用 `canonicalOpenCodeID("ses", source)` 的 14 位派生，因此即便条目被淘汰或进程重启，同一 `source` 的后缀也不变；变的只有 hex 段的时间戳。

### 3.3 缓存 key 的选择

key 是**规范化之前的原始 `source`**（客户端会话头 / `prompt_cache_key` / `metadata.user_id` / 内容派生种子）。这与改动前的确定性派生使用同一输入，因此「同一会话跨轮次稳定」的既有语义完全保持。

## 4. 为什么必须重铸「哈希型客户端值」

改动前的逻辑是：**只要匹配形状正则就原样透传**。当时的理由是正确的——重新铸造会破坏调用方自建的会话连续性。但在位不变量出现之后，这条规则变成了缺陷：

- 任何用 `sha256` 派生 hex 的实现（参考插件 `cpa-plugin-opencodezen` 的 `canonicalID`、我们自己的旧算法、以及可能的第三方客户端）产出的 `ses_` 值**形状合法、位不合法**。
- 原样透传 = 把上游的 `403 FreeTierError` 直接带进请求。

因此规则改为：**形状与位不变量都满足才透传，否则重铸**。重铸仍然走缓存，所以「调用方自建会话连续性」在 source 稳定的前提下依然成立（source 就是客户端送来的那个值）。

代价：如果调用方自己也在维护会话 id 语义（例如它把 `ses_x` 映射到自己的会话表），重铸会让上游看到的 id 与调用方的不一致。但上游只把会话值当作 affinity / cache key，不要求它可反查；而**403 是硬失败**。两害相权取重铸。

## 5. 为什么不做那些「非必需」的头

已验证非必需（200 那次一个都没发）：

| 头 | 结论 |
| --- | --- |
| `X-Opencode-Request` | 非必需（缺失仍 200） |
| `X-Opencode-Client` | 非必需 |
| `X-Opencode-Project` | 非必需 |
| `Accept` | 非必需 |
| `X-Session-Affinity` | 非必需（门禁不校验；我们仍然镜像，仅为形态对齐） |
| `X-Session-Id` | 非必需（同上） |

**不为了「保险」而补**。理由不是懒：每多一个与官方客户端不一致的头，就多一份「上游按头组合收紧规则」时的暴露面；而且会让「最小必需信号集」这个排查结论失效——下次再出问题时，无法从「我们发了什么」反推「上游要什么」。代码里已有的消融注释是正确的，保留。

> 实测中有一个**被污染的反例**值得记录：`superset2`（9 个头 + 6 工具 + 形状合法的会话）在 `08:59:24` 返回 403，看起来像「额外头有害」。但它在 `08:53:58` 那次 403 之后仅 5.5 分钟就发出，**封禁未解除**——所以那次 403 不能归因于额外头。排查时必须核对每次响应的 `Date` 头与上一次 403 的间隔，见 runbook。

## 6. UA 版本不是门禁检查项

`openai_opencode_session.go` 的原注释宣称「对齐到 1.19.0 是本次修复」。**这是错的**，已改写为事实：

- UA 只要求是 `opencode/<version>` 形态且不低于版本下限（`1.17.9` → `426 UpgradeRequired`，`1.18.0` → 200）。
- 实测插件 UA `opencode/1.18.31 …` 与 `opencode/1.19.0 …` 同样 200，**版本号不参与门禁判定**。
- 本次 403 的真实根因是会话位编码。
- UA 取值保持 `opencode/1.19.0 ai-sdk/provider-utils/4.0.23 runtime/bun/1.3.14`（保持最新无害；子版本从发布产物实测取得）。

保留这条结论的意义：**下一次同类故障不要先去动 UA**。runbook 的「症状分流」把 426 与 403 明确分开。

## 7. 兜底路径

`applyOpenCodeFreeTierGateSession` 的派生顺序不变，只把最后的「随机 UUID 铸造」换成按官方编码铸造：

| 优先级 | 来源 | 稳定性 |
| --- | --- | --- |
| 1 | 已落地的会话头（合法则透传，否则重铸） | 客户端决定 |
| 2 | 请求体 `prompt_cache_key` / `metadata.user_id` | 跨轮次稳定（走缓存） |
| 3 | 模型名 + 首条用户消息（内容派生） | 跨轮次稳定（走缓存） |
| 4 | `mintOpenCodeSessionID()` 随机兜底 | 每轮变化，仅保证能过门禁 |

## 8. 风险与缓解

| 风险 | 缓解 |
| --- | --- |
| 上游再次收紧门禁（例如开始校验高 36 位时间窗） | 高 36 位已按官方编码逐位生成；runbook 记录了重新推导算法的完整方法 |
| 缓存被淘汰导致 prompt cache 击穿 | 容量 4096；淘汰只损失一次命中，不影响通过性；后缀保持确定性 |
| 重铸破坏调用方会话连续性 | 仅当客户端值位不合法时发生；source 稳定时缓存保证跨轮次一致；403 是硬失败，优先级更高 |
| 上游插件送来的 `ses_` 值再次被拒 | `isOpenCodeGateSessionID` 统一收口，任何调用点都不可能绕过 |
| 排查工具泄漏 API key | 探针 dump 对 `Authorization` 逐头掩码 + 写文件前整份 dump 再抹一遍原始 key（双保险）；`getkey.sh` 的掩码正则已覆盖 `oc_sk_`/`sk-`/`sk_` |
| 误把 429 当成门禁问题 | runbook 的症状分流表把 `403 FreeTierError` / `429 FreeUsageLimitError` / `426 UpgradeRequired` 三者分开 |
| 连续试探触发 15 分钟封禁 | runbook 强制「一次只改一个变量、403 后至少等 15 分钟」；live 测试的负向对照组排在最后 |

## 9. 非目标

- 不改动 `billing_service.go` 的任何定价逻辑；`jev-1.13-free` 目录条目有意保留。
- 不新增任何「非必需」头。
- 不改变工具补齐范围与 `tool_choice` 方言约束。
- 不做额度/配额管理（属 429 与 WARP 轮换的范畴）。
- 不把会话缓存外置（Redis 等）——进程内缓存已足够，外置会引入新的失败模式。
