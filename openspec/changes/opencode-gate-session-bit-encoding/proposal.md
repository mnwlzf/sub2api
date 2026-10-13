# 提案：修复 OpenCode 免费层门禁的会话位编码

## Why

2026-10-13 起，OpenCode Zen 免费层（模型 ID 以 `-free` 结尾）稳定返回：

```json
{"type":"error","error":{"type":"FreeTierError",
 "message":"OpenCode's free tier can only be used from within OpenCode. Download it at https://opencode.ai and keep building."}}
```

HTTP `403`，`cf-ray: a49d1deed94408af-LAX`，`x-opencode-log-id: adb0cb3e-8468-40e9-8da6-2120582eda9b`。

而我们的门禁伪装实现此前已通过实测：会话头形状、UA 版本下限、`bash`+`read`、`stream:true` 四项都已满足，且上线后长期稳定 200。也就是说，**同样的出站请求在 2026-10-11 前后行为不同**。

### 根因

2026-10-11，opencode 把 `/zen/*` 改成了指向新 inference 服务的**薄代理**（`anomalyco/opencode` commit `83f2b6f67a`）。门禁随之从「查会话头**形状**」升级为「查形状 **+ 位不变量**」：

> `X-Opencode-Session` 的 12 位 hex 字段，**低 12 位必须等于 `0xFFE`**。

我们的实现用 `sha256(source)[:6]` 派生 hex（`canonicalOpenCodeID`），本质是随机 48 位，低 12 位恰好是 `0xFFE` 的概率只有 `1/4096`——所以必然 403，且失败形态与「完全没发会话头」一模一样，极易误判成形状问题。

### 决定性证据（单变量消融）

头、UA、body 逐字节相同，**只改会话值**：

| 时刻 (UTC) | `X-Opencode-Session` | hex 12 位 | 低 3 位 | 结果 | cf-ray |
| --- | --- | --- | --- | --- | --- |
| 08:53:58 | `ses_d1bfaf4aff65ysx8xVwhQ12lLq` | `d1bfaf4aff65` | `f65` | **403** `FreeTierError` | `a49d1deed94408af-LAX` |
| 09:15:53 | `ses_ecb74175effeDu4hfV9AoNeKL3` | `ecb74175effe` | **`ffe`** | **200 OK**（SSE） | `a49d3debfa5ecc87-LAX` |

两次请求 body 均为 2644 字节且逐字节相同；唯一的差异就是会话值。

### 官方编码算法

从 opencode 1.19.0 发布产物二进制（偏移 `105055473`）挖出：

```js
var x4=0,cA=0;
function u_(){return tA(!1)}
function oA(){return tA(!0)}          // Session ID 走 descending
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

即 `ses_` + `oA()`：`hex` 是 `~(now*0x1000 + counter)` 的低 48 位，后缀 14 位是 `crypto.getRandomValues` 的 base62。

`counter` 在每次「新的毫秒」时归零再自增，所以**同一毫秒内的第一个 id 的 counter 恒为 1**，此时低 12 位恰好是 `0xFFE`——这正是门禁在查的指纹。三条真实客户端 id 反推自洽：`(hex >> 12) + mintedAtMillis == (27<<36) - 1` 且 `low12 == 0xffe`。

## What Changes

- 新增门禁位不变量常量与判定：`openCodeGateSessionLowHex = "ffe"`、`openCodeGateSessionEpochMillis = int64(27) << 36`、`isOpenCodeGateSessionID(id) bool`（形状 **且** 位不变量）。
- 新增按官方编码的铸造函数：`mintOpenCodeSessionID()`（随机后缀，兜底路径）与 `mintOpenCodeSessionIDForSource(source)`（确定性后缀 + 缓存，常规路径）。hex 段 `((epoch - 1 - now) << 12) | 0xFFE`，与官方 `tA(descending=true, counter=1)` **逐位相同**。
- 新增**有界**会话铸造缓存（容量 4096，FIFO 淘汰）：官方编码含时间戳，不缓存则每轮换 id、击穿上游 prompt cache。
- 改三处调用点：客户端会话值**必须过 `isOpenCodeGateSessionID`** 才原样透传，否则重铸；`canonicalOpenCodeID("ses", source)` 改走缓存铸造；随机兜底改 `mintOpenCodeSessionID()`。
- 修正 `openai_opencode_session.go` 的 UA 注释：UA 版本**不是**本次 403 的原因，也不是门禁检查项（实测插件 UA `1.18.31` 与 `1.19.0` 同样 200）。UA 取值保持 `opencode/1.19.0 …`，保留「低于下限返回 426」这条实测结论。
- 新增排查 Runbook：`docs/opencode-free-tier-gate-runbook.md`。
- 保留 `openai_opencode_freetier_probe_live_test.go` 作为排查工具，重写为 4 个模式并**修复其 dump 泄漏 API key 的缺陷**。

## Non-Goals

- **不改动定价**：`billing_service.go` 保持零 diff；`jev-1.13-free` 目录条目有意保留。
- **不新增「非必需」头**：`X-Opencode-Request` / `X-Opencode-Client` / `X-Opencode-Project` / `Accept` / `X-Session-Affinity` / `X-Session-Id` 已验证非必需，不为「保险」而加。代码里已有的消融注释是正确的，保留。
- 不改 `tool_choice` 方言约束、工具补齐范围等既有行为。
- 不做上游额度/配额管理（那是 429 与 `opencode-warp-rotate-on-429` 的范畴）。

## Impact

- **后端**：`internal/service/openai_opencode_freetier.go`（位不变量、铸造、缓存、三处调用点）、`openai_opencode_session.go`（注释）、两个 live 测试文件。
- **数据库**：无。缓存是进程内 map。
- **管理端 / 前端**：无。
- **上游影响**：仅改变出站 `X-Opencode-Session` 的取值形态；付费模型与 `free_tier_gate=off` 的流量完全不受影响。
- **行为变化**：客户端（含上游插件）送来的**形状合法但位不合法**的 `ses_` 值不再原样透传，会被重铸。这是修复的核心——旧行为会把上游的 403 带进来。
- **已知取舍**：缓存 FIFO 淘汰后该会话换新 id，损失一次 prompt cache 命中（仍过门禁）。
