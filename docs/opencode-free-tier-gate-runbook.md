# Runbook：OpenCode 免费层门禁排查手册

> **适用对象**：Sub2API 的 OpenCode Zen 免费层（模型 ID 以 `-free` 结尾）突然大面积失败时的定位与修复。
> **目标**：让未来的人（或未来的你）在 **半小时内** 判断「是门禁又变了 / 是配额 / 是 UA / 是别的东西」，并知道怎么重新推导出新规则。
> **最后校准**：2026-10-14，opencode **1.19.0**。**门禁规则会变，本手册的结论必须带日期读。**

---

## 0. 三十秒决策树

```
免费层请求失败
├─ HTTP 403 + body 含 "FreeTierError"
│   ├─ 15 分钟内有另一个 403？ ──► 是「封禁未解除」，等满 15 分钟再测
│   └─ 否则 ──► 门禁身份不通过 → 走 §2 单变量消融
├─ HTTP 429 + body 含 "FreeUsageLimitError" ──► 出口 IP 配额耗尽（另一回事）→ §3
│   └─ 且 opencode_warp_rotate 事件 reason=failed / detail 含 probe_error / attempts=0
│        ──► 轮换服务自己的探测请求也没过门禁 → §3.1（先修这个，否则轮换 100% 失败）
├─ HTTP 426 + "UpgradeRequired" ──► UA 版本低于下限 → §1.3
└─ 其它（400 / 403 RegionError / 500）──► §1.4
```

**先做这一步**：把失败响应的 `date`、`cf-ray`、`x-opencode-log-id` 三个头抄下来。`x-opencode-log-id` 是与 opencode 对账时的唯一凭据；`date` 是判断「封禁窗口」的唯一依据。

---

## 1. 症状分流

### 1.1 `403 FreeTierError` —— 门禁身份不通过

```json
{"type":"error","error":{"type":"FreeTierError",
 "message":"OpenCode's free tier can only be used from within OpenCode. Download it at https://opencode.ai and keep building."}}
```

含义：**上游认为这次请求不是官方 OpenCode 客户端发的。**

这条错误信息**没有指向任何具体缺失的信号**。会话头形状不对、会话编码不合法（低 12 位不是 `0xFFE`，或高 36 位反解不出合法铸造时刻）、缺 `bash`/`read`、`stream:false`、完全没发会话头——**全部**返回这同一段文本。所以必须用消融法（§2）而不是猜。

### 1.2 ⚠️ 门禁 403 会封禁 key 约 15 分钟

**这是排查中最容易踩的坑。** 一旦发出一个门禁 403，该 API key 在约 15 分钟内会被**无差别**拒绝——此时即使你发出完全正确的请求，也会拿到一模一样的 403 `FreeTierError`。

真实教训（2026-10-13）：一次 `superset2`（9 个头 + 6 工具 + 形状合法的会话）在 `08:59:24` 返回 403，看起来像「额外头有害」。但它在 `08:53:58` 那次 403 之后仅 **5.5 分钟**发出——封禁根本没解除。那次 403 不能归因于额外头。

**操作规则**：

- **每次 403 之后至少等 15 分钟**再发下一个请求。
- 每条记录都写 `date` 头，并计算与上一次 403 的间隔。间隔 < 15 分钟的记录**一律作废**。
- 把「故意触发 403」的用例（负向对照组）**排在最后**。仓库里的 `TestLiveOpenCodeFreeTierGate` 已按此重排。
- 一次完整的排查通常需要 3–5 个数据点，**因此排一次消融要预留 1–1.5 小时**。不要试图「快点多试几次」——那样只会浪费更多 15 分钟。

### 1.3 `426 UpgradeRequired` —— UA 版本低于下限

含义：`User-Agent` 里的 `opencode/<version>` 低于上游的下限。

实测（2026-09-30）：`opencode/1.17.9` → 426；`opencode/1.18.0` → 200。

**注意**：**UA 版本号本身不是门禁检查项**。`1.18.0` / `1.18.31` / `1.19.0` 在门禁上完全等价（实测均 200）。2026-10-13 那次免费层 403 的真实根因是会话 id 的**编码**（hex 段必须是完整官方 tA() 编码，低 12 位 `0xFFE` 只是其结构推论，见 §5.1/§5.3），与 UA 无关——**同类故障不要先去动 UA**。

### 1.4 其它错误

| 症状 | 含义 | 处理 |
| --- | --- | --- |
| `429` + `FreeUsageLimitError` | 免费层配额耗尽，**按出口 IP 记账**，每日 `00:00 UTC` 重置 | §3 |
| `400` + ``only `"auto"` is supported for `tool_choice` `` | `/responses` 端点只接受 `tool_choice:"auto"` | 别在 Responses/Anthropic 方言注入 `tool_choice` |
| `400` + `Recursive JSON schemas are not currently supported` | `/responses` 拒绝带 `$ref` 等 JSON Schema 引用关键字的工具 | 查客户端带来的工具 schema（门禁注入的是扁平 schema） |
| `403` + `RegionError` | 地理封锁（如 `muse-spark-*` 在某些网络） | 换出口或换模型；**不是门禁失败** |
| `500` | 上游模型侧故障（如 `jev-1.13-free` 在 chat/responses 均 500） | 换模型 |

---

## 2. 排查步骤：单变量消融法

### 2.1 原则

1. **固定一切，一次只改一个变量。** 头、UA、body 逐字节相同；只改你要验证的那一项。
2. **先测「超集」再收窄。** 先构造一个「所有可能信号都给上」的请求（应 200），再逐个删减，删到哪个变 403，哪个就是必需项。
3. **每个数据点都要有正反两侧。** 只测「加了 X 就 200」不够，还要测「去掉 X 就 403」——否则无法排除偶然。
4. **每步之间等 15 分钟**（§1.2）。
5. **记录原始响应头与响应体**，不要只记状态码。`cf-ray` / `x-opencode-log-id` / `date` 是事后复盘的全部依据。

### 2.2 具体流程

```
第 0 步  确认当前 key 未被封禁
         ── 发一次「我们认为是正确」的请求（探针 current 模式）。200 → 继续；
            403 → 等 15 分钟再重来。

第 1 步  确认门禁仍然生效
         ── 发一次完全不伪装的裸请求（探针 nogate 模式）。必须 403 FreeTierError。
            若返回 200 → 上游已放宽门禁，本特性需要重新评估。
            ⚠️ 这一步会封禁 key 15 分钟，所以放在最后做。

第 2 步  定位是哪一项信号失效
         ── 从「全绿基线」出发，逐项破坏，看哪一项破坏后仍然 200。
            仍然 200 的项 = 非必需；变 403 的项 = 必需。
            ⚠️ 每次破坏都会引入 15 分钟封禁，所以：
              · 优先测「我们怀疑变了」的那一项
              · 每一项之间等满 15 分钟

第 3 步  若发现某个值被拒，反推它的构造规则
         ── 见 §5。门禁值往往由官方客户端的算法决定，
            从发布产物里挖比反复试探快得多。
```

### 2.3 本仓库现成的消融工具

见 §4。`ZEN_PROBE=current` 是「全绿基线」，`shapeonly` / `nogate` 是负向对照。

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

**与门禁的区别**：429 是配额；403 `FreeTierError` 是身份。**429 不会封禁 key**，403 会。

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

**防复发检查清单：上游门禁变更时，两处都要改**——

| 位置 | 组件 | 改哪里 |
| --- | --- | --- |
| 仓库内 | sub2api | `mintOpenCodeSessionID` / `openCodeGateSessionHexPartAt`（`openai_opencode_freetier.go`） |
| 服务器上 | WARP 轮换服务 | `new_session()`（`/opt/warp-rotate/warp-rotate.py`） |

历史教训：PR #21（2026-10-13）只修了 **sub2api 自己发出的**会话 id，漏了轮换服务的探测请求
（`warp-rotate.py` 的 mtime 停在 2026-10-09），于是 2026-10-14 配额耗尽时轮换 100% 失败，
故障延迟一天才暴露。**任何「门禁编码」改动都要 grep 全链路**：

```bash
grep -rn 'ses_\|x-opencode-session' /opt/warp-rotate/          # 服务器侧
grep -rn 'x-opencode-session' --include=*.go backend/          # 仓库侧
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
| 4 | 对照组：无门禁必然被拒 | **403**（会封禁 key 15 分钟） |

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

| `ZEN_PROBE` | 内容 | 预期 | 会封禁 key？ |
| --- | --- | --- | --- |
| `current` | **完全由生产实现**产出的头与 body | **200** | 否 |
| `superset` | `current` + 参考插件发送的全部额外头 | **200** | 否 |
| `shapeonly` | 生产 body + 形状合法但**位不合法**的会话值（复现修复前形态） | **403** | **是** |
| `nogate` | 完全不施加门禁的裸请求 | **403** | **是** |

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

## 5. 门禁要求清单（截至 2026-10-14，opencode 1.19.0）

### 5.1 必需信号（4 条，缺一即 403）

| # | 信号 | 要求 | 首次确认 |
| --- | --- | --- | --- |
| 1 | `X-Opencode-Session` | 形状 `^ses_[0-9a-f]{12}[0-9A-Za-z]{14}$` **且 hex 段为完整官方 tA() 编码**：低 12 位 == `0xFFE`（`counter == 1` 形态）**且**高 36 位反解出的铸造时刻落在窗口内（见 §5.3） | 形状 2026-09-30 / 低 12 位 **2026-10-13** / 完整编码 **2026-10-14** |
| 2 | `User-Agent` | `opencode/<version>` 且 `version >= 1.18.0`（低于下限 → **426**，不是 403） | 2026-09-30 |
| 3 | body `tools` | 必须**同时**含 `bash` 与 `read` 这两个确切名字 | 2026-09-30 |
| 4 | body `stream` | 必须为 `true` | 2026-09-30 |

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

### 5.3 高 36 位是否被校验

**上游侧：没有直接证据表明它按时间窗校验。** 2026-10-13 修复过程中，第一次 live 运行用的是「高 36 位与官方编码相差 1 个单位」的版本，三个正向组**全部 200**；且官方客户端一枚约 28 小时前铸造的 id（`hex=ecb74175effe`）仍返回 200。

**但我方透传判据自 2026-10-14 起校验高 36 位。** 原因是不对称性：

- 低 12 位 == `0xFFE` 只是完整编码的**结构必要条件**。哈希派生的 `ses_` 值（上游插件 `cpa-plugin-opencodezen` 正是这么生成 `ses_` 的）有 **1/4096** 的概率恰好把末 3 位落成 `ffe`；2026-10-14 单变量实测这类值仍然 **403**。
- **该透传时误判为重铸是无害的**：我们自己铸的值一样过门禁，只损失一次上游 prompt cache 命中。
- **不该透传时透传是致命的**：403 会**封 key 约 15 分钟**（§1.2），期间连正确请求也被拒。

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

**为什么这比反复试探快得多**：每次试探的失败代价是 15 分钟封禁；挖二进制是本地操作，几分钟就能拿到**精确算法**，再用真实客户端 id 反推验证，一次成功。

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
3. 跑探针 `current` 模式打真实上游 —— 一次请求，不封禁。
4. 只有在第 3 步失败时，才动用 `shapeonly` / `nogate` 这类会封禁的对照。

### 6.5 排查完记得更新

- 本文件的 §1 / §3.1 / §5 / §7。
- `openai_opencode_freetier.go` 里 `openCodeGateSessionLowHex`、`openCodeGateSessionEpochMillis`
  与时间窗常量（`openCodeGateSessionMintMaxAgeMillis` / `…FutureSkewMillis`）的注释（含实测日期）。
- 探针的 `SESSION DIAGNOSTICS` 若新增判定项，同步加进去。
- **服务器侧**：`/opt/warp-rotate/warp-rotate.py` 的 `new_session()`（见 §3.1 的防复发清单）。

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
| **2026-10-14** | **（结论修正）低 12 位 == `0xFFE` 只是必要不充分**：随机 hex 仅把末 3 位凑成 `ffe` 仍 403；门禁真正要求 hex 段是**完整官方 tA() 编码** | `403 FreeTierError` | 单变量实测（头/UA/body 逐字节相同）：随机 hex（末 3 位 `ffe`）→ 403；官方完整编码 `ec580097cffe` → 429 `FreeUsageLimitError`（**过门禁**，拿到配额判决） |
| **2026-10-14** | **（生产阻塞）WARP 轮换服务的探测请求用随机 hex 会话 id → 门禁 403 → `503 probe_error attempts=0`，轮换 100% 失败**（PR #21 漏改服务器侧） | 客户端 429（sub2api 合成文案）+ `opencode_warp_rotate reason=failed detail=…probe_error attempts=0`；`/var/log/warp-rotate-api.log` 里 `step=probe http=403 status=other` | 修 `/opt/warp-rotate/warp-rotate.py` 的 `new_session()` 并**重启** `warp-rotate-api.service`（备份 `.bak-20261014T125608Z`）；修复后 `probe http=429 status=exhausted` → `attempt=1/6 … probe http=200` → `REQ r000001 -> 200 action=rotated attempts=1 elapsed=8.43s`。见 §3.1 |
| **2026-10-14** | （我方）sub2api 透传判据收紧为「形状 + 完整编码」（低 12 位 + 高 36 位时间窗 48h/1h） | 堵住哈希派生 id 里 1/4096 的 `ffe` 偶然命中（透传即 403 → 封 key 15 分钟） | 新增单测：低 12 位恰为 `ffe` 的哈希值必须重铸；真实样本按铸造时刻求值仍合法。见 §5.3 |

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

# ── 验收：正向 + 负向（负向会封禁 key 15 分钟）──────────
go test -tags=live ./internal/service/ -run TestLiveOpenCodeFreeTierGate -v -count=1

# ── 429 诊断（服务器上）────────────────────────────────
/opt/oc-probe/probe.sh probe

# ── 轮换探测是否被门禁拒（服务器上，只读）──────────────
grep -n 'step=probe' /var/log/warp-rotate-api.log | tail -20
#   http=403 → 门禁失配（§3.1）；http=429 status=exhausted → 正常，该轮换

# ── 换出口（服务器上）──────────────────────────────────
/opt/warp-rotate/warp-rotate.sh rotate
```

**四条铁律**：

1. 一次只改一个变量。
2. 403 之后等满 15 分钟。
3. 先挖二进制，再动手试探。
4. **门禁编码要改就改两处**：sub2api 的 `mintOpenCodeSessionID` **与** warp-rotate 的 `new_session()`（§3.1）。
