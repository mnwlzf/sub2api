# Runbook：OpenCode 免费层门禁排查手册

> **适用对象**：Sub2API 的 OpenCode Zen 免费层（模型 ID 以 `-free` 结尾）突然大面积失败时的定位与修复。
> **目标**：让未来的人（或未来的你）在 **半小时内** 判断「是门禁又变了 / 是配额 / 是 UA / 是别的东西」，并知道怎么重新推导出新规则。
> **最后校准**：2026-10-15，opencode UA 版本带 **`[1.18.0, 1.18.33]`**（上游 2026-10-14 收紧，官方最新 1.19.0 在带外）。**门禁规则会变，本手册的结论必须带日期读。**

---

## 0. 三十秒决策树

```
免费层请求失败
├─ HTTP 403 + body 含 "FreeTierError"
│   ├─ ⭐ 第 0 步：核对 UA 版本是否落在当前带内 ──► 改一个版本号就能复现/消除（§2.4）
│   ├─ 第 1 步：核对凭据是否为受控账号 key（`oc_sk_…`；`public` 会 403）
│   ├─ 第 2 步：核对会话 id 是否为完整官方 tA() 编码（§5.1 信号 1）
│   └─ 以上都查过 ──► 门禁身份不通过 → 走 §2 单变量消融
├─ HTTP 429 + body 含 "FreeUsageLimitError" ──► 出口 IP 配额耗尽（另一回事）→ §3
│   └─ 且 opencode_warp_rotate 事件 reason=failed / detail 含 probe_error / attempts=0
│        ──► 轮换服务自己的探测请求也没过门禁 → §3.1（先修这个，否则轮换 100% 失败）
├─ HTTP 426 + "UpgradeRequired" ──► UA 版本低于下界 1.18.0 → §1.3
├─ HTTP 400 + "encrypted_content … was not issued to this caller"
│   ──► 回放的加密推理块不属于当前账号（多账号 failover）→ §1.5（已有自愈，仅需确认是否触发）
└─ 其它（400 / 403 RegionError / 500）──► §1.4
```

**先做这一步**：把失败响应的 `date`、`cf-ray`、`x-opencode-log-id` 三个头抄下来。`x-opencode-log-id` 是与 opencode 对账时的唯一凭据；`date` 是给数据点排序、判断时序的唯一依据。

---

## 1. 症状分流

### 1.1 `403 FreeTierError` —— 门禁身份不通过

```json
{"type":"error","error":{"type":"FreeTierError",
 "message":"OpenCode's free tier can only be used from within OpenCode. Download it at https://opencode.ai and keep building."}}
```

含义：**上游认为这次请求不是官方 OpenCode 客户端发的。**

这条错误信息**没有指向任何具体缺失的信号**。会话头形状不对、会话编码不合法（低 12 位不是 `0xFFE`，或高 36 位反解不出合法铸造时刻）、**UA 版本落在当前带外**、缺 `bash`/`read`、`stream:false`、凭据不是受控账号 key、完全没发会话头——**全部**返回这同一段文本。所以必须用消融法（§2）而不是猜。

**⚠️ 最快的判别法：先核对 UA 版本（§2.4）。** 2026-10-14 的生产阻塞就是 UA 版本在带外造成的——把 UA 版本号一改就能复现/消除，不需要碰会话 id、body 或头。

### 1.2 ⚠️ 403 与「封 key 15 分钟」——**因果已被证伪，只保留为操作纪律**

**先说结论：403 不会封 key、不会封 IP。** 2026-10-15 的受控实测（同一 key、同一出口、间隔 16–33 秒）连续推翻了这条因果：

| 403 时刻 (UTC) | 紧接着的 200 时刻 | 间隔 |
| --- | --- | --- |
| 01:46:44 | 01:47:17 | **33 s** |
| 01:48:20 | 01:48:41 | **21 s** |
| 01:49:02 | 01:49:23 | **21 s** |
| 01:54:03 | 01:54:24 | **21 s** |

一次完整的 24 请求二分（含 8 个 403）全程无 429、无超时、无 5xx，也没有任何「后续请求被无差别拒绝」的现象。所以：

- **不要再用「封禁未解除」解释一个意外的 403。** 一个 403 之后几秒就能拿到 200。
- 2026-10-13 那次「`superset2` 在 5.5 分钟后仍 403」的记录，**不能**再归因于封禁窗口——更可能是当次请求本身仍有真实缺陷（当时会话编码尚未收紧，见 §5.1）。
- 确实存在**上游侧的瞬态窗口**：2026-10-14 20:03–00:41 探测连续 403（连 `1.18.0` 也被拒），这是上游切换版本带时的过渡态，与「封 key」不是一回事。这类瞬态**以 2–3 小时为量级**，不是 15 分钟。

**保留 15 分钟只是操作纪律**（不是因果结论）：

- 排查时**每条记录都写 `date` 头**，并计算与上一次 403 的间隔；间隔 < 15 分钟的记录**视为弱证据**（因为不能排除上游瞬态），但**已经不需要为了「解封」而空等**。
- 把「故意触发 403」的用例（负向对照组）**排在最后**——理由是它浪费请求配额、且会把 403 混进数据点，不是为了等解封。
- 若连续 403 跨越 **2 小时以上**且期间换过 UA 版本、凭据、会话编码仍全部 403，则按「上游瞬态窗口」处理：**不要再消耗请求去试探**，记录时间窗后等 2–3 小时复测（§6.5）。

### 1.3 `426 UpgradeRequired` / `403` —— **UA 版本带**（上下界各一条独立检查）

含义：`User-Agent` 里的 `opencode/<version>` **不在上游当前接受的闭区间内**。两个方向各有一条独立检查：

```
version <  1.18.0   -> 426 UpgradeRequired
1.18.0 <= version <= 1.18.33  -> 放行（200）
version >= 1.18.34  -> 403 FreeTierError     ← 注意：是 403，不是 426！
```

实测（下界 2026-09-30 / 上界 **2026-10-15 二分确定**，同一 key / body / 出口，只改 UA 版本号）：

| UA 版本 | HTTP | 备注 |
| --- | --- | --- |
| `1.17.9` | **426** | `OpenCode 1.18.0 or newer is required` |
| `1.18.0` / `1.18.1` / `1.18.31` / `1.18.32` / `1.18.33` | **200** | 带内 |
| `1.18.34` / `1.18.35` | **403** `FreeTierError` | 带外（上侧） |
| `1.19.0` / `1.20.0` | **403** `FreeTierError` | 带外（上侧），`1.19.0` 是官方最新 release |

> ⚠️ **「UA 版本号不是门禁检查项」这条旧结论已被 2026-10-15 实测证伪**（本手册 2026-10-14 版本这么写过）。
> 版本号**是**硬性检查项，而且上界会把**官方最新版**拒之门外。

**关键事实**：

- **只有版本号起作用。** UA 的 `ai-sdk/provider-utils/… runtime/bun/…` 后缀对判定**无影响**：裸 `opencode/1.18.0` 与 `opencode/1.18.0 ai-sdk/…bun/1.3.14` 同样 200；裸 `opencode/1.19.0` 与带后缀同样 403。
- **上界是上游 2026-10-14 13:45–13:53 UTC 收紧版本带时钉下的**，起因不明（推测钉在某个旧客户端 build 上）。官方最新 `1.19.0` 客户端**自己也会被拒** → 这是**上游回归**，不是我们的用法错误。
- **`1.19.0` 曾经可用**：2026-10-13 我们把 UA 从 `1.18.31` 升到 `1.19.0`，当时无害；次日上游收紧后它就落到带外，导致全站 403。见 §7「教训」。
- **⚠️ 上界会变。** 每次改动 UA 版本号之前，**必须**按 §2.4 重测当前带，并同步更新 §5.1 信号 2、`openCodeUpstreamUserAgent` 的注释与 `TestOpenCodeUpstreamUserAgentVersionBand` 的 `openCodeUAVersionBandLow/High`。

### 1.4 其它错误

| 症状 | 含义 | 处理 |
| --- | --- | --- |
| `429` + `FreeUsageLimitError` | 免费层配额耗尽，**按出口 IP 记账**，每日 `00:00 UTC` 重置 | §3 |
| `400` + ``only `"auto"` is supported for `tool_choice` `` | `/responses` 端点只接受 `tool_choice:"auto"` | 别在 Responses/Anthropic 方言注入 `tool_choice` |
| `400` + `Recursive JSON schemas are not currently supported` | `/responses` 拒绝带 `$ref` 等 JSON Schema 引用关键字的工具 | 查客户端带来的工具 schema（门禁注入的是扁平 schema） |
| `403` + `RegionError` | 地理封锁（如 `muse-spark-*` 在某些网络） | 换出口或换模型；**不是门禁失败** |
| `400` + ``reasoning `encrypted_content` was not issued to this caller`` | 回放的加密推理块**由别的账号签发**（多账号 failover 造成），不属于当前 caller | §1.5（已有自愈：丢加密项 + 重试一次） |
| `500` | 上游模型侧故障（如 `jev-1.13-free` 在 chat/responses 均 500） | 换模型 |

### 1.5 `400` + ``reasoning `encrypted_content` was not issued to this caller`` —— 跨账号回放的加密推理块

```json
{"error":{"code":null,"message":"reasoning `encrypted_content` was not issued to this caller","param":null,"type":"invalid_request_error"},"model":"muse-spark-1.3-contributor-free"}
```

**含义**：请求体里回放的**加密推理块**（Responses `input` 中 `type:"reasoning"` / `type:"compaction"` 的 `encrypted_content`）
是由**另一个账号**（另一把上游 API key）签发的。上游把密文绑定到签发它的 caller，本账号重放别人的密文 → 整条请求被 400 拒绝。

**成因**：sub2api 对同一把客户端 key 做**多账号 failover / 负载均衡**。同一条会话的多个回合可能先后落在不同账号上，
而客户端（尤其是 **compact** 时重放整段历史）会把**所有**历史密文一起发上来，必然混有非当前账号签发的块。
生产实例：`account_id=31589`（`opencode-lnyocly2`）在 2026-10-15 11:07:48 收到该 400（`body_bytes=3131111`），
而该会话此前 10-14 19:2x–10-15 10:42 的成功回合**全在 `31568`**（`opencode-lnyocly`）。

**⚠️ 关键点：`code` 为 `null`。** 上游这条报错**不带错误码**，只有消息。

**现有自愈机制**（`backend/internal/service/openai_gateway_forward.go`，`Forward()` 的 HTTP/非 WSv2 路径）：

- 判据变量 `invalidEncryptedContentError`，命中后走 1137 行起的恢复分支；
- 恢复动作 = `trimOpenAIEncryptedReasoningItems(decoded)`：
  - `type:"reasoning"` → 只删 `encrypted_content` 字段（保留 `id` / `summary`），顺带删 `content: null`；
  - `type:"compaction"` / `"compaction_summary"` → 连整项一起删；
  - 删完只剩 1 个键的项 → 整项丢弃；
  - 若删空了 `input` → 删掉 `input` 键。
- 然后重新序列化 → `continue` 重试**一次**，`httpInvalidEncryptedContentRetryTried` 保证「只重试一次」，并记 lineage。
- **守卫**：`trimOpenAIEncryptedReasoningItems` 返回 `false`（没有可删的加密推理项）→ **不重试**，只打日志
  `[OpenAI] Skip non-WSv2 invalid_encrypted_content retry because encrypted reasoning items are missing`。

**为什么原来不触发**：原判据只认 `error.code` —— `upstreamCode == "invalid_encrypted_content"`，
或 `upstreamCode == "thinking_signature_invalid" && …`。这条报错的 `code` 是 `null`
（`extractUpstreamErrorCode` 读 `error.code`，null → 空串），两个分支都不匹配 → 自愈被跳过，客户端直接收到 400。
修复（2026-10-15）把消息判据 `encrypted_content` + `was not issued to this caller` 加进同一个
`invalidEncryptedContentError`，**复用**同一套丢弃 + 重试逻辑，不新增动作。

**判别法（确认是否真的是这个问题）**：

1. 报错文本：`ops_error_logs.upstream_error_detail LIKE '%was not issued to this caller%'`，
   且 `error_phase='upstream'`、`upstream_status_code=400`；`upstream_errors` 里 `error.code` 为 `null`。
2. **看该会话前后尝试的 `account_id` 是否不同**（这是本问题的确诊依据）：

   ```sql
   -- ① 失败回合的账号
   SELECT to_char(created_at,'MM-DD HH24:MI:SS') AS t, account_id, model, upstream_status_code
   FROM ops_error_logs
   WHERE upstream_error_detail LIKE '%was not issued to this caller%'
   ORDER BY created_at DESC;

   -- ② 同一 user/key 在相近时间的成功回合落在哪个账号
   SELECT to_char(created_at,'MM-DD HH24:MI:SS') AS t, account_id, model
   FROM usage_logs
   WHERE user_id = <uid> AND model = 'muse-spark-1.3-contributor-free'
   ORDER BY created_at DESC LIMIT 20;
   ```

   若最近的成功回合与失败回合**分属不同 `account_id`** → 确诊跨账号密文。
3. 对照容器日志：`openai.account_selected` 给出本次 `account_id`；
   若**没有** `Retrying non-WSv2 request once after invalid_encrypted_content` → 说明自愈确实没触发（修复前即如此）。

**处置**：

- **单次偶发**：无需人工干预。自愈重试会丢掉密文重发（代价：该轮丢失加密推理上下文，但仍能出结果）。
- **某账号长期只失败不成功**（如 `31589` 在 30h 内 0 次成功）：考虑把它从该分组摘掉，或核对它的 key / 出口。
- ⚠️ 该报错**不封 key**（与 §1.2 的 403 同理，已受控实测）；但仍要克制请求数。

**已知未覆盖（本次有意留白）**：WSv2 路径的错误分类 `classifyOpenAIWSErrorEventFromRaw`
（`openai_ws_forwarder_support.go`）只匹配 `invalid_encrypted_content` 与
`encrypted content` + `could not be verified`，同样**不匹配**这条消息。
本次实测的 8 次失败全部走 **HTTP** 路径（容器日志 `openai routing decision` → `"transport": "http"`），
故本次只修 HTTP 路径。若将来在 WS 路径看到同款 400，需要在 `classifyOpenAIWSErrorEventFromRaw` 补同样的消息判据。

---

## 2. 排查步骤：单变量消融法

### 2.1 原则

1. **固定一切，一次只改一个变量。** 头、UA、body 逐字节相同；只改你要验证的那一项。
2. **先测「超集」再收窄。** 先构造一个「所有可能信号都给上」的请求（应 200），再逐个删减，删到哪个变 403，哪个就是必需项。
3. **每个数据点都要有正反两侧。** 只测「加了 X 就 200」不够，还要测「去掉 X 就 403」——否则无法排除偶然。
4. **⭐ 先核对 UA 版本是否落在当前带内**（§2.4）——这是最快的判别法，成本只有一次改版本号的对照。
5. **记录原始响应头与响应体**，不要只记状态码。`cf-ray` / `x-opencode-log-id` / `date` 是事后复盘的全部依据。
6. **数据点之间留 10–20 秒即可**（403 不封 key，§1.2）；但每条记录都要写 `date`，间隔 < 15 分钟的在归因时按「弱证据」处理。

### 2.2 具体流程

```
第 0 步  ⭐ 核对 UA 版本是否落在当前带内（§2.4）
         ── 同一 key / body / 出口，只改 UA 版本号发两次：
            当前常量 vs 带内已知好版本（1.18.31）。
            改一下就 200 → UA 版本带就是根因，改完收工。
            两者都 403 → 继续往下。

第 1 步  确认对照用的 key 是受控账号 key（§5.1 信号 5）
         ── 用字面量 public 会 403；必须用与生产转发相同的 oc_sk_ key。
            同时发一次「我们认为是正确」的请求（探针 current 模式）。
            200 → 继续；403 → 继续第 2 步（不要空等 15 分钟）。

第 2 步  确认门禁仍然生效
         ── 发一次完全不伪装的裸请求（探针 nogate 模式）。必须 403 FreeTierError。
            若返回 200 → 上游已放宽门禁，本特性需要重新评估。
            ⚠️ 这一步会浪费一次请求并把 403 混进数据点，所以放在最后做。

第 3 步  定位是哪一项信号失效
         ── 从「全绿基线」出发，逐项破坏，看哪一项破坏后仍然 200。
            仍然 200 的项 = 非必需；变 403 的项 = 必需。
            ⚠️ 每次破坏都会烧一次请求，所以：
              · 优先测「我们怀疑变了」的那一项
              · 若连续 403 跨越 2 小时以上，按上游瞬态窗口处理（§1.2），停止试探

第 4 步  若发现某个值被拒，反推它的构造规则
         ── 见 §5。门禁值往往由官方客户端的算法决定，
            从发布产物里挖比反复试探快得多。
```

### 2.3 本仓库现成的消融工具

见 §4。`ZEN_PROBE=current` 是「全绿基线」，`shapeonly` / `nogate` 是负向对照。

### 2.4 ⭐ 排查顺序：**先核对 UA 版本是否落在当前带内**

拿到 `403 FreeTierError` 时，**第一件事不是挖会话编码、不是怀疑头、不是怀疑 body**，而是核对 UA 版本：

```bash
# 1. 看我们的出站 UA 版本是多少
grep -n 'openCodeUpstreamUserAgent =' \
  sub2api/backend/internal/service/openai_opencode_session.go
#   期望：opencode/<version>，且 version 落在 [1.18.0, 1.18.33]（上界会变，见 §5.1 信号 2）

# 2. 单变量对照：同一 key / body / 出口 / 会话铸造法，只改 UA 版本号发两次
#    （服务器侧复用 .oc-gate/bisect2/harness.py 的 zen_probe/post，间隔 10–20 s）
#      旧版本（当前常量）   -> 403 ?
#      带内已知好版本 1.18.31 -> 200 ?
```

**判读**：

| 现象 | 结论 |
| --- | --- |
| 改成 `1.18.31` 后 200，改回当前常量后 403 | **UA 版本带**就是根因 → 改常量 + 注释 + 单测断言（§5.1 信号 2） |
| 两者都 403 | 不是（只）UA 版本问题 → 继续查凭据（§5.1 信号 5）、会话编码（§5.1 信号 1），随后才查头/body |
| 两者都 200 | 生产 403 不来自这条链路 → 查是不是别的出站组件发的请求（§3.1 的教训：探测与转发可能用不同的 UA） |

**为什么排第一**：这是唯一「改一个版本号就能复现/消除」的单变量——成本最低、判别力最强。2026-10-14 的生产阻塞里，会话编码、头、body 全部被逐一排除后才发现真因是 UA 版本带；把顺序倒过来可以省掉整轮排查。

**注意**：若对照用 `1.18.0`，它与带内其他版本在门禁上等价，但离下界太近（上游若抬高下限会立刻断）。生产常量选带内**中段**（当前 `1.18.31`）。

---

## 3. `429 FreeUsageLimitError` —— 配额，不是门禁

```json
{"type":"error","error":{"type":"FreeUsageLimitError", ...}}
```

关键事实（来自 `/opt/oc-probe/` 的实测，2026-10-09）：

- **200 响应不带任何 `x-ratelimit-*`，也不带 `retry-after`** ——无法从成功响应读出剩余额度。
- **429 响应带 `retry-after`**，它指向一个**绝对 UTC 时刻**，实测恰好是 **`00:00:00 UTC`**（每日重置）。
- 配额**按出口 IP 记账**。同一次运行里，同一个 key、同一个 body、同一个会话头形态，仅换出口 IP，结果就在 200/429 之间分裂 → 决定因素是**出口 IP**，不是 key。
- `retry-after` 是**固定截止时刻的倒计时**，不是滑动窗口（50 秒后重测，`retry-after` 减少了 50 秒，指向同一时刻）。
- `accounts.retry_after_seconds` 在 Postgres 里**永远是 NULL**：sub2api 从不持久化响应头。所以不要指望从数据库读它。

**处理**：轮换出口 IP，或等到 `00:00 UTC`。本仓库有 `opencode-warp-rotate-on-429` 能力（429 时先换 WARP 出口再用同一账号重试）。**若轮换 100% 失败**（`attempts=0` + `probe_error`），那是轮换服务自己的探测请求被门禁拒了 → 见 §3.1。

**与门禁的区别**：429 是配额；403 `FreeTierError` 是身份。**429 不会封禁 key，403 同样不封 key、不封 IP**（2026-10-15 实测，§1.2）——两者的差别是「上游配额判决」vs「客户端身份不合格」，不是「会不会被封」。

---

## 3.1 ⭐ 轮换服务的探测请求同样受门禁约束（2026-10-14 生产阻塞）

**这是「429 处置链路自身被门禁打死」的故障形态**：症状看起来像「WARP 轮换服务坏了」，根因仍在门禁。

**完整因果链**：

```
出口 IP 当日免费额度耗尽 → 上游 429 FreeUsageLimitError
  → sub2api 三重闸门命中（真的触发了，2026-10-14 触发 36 次）
  → 调 POST http://172.19.0.1:9110/ensure-fresh
  → 轮换服务先探测当前出口，用的是「随机 hex 会话 id」→ 403 FreeTierError
  → 轮换服务把 403 归类为 other → 返回 503 probe_error、attempts=0 → 不轮换
  → failover 耗尽 → 客户端看到 sub2api 合成的 429
```

**症状（怎么认出来）**：

| 观察点 | 值 |
| --- | --- |
| ops 事件 `opencode_warp_rotate` | `reason=failed`，detail 含 `probe_error` |
| 同事件的 attempts | **`attempts=0`**（一次都没轮换） |
| 轮换失败率 | **100%**（不是偶发） |
| `/var/log/warp-rotate-api.log` | `step=probe http=403 status=other` |
| 客户端 | `rate_limit_error` / `Upstream rate limit exceeded, please retry later` |

⚠️ 最后一行是**误导源**：这段文案是 **sub2api 自己合成的**（failover 耗尽后 `mapUpstreamError(429)`），上游原型是 `FreeUsageLimitError`（**配额**，不是速率限制）。按「速率限制」去调参永远查不到根因。

**与「正常失败」的区分（关键）**：

| probe 行 | 含义 | 处置 |
| --- | --- | --- |
| `probe http=429 status=exhausted` | **正常**：门禁已通过，拿到真实配额判决 → 该轮换 | 走轮换流程，不用管 |
| `probe http=403 status=other` | **门禁失配**：探测请求没过门禁 | 修 `new_session()`，见下 |
| `probe http=200 status=ok` | 出口可用 | 无需轮换 |

只要看到 `attempts=0` + `http=403`，就不要再怀疑 WARP / 网络 / 配额，直接查门禁。

**诊断步骤**：

1. 看 `attempts`：`0` = 轮换服务连第一次探测都没过 → 门禁失配（**不是**「换了 6 个出口都不可用」，那会是 `attempts=6`）。
2. 看 probe 状态码：`403` = 门禁；`429` = 配额；`5xx` = 上游故障。
3. 用官方编码做**单变量对照**：把 `new_session()` 换成官方 tA() 编码
   （`hex = ((27<<36) - 1 - unix_millis) << 12 | 0xFFE`），其余头 / UA / body 逐字节不变。
   `403 → 429` 即确诊（2026-10-14 实测）。
4. 交叉验证：判据与编码实现见 §5.1 / §5.3 / §6；仓库侧的对应实现是
   `mintOpenCodeSessionID` 与 `isOpenCodeGateSessionID`（`backend/internal/service/openai_opencode_freetier.go`）。

**修复位置（⚠️ 不是 sub2api）**：`/opt/warp-rotate/warp-rotate.py` 的 `new_session()`。

- 先备份：`cp -a /opt/warp-rotate/warp-rotate.py{,.bak-$(date -u +%Y%m%dT%H%M%SZ)}`。
- 改完**必须重启服务**：`systemctl restart warp-rotate-api.service`。
  该服务在进程内 `importlib` 加载模块，**不重启不生效**——「改了文件没重启」是本节第二个坑。
- 重启后按上面的对照再探一次，`probe http=429 status=exhausted` 即为修复成功。

**2026-10-14 实测证据（修复前 → 修复后）**：

```
修复前：probe http=403 status=other → 503 action=- attempts=0 exit_ip=...:1cd2::2df:12 result=probe_error
修复后：probe http=429 status=exhausted retry_after=39533
        step=exhausted ... rotating (max 6 tries, budget 90s)
        attempt=1/6 rotated ...:1cd2::2df:12 -> ...:31f5::4fa:28 downtime=5.73s
        attempt=1/6 probe http=200 status=ok
        REQ r000001 -> 200 action=rotated attempts=1 result=usable elapsed=8.43s
```

备份：`/opt/warp-rotate/warp-rotate.py.bak-20261014T125608Z`。

**防复发检查清单：上游门禁变更时，这些地方都要改**——

| 位置 | 组件 | 改哪里 |
| --- | --- | --- |
| 仓库内 | sub2api 会话 id | `mintOpenCodeSessionID` / `openCodeGateSessionHexPartAt`（`openai_opencode_freetier.go`） |
| 仓库内 | **sub2api UA 版本带** | `openCodeUpstreamUserAgent`（`openai_opencode_session.go`）+ `openCodeUAVersionBandLow/High`（`openai_opencode_freetier_test.go`） |
| 服务器上 | WARP 轮换服务 | `new_session()`（`/opt/warp-rotate/warp-rotate.py`）**与 UA 常量**（`warp-rotate.py` / `warp-rotate.conf`） |
| 服务器上 | oc-probe | UA 常量（`/opt/oc-probe/probe.sh` / `probe.conf`） |

历史教训：PR #21（2026-10-13）只修了 **sub2api 自己发出的**会话 id，漏了轮换服务的探测请求
（`warp-rotate.py` 的 mtime 停在 2026-10-09），于是 2026-10-14 配额耗尽时轮换 100% 失败，
故障延迟一天才暴露。**任何「门禁编码」改动都要 grep 全链路**：

```bash
grep -rn 'ses_\|x-opencode-session' /opt/warp-rotate/          # 服务器侧
grep -rn 'x-opencode-session' --include=*.go backend/          # 仓库侧

# ⭐ UA 版本带同理：改一处就要全链路核对（否则不同组件会分处带内/带外，
#    症状会「诡异分裂」——探测 200 而生产 403，2026-10-14 正是这样）
grep -rn 'opencode/[0-9]' --include=*.go backend/              # 仓库侧
grep -rn 'opencode/[0-9]' /opt/warp-rotate/ /opt/oc-probe/     # 服务器侧
```

---

## 4. 现成工具清单

### 4.1 `TestLiveOpenCodeFreeTierGate` —— 端到端验收（正向 + 负向）

`backend/internal/service/openai_opencode_freetier_live_test.go`（`//go:build live`，不进常规 CI）。

```bash
cd sub2api/backend
HTTPS_PROXY=http://127.0.0.1:10809 HTTP_PROXY=http://127.0.0.1:10809 \
  OPENCODE_ZEN_API_KEY=<key> GOTOOLCHAIN=go1.27.2 \
  go test -tags=live ./internal/service/ -run TestLiveOpenCodeFreeTierGate -v -count=1
```

子测试与顺序（**顺序是有意的，不要重排**）：

| 顺序 | 子测试 | 预期 |
| --- | --- | --- |
| 1 | 实验组：门禁伪装必然放行 | 200 |
| 2 | 边界组：客户端不提供任何会话标识 | 200 |
| 3 | 无工具请求组：补官方工具集 + `tool_choice:none` | 200 |
| 4 | 对照组：无门禁必然被拒 | **403**（不封 key，§1.2；但会烧一次请求，故排在最后） |

用途：**验证修复是否成立**。三个正向组全绿 + 对照组 403 = 门禁伪装真实生效。

### 4.2 `TestLiveOpenCodeFreeTierProbe` —— 单次探针（诊断用）

`backend/internal/service/openai_opencode_freetier_probe_live_test.go`（`//go:build live`）。

```bash
cd sub2api/backend
HTTPS_PROXY=http://127.0.0.1:10809 HTTP_PROXY=http://127.0.0.1:10809 \
  ZEN_PROBE=<mode> ZEN_PROBE_OUT=/tmp/probe.txt \
  OPENCODE_ZEN_API_KEY=<key> GOTOOLCHAIN=go1.27.2 \
  go test -tags=live ./internal/service/ -run TestLiveOpenCodeFreeTierProbe -v -count=1
```

模式：

| `ZEN_PROBE` | 内容 | 预期 | 会烧掉一次请求/把 403 混进数据点？ |
| --- | --- | --- | --- |
| `current` | **完全由生产实现**产出的头与 body | **200** | 否 |
| `superset` | `current` + 参考插件发送的全部额外头 | **200** | 否 |
| `shapeonly` | 生产 body + 形状合法但**位不合法**的会话值（复现修复前形态） | **403** | 是（不封 key，§1.2） |
| `nogate` | 完全不施加门禁的裸请求 | **403** | 是（不封 key，§1.2） |

dump 里有一段 `=== SESSION DIAGNOSTICS ===`，**先看这几行**：

```
=== SESSION DIAGNOSTICS ===
  session: ses_ec580097cffeysx8xVwhQ12lLq
  shape_ok: true
  gate_ok (shape + official tA encoding + window): true
  hex12: ec580097cffe
  low12: ffe (required: ffe)
  minted_at: 1791982827139 (unix ms)
  window: [now-172800000ms, now+3600000ms], delta_ms = -2861, in_window = true
```

它能立刻区分三类问题：形状（`shape_ok`）、低 12 位（`low12` 必须为 `ffe`）、时间窗（`minted_at` 与 `delta_ms`）。
`delta_ms = minted_at - now`；超出 `[now-48h, now+1h]` 即 `in_window=false`——这种值会被 sub2api 重铸（只损失一次 prompt cache 命中，不会 403），**也**说明它不是官方客户端当前铸造的 id。

**安全**：探针对 `Authorization` / `X-Api-Key` / `Api-Key` 逐头掩码，并在写出/打印前对整份 dump 再抹一遍原始 key。**改动探针时不要绕过这两个出口**（历史上正是因为只掩码了 `=== AUTH:` 一行而泄漏过完整 key）。

### 4.3 `/opt/oc-probe/` —— 服务器上的 429 诊断探针

**手动、按需运行**（刻意没有 systemd timer / cron）。当 opencode 开始返回 429 时手动跑。

```bash
/opt/oc-probe/probe.sh probe    # 三段对比（核心）
/opt/oc-probe/probe.sh status   # 上次探针摘要 + 当前出口身份
/opt/oc-probe/probe.sh conf     # 生效配置（无密钥）
/opt/oc-probe/probe.sh help
```

三段使用**同一份 body**（打印并记录 sha256 以证明 body 未变），唯一变量是出口：

| 段 | 出口 | 方式 |
| --- | --- | --- |
| 1 | 当前 WARP 出口 | `curl -x socks5h://127.0.0.1:1080` |
| 2 | 服务器原生 IP | 不带代理的 `curl` |
| 3 | 全新 WARP 出口 | 一次 `warp-rotate.sh rotate`，等待恢复后发送 |

产物：

- stdout：三段对比表
- `/var/log/oc-probe.log`：追加式运行日志（超限自动轮转 `.log.1`）
- `/var/lib/oc-probe/last.json`：机器可读的完整头与 body 前缀（mode 600）

**怎么读**：

| 现象 | 结论 |
| --- | --- |
| 三段全 200 | 当前没被限流，无需诊断 |
| 第 2 段 200、1/3 段 429 | 限流按 WARP 出口 IP / 前缀记账 → 轮换 WARP 或绕开 |
| 三段全 429 且 `resets at` 相同 | 限流跟随 api key / 账号，不是 IP |
| 第 1 段 429、第 3 段 200 | 轮换 WARP 确实有效，上一个出口已被烧掉 |

**⚠️ 与本手册的关系**：`/opt/oc-probe/` 的 README 里把门禁头写成 `X-Opencode-Session: ses_<12 位小写 hex><14 位字母数字>`——那是 **2026-10-13 之前**的规格（只查形状）。它现在**不足以**过门禁，必须用完整官方 tA() 编码的值（低 12 位 `ffe` **且**高 36 位反解出的铸造时刻在窗口内，见 §5.1/§5.3）。用 `oc-probe` 诊断 429 时如果顺手看到 403，请按 §1.1 处理，并**记得更新该 README 的门禁规格**。

### 4.4 `/opt/warp-rotate/` —— 出口 IP 轮换服务

免费层配额按出口 IP 记账，所以 429 的处置手段是换出口。服务端独立部署，冻结契约：

```
POST http://172.19.0.1:9110/ensure-fresh
  Header: Authorization: Bearer <token>
  Body:   {"proxy_id": 17}
  200 → {"ok":true,"action":"none"|"rotated","usable":true,"exit_ip":"2a09:...",
         "attempts":0,"elapsed_seconds":1.2,
         "prefixes":{"slash32":"...","slash48":"...","slash64":"..."}}
  503 → {"ok":false,"usable":false,"error":"no_usable_exit",
         "exit_ip":"...","attempts":6,"retry_after":49711}
GET /status   /healthz
```

语义：当前出口可用则直接返回（`action=none`，**不轮换**）；不可用才轮换到可用为止。**服务端硬超时约 90 秒。**

地址说明：基址必须是 `http://172.19.0.1:9110`。`172.19.0.1` 是 docker 默认网桥的网关地址（宿主机在 bridge 网络上的入口），sub2api 容器内可达、公网不可达；**容器内没有 `warp-rotate` 这个 DNS 别名**，写 `http://warp-rotate:9110` 在容器里解析不了。

命令行轮换（宿主机上）：`/opt/warp-rotate/warp-rotate.sh rotate`（约 4 秒 WARP 中断）。

**⚠️ 轮换服务自身发出的探测请求也受门禁约束**：它用 `new_session()` 生成 `X-Opencode-Session`，
一旦编码不合法，`/ensure-fresh` 会返回 `503 probe_error` + `attempts=0`，表现为「轮换 100% 失败」。
完整症状、诊断与修复位置见 **§3.1**（这是 2026-10-14 的生产阻塞）。

### 4.5 `.oc-gate/getkey.sh` —— 只读取 key（含掩码）

从服务器 `sub2api-postgres` **只读** `SELECT credentials->>'api_key' FROM accounts WHERE id=31568`，写到 `.oc-gate/zen_key.txt`（仓库外，chmod 600），stdout 只显示掩码形态。

**掩码必须覆盖全部已知 key 方案**：`oc_sk_` / `sk-` / `sk_`。历史事故：正则只匹配 `sk-`，导致 `oc_sk_` 前缀的完整 key 明文进入对话记录。任何新增的 key 方案都要同步加进脚本里的 `SCHEMES`。

---

## 5. 门禁要求清单（截至 **2026-10-15**，UA 版本带 **`[1.18.0, 1.18.33]`**）

### 5.1 必需信号（**5 条**，缺一即 403 —— 信号 2 越上界也是 403，越下界是 426）

| # | 信号 | 要求 | 首次确认 |
| --- | --- | --- | --- |
| 1 | `Authorization` 凭据 | 必须是**受控账号 key**（`Bearer oc_sk_…`）。用字面量 `public` 等非受控凭据会 403 `FreeTierError`（2026-10-14 实测：同 recipe / 同出口 / 同 body，31568 的 `oc_sk_` → 200，31585 的 `public` → 403）。**排查时必须先确认对照用的 key 与生产转发用的是同一个受控 key** | **2026-10-14** |
| 2 | `User-Agent` **版本带** | 形态 `opencode/<major>.<minor>.<patch>`，且 **`1.18.0 <= version <= 1.18.33`**。低于 `1.18.0` → **426 UpgradeRequired**；**`>= 1.18.34` → 403 FreeTierError**（含官方最新 `1.19.0`）。UA 后缀（`ai-sdk/… runtime/bun/…`）对判定**无影响**，只有版本号起作用 | 下界 2026-09-30 / **上界 2026-10-15**（二分：1.18.33 → 200、1.18.34 → 403） |
| 3 | `X-Opencode-Session` | 形状 `^ses_[0-9a-f]{12}[0-9A-Za-z]{14}$` **且 hex 段为完整官方 tA() 编码**：低 12 位 == `0xFFE`（`counter == 1` 形态）**且**高 36 位反解出的铸造时刻落在窗口内（见 §5.3） | 形状 2026-09-30 / 低 12 位 **2026-10-13** / 完整编码 **2026-10-14** |
| 4 | body `tools` | 必须**同时**含 `bash` 与 `read` 这两个确切名字 | 2026-09-30 |
| 5 | body `stream` | 必须为 `true` | 2026-09-30 |

> ⚠️ **信号 2 的上界会变，而且可能是「反直觉」的方向。** 上游 2026-10-14 13:45–13:53 UTC 把上界钉在 `1.18.33`，**官方最新 `1.19.0` 反而被拒**。改动 UA 版本号前必须按 §2.4 重测；同步更新：
> - `openai_opencode_session.go` 的 `openCodeUpstreamUserAgent`（含注释里的实测日期与区间）
> - `openai_opencode_freetier_test.go` 的 `openCodeUAVersionBandLow` / `openCodeUAVersionBandHigh`
> - 本表 + §7 历史记录
> - **服务器侧**：`/opt/warp-rotate/warp-rotate.conf` / `warp-rotate.py`、`/opt/oc-probe/probe.conf` / `probe.sh`（§3.1 防复发清单）

### 5.2 已验证非必需（**不要为了「保险」而加**）

| 头 | 证据 |
| --- | --- |
| `X-Opencode-Request` | 缺失仍 200 |
| `X-Opencode-Client` | 缺失仍 200 |
| `X-Opencode-Project` | 缺失仍 200 |
| `Accept` | 缺失仍 200（200 那次一个都没发） |
| `X-Session-Affinity` | 门禁不校验；实现里仍镜像，仅为形态对齐 |
| `X-Session-Id` | 同上 |

**为什么不加**：每多一个与官方客户端不一致的头，就多一份「上游按头组合收紧规则」时的暴露面；而且会让「最小必需信号集」这个结论失效——下次再出问题时，无法从「我们发了什么」反推「上游要什么」。

#### ⚠️ 防复发：**镜像头是 2026-10-14 排查中最大的红鲱鱼**

- **R1 / R4 都带了 `X-Session-Affinity` + `X-Session-Id` 两个镜像头，照样 403。**（`/root/oc403/probe_remote.sh` 与 `curlcfg_R4_chat_baseline` 逐字含这两个头。）当时 403 的真因是**凭据**（R1–R4 全用 31585 的 `public`）与**UA 版本带**，不是镜像头。
- **同一时段、同一出口，生产探测请求只有 5 个头（零镜像头）却连续 5 次 200**（`16:51:15 / 17:01:19 / 17:11:20 / 17:21:22 / …`，`MAINTAIN_RESULT=USABLE`）。
- **因果方向反了**：是「10-13 成功的 dump 里恰好有这两个头、之后失败的探针恰好没有」这种**幸存者偏差**把人引向了镜像头。**先看单变量对照，再看「成功/失败样本之间还有什么不同」。**
- 结论：**`X-Session-Affinity` / `X-Session-Id` 既非必需，也不是任何一次已定位 403 的判别变量。** 再遇到 403，不要先去加头——按 §2.4 与 §5.1 的 1→2→3 顺序查。

### 5.3 高 36 位是否被校验

**上游侧：没有直接证据表明它按时间窗校验。** 2026-10-13 修复过程中，第一次 live 运行用的是「高 36 位与官方编码相差 1 个单位」的版本，三个正向组**全部 200**；且官方客户端一枚约 28 小时前铸造的 id（`hex=ecb74175effe`）仍返回 200。

**但我方透传判据自 2026-10-14 起校验高 36 位。** 原因是不对称性：

- 低 12 位 == `0xFFE` 只是完整编码的**结构必要条件**。哈希派生的 `ses_` 值（上游插件 `cpa-plugin-opencodezen` 正是这么生成 `ses_` 的）有 **1/4096** 的概率恰好把末 3 位落成 `ffe`；2026-10-14 单变量实测这类值仍然 **403**。
- **该透传时误判为重铸是无害的**：我们自己铸的值一样过门禁，只损失一次上游 prompt cache 命中。
- **不该透传时透传是致命的**：403 `FreeTierError` 会让这次请求失败（并污染 `upstream_error_detail` / 触发账号健康判定），期间发出的正确请求虽然也未必被拒，但故障持续存在、排查噪声很大。

因此判据从严（实现：`isOpenCodeGateSessionID` / `isOpenCodeGateSessionMintedAtMillis`）：

```
minted_at = (27<<36) - 1 - (hex >> 12)      # 从高 36 位反解出的铸造时刻（Unix 毫秒）
要求  now - 48h  <=  minted_at  <=  now + 1h
```

窗口常量取宽（48 小时 / 1 小时）的理由：

- 唯一有实测证据的官方复用样本（`ecb74175effe`）在被 200 接受时已铸造约 **28 小时**，窗口必须显著大于它；48 小时同时覆盖请求在途、分钟级时钟偏差与跨天会话。
- 窗口仍然**有限**：哈希噪声反解出的时刻近似均匀分布在 2^36 ms ≈ 795 天里，48h+1h 的窗口把上述 1/4096 漏检率再压低约 390 倍（≈ 6e-7）。
- 判严的代价只有重铸（prompt cache 未命中），所以**宁可多铸，不可放过**。真实历史样本（铸造时刻距今超过 48 小时）会被判为需重铸，这是**预期行为**，不是 bug——测试 `TestIsOpenCodeGateSessionIDRealSamplesAgeOut` 把它钉住了。

**把「只查低 12 位」当作实测结论，不要当作长期契约。** 上游若真的开始按时间窗校验，上面的窗口常量必须按实测重新校准（见 §6.5）。

### 5.4 ⚠️ 这份清单会变

上游 2026-10-11 才刚把门禁从「形状」升级为「形状 + 位不变量」，且变更**没有任何公告**。**每次读这份清单都要看日期。** 如果清单上的结论与你的实测不符，**相信实测**，并按 §6 重新推导，然后更新本文件。

---

## 6. ⭐ 如何重新推导（门禁再次变化时怎么查）

### 6.1 从官方发布产物挖算法

**仓库已从 `sst/opencode` 迁到 `anomalyco/opencode`。**

```bash
cd /tmp
# 先查最新版本号
curl -x http://127.0.0.1:10809 -s https://api.github.com/repos/anomalyco/opencode/releases/latest \
  | grep -m1 '"tag_name"'
# 下载对应平台的产物（linux-x64 最方便挖字符串）
curl -x http://127.0.0.1:10809 -L -o oc.tar.gz \
  https://github.com/anomalyco/opencode/releases/download/v<ver>/opencode-linux-x64.tar.gz
tar xzf oc.tar.gz
ls -la opencode
```

**挖什么**（在解包出的二进制里按这些模式 grep）：

| 目标 | 关键片段 |
| --- | --- |
| 会话 id 生成器 | `startsWith("ses")`、`SessionID`、`function tA`、`0x1000n`、`descending`、`cA++`、`~$`、`<< BigInt` |
| 门禁位不变量 | `ffe`、`0xffe`、`4094`（时间窗 48h/1h 是**我方**策略常量，不在二进制里） |
| 递减计数起点常量 | 当前的 `1855425871872`（`27<<36`）；**这个常量本身也会变** |
| UA 构造 | `ai-sdk/provider-utils`、`provider-utils/${VAR}`、`user-agent`、`runtime/bun` |
| 会话头名 | `x-opencode-session`、`x-session-affinity`、`x-session-id` |

一段可直接用的提取脚本（历史排查里用过的形态）：

```python
import re
data = open("opencode", "rb").read()
def show(label, pat, before=300, after=400, limit=4):
    print("=" * 90); print("PATTERN", label)
    n = 0
    for m in re.finditer(pat, data):
        n += 1
        if n <= limit:
            s = max(0, m.start() - before); e = min(len(data), m.start() + after)
            print("  [%d] @%d :: %s" % (n, m.start(),
                  data[s:e].decode("utf-8", "replace").replace("\n", "\\n")))
    print("  total", n)

show("epoch-const", rb"1855425871872")
show("ses-factory", rb'startsWith\("ses"\)')
show("id-gen", rb"0x1000n")
show("ffe", rb"ffe\b")
show("ua", rb"ai-sdk/provider-utils")
```

**为什么这比反复试探快得多**：反复试探要烧请求、还会把 403 混进数据点、拉长判别时间；挖二进制是本地操作，几分钟就能拿到**精确算法**，再用真实客户端 id 反推验证，一次成功。

### 6.2 怎么拿到「真实客户端 id」作为样本

有样本才能反推常量、验证你的实现。几个来源：

1. **跑一次官方客户端并抓包**（最可靠）：
   ```bash
   # 用 mitmproxy 做中间人
   mitmproxy -p 8888
   # 另一个终端：让官方 opencode 走这个代理
   HTTPS_PROXY=http://127.0.0.1:8888 HTTP_PROXY=http://127.0.0.1:8888 opencode
   # 在 mitmproxy 里看 opencode.ai/zen/* 请求的 x-opencode-session
   ```
   **关键**：要拿到**会话创建时的第一个** id（`counter == 1`），才能验证位不变量。多抓几个不同会话。
2. **从二进制的字符串/测试固件里 grep**：`grep -aoE 'ses_[0-9a-f]{12}[0-9A-Za-z]{14}' opencode | sort -u`
3. **从上游插件仓库**（如 `cpa-plugin-opencodezen`）的日志或 issue 里找。
4. **从本站生产日志**：出站请求头在 debug 级别会记录；另外每个上游 403 都带 `x-opencode-log-id`，可与 opencode 对账。
5. **自己实现一版后，用官方的「非首次」样本反向确认**：即使样本位不合法（`counter > 1`），它仍然能验证**常量**（见 §6.3）。

### 6.3 验证自洽性

拿到样本 id 后，用这些等式验证你的理解（Python 一行即可）：

```python
E = 27 << 36                                   # 递减计数起点常量（会变！）
hexv = int("ecb74175effe", 16)                 # 样本的 12 位 hex 段（真实客户端 id）
print("low12 =", hex(hexv & 0xFFF))            # 门禁要求 == 0xffe
print("mintedAt =", E - 1 - (hexv >> 12))      # 反推铸造时刻（应是一个合理的近期时间戳）
```

- **`(hex >> 12) + mintedAt == E - 1`** —— 常量正确、算法理解正确。
- **`low12 == 0xFFE`** —— 该样本是「门禁合法形态」（即官方客户端在某个毫秒里的**第一个** id）。
- **样本反推出的时刻落在合理范围内**（不是 1970，也不是 2100）—— 说明常量与窗口判断正确。

**三条以上独立样本都自洽**，才算理解正确。

> 历史实例（2026-10-13）：`ecb74175effe` → 反推 2026-10-13 09:15:46.721 UTC（低 12 位 `ffe` ✅）；`d1bfaf4aff65` → 反推 2027-01-05（低 12 位 `f65` ❌）；`fbfd91e7a85c` → 反推 2026-08-26（低 12 位 `85c` ❌）。
> **后两条也是「真实客户端 id」，但位不合法**——它们是官方客户端的非首次铸造路径（`counter > 1`）产物。
> **教训：「复用一条真实客户端 id」并不保证过门禁**；只有 `counter == 1` 的形态才行。
>
> ⚠️ **`ecb74175effe → 200` 是误导性样本，不要再引它证明「末 3 位凑成 `ffe` 就够了」。**
> 它反推出的铸造时刻（2026-10-13 09:15:46.721 UTC）本来就是真的：它是一枚**真实的官方客户端 id**，
> 而不是「手工把末 3 位改成 `ffe`」的构造值。2026-10-14 补做的单变量对照（随机 hex，**只**把末 3 位
> 改成 `ffe`）返回 **403**，才证明低 12 位既不充分、当时把它写成「门禁位不变量」也过松。
> 正确判据见 §5.1 / §5.3。

### 6.4 推导出闭式后的验证顺序

1. 用**官方 `tA()` 的逐字复刻**作为测试参照，对大量时间点逐位比对（见 `TestOpenCodeGateSessionHexMatchesOfficialEncoding`）。
2. 用**真实客户端样本**反推常量。
3. **先做 UA 版本带的单变量对照**（§2.4）——改一个版本号就能复现/消除，成本最低。
4. 跑探针 `current` 模式打真实上游 —— 一次请求，不封 key。
5. 只有在上面都失败时，才动用 `shapeonly` / `nogate` 这类会烧请求、把 403 混进数据点的对照。

### 6.5 排查完记得更新

- 本文件的 §1 / §2.4 / §3.1 / §5 / §7。
- `openai_opencode_freetier.go` 里 `openCodeGateSessionLowHex`、`openCodeGateSessionEpochMillis`
  与时间窗常量（`openCodeGateSessionMintMaxAgeMillis` / `…FutureSkewMillis`）的注释（含实测日期）。
- **UA 版本带改了**：`openai_opencode_session.go` 的 `openCodeUpstreamUserAgent` 注释（写明实测日期与闭区间）、
  `openai_opencode_freetier_test.go` 的 `openCodeUAVersionBandLow` / `openCodeUAVersionBandHigh`、本文件 §5.1 信号 2。
- 探针的 `SESSION DIAGNOSTICS` 若新增判定项，同步加进去。
- **服务器侧**：`/opt/warp-rotate/warp-rotate.py` 的 `new_session()` 与 UA 常量、`/opt/oc-probe/` 的 UA 常量（见 §3.1 的防复发清单）。

---

## 7. 历史变更记录

| 日期 | 变更 | 症状 | 证据 / 处置 |
| --- | --- | --- | --- |
| 2026-09-30 | 门禁建立：形状 `ses_` + UA ≥ 1.18.0 + `bash`+`read` + `stream:true`，四者缺一即 403 | `403 FreeTierError`；UA `1.0.0` 另触发 `426` | 消融实验（17 组）；实现门禁伪装 |
| 2026-10-04 | （我方）补齐会话头镜像与官方 6 工具、按名排序 | — | 实测 6 工具 + affinity → 200 |
| 2026-10-06 | （我方）工具注入范围收紧为「客户端已带工具时只补 `bash`+`read`」 | 客户端 `unsupported call: read/bash` → 整轮 `provider_fault` | 生产故障驱动 |
| 2026-10-06 | （我方）`tool_choice` 只在 Chat 方言注入 | `/responses` 返回 `400 only "auto" is supported` | 实测 chat 200 / responses 400 |
| 2026-10-09 | （我方）429 时先轮换 WARP 出口再用同账号重试 | `429 FreeUsageLimitError` | 配额按出口 IP 记账；每日 `00:00 UTC` 重置 |
| **2026-10-11** | **上游把 `/zen/*` 改为指向新 inference 服务的薄代理**（`anomalyco/opencode` `83f2b6f67a`）；**门禁从「查形状」升级为「查形状 + 位不变量」：hex 段低 12 位必须为 `0xFFE`** | `403 FreeTierError`（与「没发会话头」完全同形） | 单变量消融：`hex=d1bfaf4aff65`(f65) → 403；`hex=ecb74175effe`(ffe) → 200。头/UA/body 逐字节相同。⚠️ 后者是**真实官方 id**（反推铸造时刻 2026-10-13T09:15:46Z），**不能**用来证明「末 3 位凑成 `ffe` 即可」，见 §6.3 |
| 2026-10-13 | （我方）按官方编码铸造会话 id + 有界缓存；哈希型客户端值改为重铸；UA 注释更正 | 修复上线 | 上游实测 200（`cf-ray a49d802a6c265cfd-LAX`）；live 测试 3 正向 200 + 对照 403 |
| 2026-10-13 | （我方）探针 dump 泄漏 API key 的缺陷修复；`getkey.sh` 掩码覆盖 `oc_sk_` | 本地文件与对话记录中出现完整 key | 就地抹除 + 双保险掩码；建议轮换账号 31568 的 key |
| **2026-10-13** | **（我方，后被证明是危险操作）把 UA 从 `1.18.31` 升到官方最新 `1.19.0`** | 当时无害；**次日上游收紧版本带后成为全站 403 的根因** | 「保持最新无害」的假设被 2026-10-15 实测证伪 —— 见本表 2026-10-15 两行 |
| **2026-10-14** | **（结论修正）低 12 位 == `0xFFE` 只是必要不充分**：随机 hex 仅把末 3 位凑成 `ffe` 仍 403；门禁真正要求 hex 段是**完整官方 tA() 编码** | `403 FreeTierError` | 单变量实测（头/UA/body 逐字节相同）：随机 hex（末 3 位 `ffe`）→ 403；官方完整编码 `ec580097cffe` → 429 `FreeUsageLimitError`（**过门禁**，拿到配额判决） |
| **2026-10-14** | **（生产阻塞）WARP 轮换服务的探测请求用随机 hex 会话 id → 门禁 403 → `503 probe_error attempts=0`，轮换 100% 失败**（PR #21 漏改服务器侧） | 客户端 429（sub2api 合成文案）+ `opencode_warp_rotate reason=failed detail=…probe_error attempts=0`；`/var/log/warp-rotate-api.log` 里 `step=probe http=403 status=other` | 修 `/opt/warp-rotate/warp-rotate.py` 的 `new_session()` 并**重启** `warp-rotate-api.service`（备份 `.bak-20261014T125608Z`）；修复后 `probe http=429 status=exhausted` → `attempt=1/6 … probe http=200` → `REQ r000001 -> 200 action=rotated attempts=1 elapsed=8.43s`。见 §3.1 |
| **2026-10-14** | （我方）sub2api 透传判据收紧为「形状 + 完整编码」（低 12 位 + 高 36 位时间窗 48h/1h） | 堵住哈希派生 id 里 1/4096 的 `ffe` 偶然命中（透传即 403） | 新增单测：低 12 位恰为 `ffe` 的哈希值必须重铸；真实样本按铸造时刻求值仍合法。见 §5.3 |
| **2026-10-14** | （结论修正）**镜像头假设证伪**：`X-Session-Affinity` / `X-Session-Id` 既非必需也非鉴别变量；真因指向**凭据** | R1/R4 带镜像头仍 403；同时段生产探测零镜像头连续 5 次 200 | 3 次受控实测（间隔 14m59s / 15m56s）+ `/root/oc403/` 的 curl 配置逐字比对。见 §5.2 |
| **2026-10-14 13:45–13:53 UTC** | **（上游回归）上游收紧免费层门禁的 UA 版本带为 `[1.18.0, 1.18.33]`**；`>= 1.18.34` 一律 403 `FreeTierError`，**官方最新 `1.19.0` 自己也被拒** | 全站 `403 FreeTierError`（sub2api 出站 UA 为 `1.19.0`，落到带外） | 我方 UA 于 2026-10-13 从 `1.18.31` 升到 `1.19.0`，当时无害 → 次日落入带外。见下方「教训」 |
| **2026-10-14 20:03–00:41** | （上游瞬态窗口）版本带切换过渡期，`1.18.0` 与 `1.19.0` **都被拒** | warp-rotate 探测连续 `http=403 status=other` | 与「封 key」无关：这是上游换带过渡，量级 **2–3 小时**，不是 15 分钟。见 §1.2 |
| **2026-10-15** | **（结论修正）403 不封 key、不封 IP —— 「封 15 分钟」的因果被证伪** | 受控实测 4 组 403→200 间隔仅 **21–33 秒**；24 请求二分全程无 429/超时/5xx | 同 key / 同出口交错对照（`01:46:44→01:47:17`、`01:48:20→01:48:41`、`01:49:02→01:49:23`、`01:54:03→01:54:24` UTC）。15 分钟仅作操作纪律保留。见 §1.2 |
| **2026-10-15** | **（生产阻塞修复）`openCodeUpstreamUserAgent` 由 `1.19.0` 回落到带内 `1.18.31`** | 修复前 `1.19.0` → **403 FreeTierError**；修复后 `1.18.31` → **200**（SSE `response.created`） | 单变量实测（同 key / body / 出口，间隔 22 s）：`1.19.0 ai-sdk/…bun/1.3.14` → `403`（`cf-ray a4ab483e6aa67d78-ORD`）；`1.18.31 ai-sdk/…bun/1.3.14` → `200`（`cf-ray a4ab489f1f847d78-ORD`）。同步：常量注释、`openai_opencode_freetier.go` 信号 2、新增 `TestOpenCodeUpstreamUserAgentVersionBand`（已用变异测试确认能拦住 `1.19.0`） |
| **2026-10-15** | **（结论修正）上界二分确定**：`1.18.32` / `1.18.33` → 200；`1.18.34` / `1.18.35` → 403；`1.17.9` → 426 | 划出闭区间 `[1.18.0, 1.18.33]` | 24 请求单变量消融（含 8 个 403，无封禁） |
| **2026-10-15** | **（生产阻塞修复）`400 reasoning `encrypted_content` was not issued to this caller` 未触发自愈**：该报错 `error.code` 为 `null`，原判据只认 code，两个分支都不匹配 → 恢复被跳过。改为按消息匹配（`encrypted_content` + `was not issued to this caller`），复用既有「丢加密推理项 + 重试一次」逻辑 | compact / 长历史重放时 `400`，客户端直接失败；`ops_error_logs` 5 天内 8 条（10-10 起，账号 31568/31585/31589，全部 `muse-spark-1.3-contributor-free`，全部 `upstream_status_code=400`） | 确诊依据：失败回合账号 ≠ 同会话早前成功回合账号（如 10-15 11:07:48 失败于 `31589`，此前成功全在 `31568`）；容器日志 `transport:"http"`、无 `Retrying non-WSv2` 行。见 §1.5 |

### ⚠️ 教训：**「顺手把版本号升到最新」是危险操作**

2026-10-13 我们把 UA 从 `1.18.31` 升到官方最新 `1.19.0`，理由是「保持最新无害」——**当时的实测也确实无害**。次日上游收紧版本带，`1.19.0` 立刻变成全站 403 的根因。

这条教训的可推广形式：

1. **门禁类常量不要以「更新」为默认方向。** UA 版本、会话编码这类伪装值的正确性完全取决于上游当下的实现，与「我们的代码是否过时」无关。
2. **改伪装值等于赌上游的当前状态。** 任何一次这样的改动都必须附**当次**的上游实测证据（正反两侧），不能引用上一次的实测。
3. **改动必须被测试钉住。** 本次新增的 `TestOpenCodeUpstreamUserAgentVersionBand` 会在任何人把版本改到带外时 FAIL（上界写在测试里，不写在人的记忆里）。
4. **上界可能比当前官方版本低。** 因此「用最新版」甚至可能比「用一个中段的旧版」更容易被拒。生产常量取**带内中段**（当前 `1.18.31`：距下界 31 个 patch、距上界 2 个 patch）。

**下一次变更怎么记录**：把「上游变更」与「我方修复」分行，写清日期、症状（原始状态码 + body 片段）、证据（消融对照 + `cf-ray`），并**在 §5 的清单上标注新日期**。

---

## 8. 快速参考卡

```bash
# ── 环境 ────────────────────────────────────────────────
export GOCACHE=G:/My_Project/注册机/.gocache
export GOTMPDIR=G:/My_Project/注册机/.gotmp
export GOTOOLCHAIN=go1.27.2
export HTTPS_PROXY=http://127.0.0.1:10809
export HTTP_PROXY=http://127.0.0.1:10809

# ── 取 key（只读 SELECT，掩码输出）─────────────────────
bash /g/My_Project/注册机/.oc-gate/getkey.sh
export OPENCODE_ZEN_API_KEY="$(cat /g/My_Project/注册机/.oc-gate/zen_key.txt)"

# ── 单测（不碰上游，安全）──────────────────────────────
cd /g/My_Project/注册机/sub2api/backend
go test -tags=unit ./internal/service/ -run 'OpenCode' -count=1

# ── 探针：全绿基线（不封禁）────────────────────────────
ZEN_PROBE=current ZEN_PROBE_OUT=/tmp/probe.txt \
  go test -tags=live ./internal/service/ -run TestLiveOpenCodeFreeTierProbe -v -count=1

# ── 验收：正向 + 负向（负向会多烧一次请求；403 不封 key，见 §1.2）──
go test -tags=live ./internal/service/ -run TestLiveOpenCodeFreeTierGate -v -count=1

# ── UA 版本带单测（不碰上游，改版本号后必跑）────────────
go test -tags=unit ./internal/service/ \
  -run 'TestOpenCodeUpstreamUserAgentVersionBand' -v -count=1

# ── 429 诊断（服务器上）────────────────────────────────
/opt/oc-probe/probe.sh probe

# ── 轮换探测是否被门禁拒（服务器上，只读）──────────────
grep -n 'step=probe' /var/log/warp-rotate-api.log | tail -20
#   http=403 → 门禁失配（§3.1）；http=429 status=exhausted → 正常，该轮换

# ── 换出口（服务器上）──────────────────────────────────
/opt/warp-rotate/warp-rotate.sh rotate
```

**五条铁律**：

1. 一次只改一个变量。
2. **403 先把 UA 版本核对一遍**（§2.4）——「改一个版本号就能复现/消除」是最快的判别法。
3. 403 **不封 key、不封 IP**（§1.2）；15 分钟只是操作纪律，不要用「封禁未解除」解释意外的 403；但**连续 2 小时以上全 403** 要按上游瞬态窗口处理，别继续烧请求。
4. 先挖二进制，再动手试探。
5. **门禁常量要改就改全**：sub2api 的 `mintOpenCodeSessionID` **与** `openCodeUpstreamUserAgent`，加服务器侧 warp-rotate 的 `new_session()` / UA、oc-probe 的 UA（§3.1）。**UA 版本号不是「越新越好」。**
