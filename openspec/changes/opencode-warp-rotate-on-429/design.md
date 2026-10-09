## Context

opencode 免费层配额按出口 IP 记账。账号 31568 绑定的 `socks5h://warp-test:1080` 是 WARP 端点，端点后面的出口 IP 可以被外部服务轮换掉。因此「换出口 → 同账号重试」不需要触碰账号的 proxy 绑定，也不需要改 `pool_mode`。

轮换服务（已按冻结契约实现，服务端独立部署）：

```
POST http://warp-rotate:9110/ensure-fresh
  Header: Authorization: Bearer <token>
  Body:   {"proxy_id": 17}
  200 → {"ok":true,"action":"none"|"rotated","usable":true,"exit_ip":"2a09:...",
         "attempts":0,"elapsed_seconds":1.2,
         "prefixes":{"slash32":"...","slash48":"...","slash64":"..."}}
  503 → {"ok":false,"usable":false,"error":"no_usable_exit",
         "exit_ip":"...","attempts":6,"retry_after":49711}
GET /status   /healthz
```

语义：当前出口可用则直接返回（`action=none`，不轮换）；不可用才轮换到可用为止。**服务端硬超时约 90 秒**。

## Goals / Non-Goals

- Goals：429 时同账号重试真正生效；轮换失败/超时严格回落到改动前的行为；默认关闭；可灰度；可观测。
- Non-Goals：见 proposal.md 的 Non-goals。

## Decisions

### 1. 触发点选在 failover 收口点，而不是错误构造函数

选 `openai_gateway_forward.go` 的 `/responses` failover 分支与 `openai_gateway_cc_pipeline.go` 的 `failoverOpenAIUpstreamHTTPError`。

不选 `newOpenAIAccountFailoverErrorWithClassificationHeaders`：它是纯构造函数，被十几条路径复用，且在所有副作用之后才执行，无法影响 `handle429` 的行为。

### 2. 三重限定，缺一不可

插入点被所有平台共用。触发条件收敛为一个纯函数：

```go
isOpenCodeFreeUsageLimit429(account, statusCode, body)
  = account.IsOpenCodeGo() && statusCode == 429 && body 含 FreeUsageLimitError
```

合法 JSON 时只认结构化字段（`error.type` / `response.error.type` / `detail.type` / `type`），非法 JSON 才退回子串匹配，避免把 message 里引用该类型名的响应误判。

### 3. 轮换后必须显式关空闲连接

OpenAI profile + `socks5h` 代理 → `upstreamProtocolModeOpenAIH2` → HTTP/2 多路复用一条 TCP 隧道，空闲连接池 `IdleConnTimeout=90s`。不关的话，重试直接复用轮换前的旧隧道，出口 IP 根本没变。

实现放在 `repository/http_upstream.go`（复用 `removeClientLocked` 已验证的 `client.CloseIdleConnections()`），按 `proxyKey` 匹配、只关空闲连接、不删缓存条目，因此不会打断在途请求。调用侧用**可选接口断言**，避免修改 `service.HTTPUpstream` 接口定义牵连所有测试 stub：

```go
if closer, ok := s.httpUpstream.(interface {
    CloseUpstreamIdleConnections(proxyURL string, accountID int64)
}); ok { closer.CloseUpstreamIdleConnections(proxyURL, account.ID) }
```

顺序是硬约束：**先轮换、再关空闲连接**，测试用事件序列断言。

### 4. 并发去重：singleflight + 最小间隔

`singleflight.Group` 按 `proxyID` 做 key（仓库先例：`cn_provider_quota_service.go`）。最小间隔在 singleflight 内部判定，因此被合并的调用拿到同一个「跳过」结论。同一 proxy 上并发的 N 个 429 只产生一次轮换调用。

`min_interval_seconds <= 0` 表示不额外限流（并发去重始终由 singleflight 保证）；示例配置给 30。

### 5. 同账号重试预算：只用 SameAccountRetryMax

`handler/failover_loop.go` 的 `sameAccountRetryAllowed`：`SameAccountRetryMax > 0` 时按次数上限封顶；一旦 `SameAccountRetryDeadline` 非零则**绕过次数上限**，在窗口内无限重试。因此只设置 `SameAccountRetryMax`（默认 2）+ `SameAccountRetryDelay = 300ms`，绝不设 deadline。

### 6. 必须抑制 429 冷却，否则重试静默落空（调研结论的修正）

调研结论是「钩子放在 `handleFailoverSideEffects` 之前即可」。逐行核对实际代码后确认**这还不够**：

- `handleFailoverSideEffects` → `handleOpenAIAccountUpstreamError` → `rateLimitService.HandleUpstreamError`；
- opencode_go 走 `applyCNProviderReactive429` 未命中 → 最终 `apply429FallbackRateLimit(ctx, account, "no_reset_time")` → `notifyAccountSchedulingBlocked` + `accountRepo.SetRateLimited`（默认 5 秒冷却）；
- handler 收到 `FailoverContinue` 后 `continue` 回到循环顶部 `SelectAccountWithLoadAwareness`，**被摘出调度快照的账号不会被重新选中**，同账号重试落空。

既有代码里 OpenAI OAuth 429 已经解决过同一问题：`handle429` 通过 `s.runtimeBlocker.(interface{ ShouldRetryOpenAIOAuth429(...) })` 早退。本改动照同一范式新增 `ShouldRetryOpenCodeWarpRotated429(ctx, account, body)`，由网关服务读取请求 ctx 上的「已轮换」标记。

标记通过返回值传播：钩子返回 `(ctx, rotated)`，调用方把新 ctx 传给 `handleFailoverSideEffects`。`openAIAccountStateContext` 用 `context.WithoutCancel` + `WithTimeout`，两者都保留 value，标记能一路到达 `handle429`。

### 7. 配置与账号级灰度

`gateway.opencode_warp_rotate.enabled` 默认 false。账号凭据 `warp_rotate_on_429`：

| 取值 | 行为 |
| --- | --- |
| `on` | 该账号显式开启，**全局关闭时也可用**（灰度） |
| `off` | 该账号显式关闭（即使全局已开启） |
| 缺失 / `auto` / 非法 | 跟随全局 `enabled` |

合并默认（全局 false、无账号凭据）时整个能力为 no-op。`service_url` 为空时 `NewOpenCodeWarpRotator` 返回 nil，不注入，同样 no-op。

### 8. 失败处理

钩子自身绝不返回错误、绝不 panic。轮换失败（含 503 `no_usable_exit`、超时、未配置、无代理绑定）时返回 `(ctx, false)`，调用方走原有 failover 分支，客户端行为与改动前一致。

## Risks / Trade-offs

| 风险 | 处理 |
| --- | --- |
| 轮换服务不可用导致请求被拖到 90 秒 | `timeout_seconds` 客户端超时；失败即回落；最小间隔节流重复尝试 |
| 轮换风暴 | singleflight（并发）+ `min_interval_seconds`（串行抖动） |
| 误判其他平台 429 | 三重限定 + 结构化 JSON 解析 |
| 复用旧隧道导致静默失效 | 轮换后显式 `CloseIdleConnections`，事件序列断言 |
| 无限重试 | 只设 `SameAccountRetryMax`，不设 deadline |
| 账号被冷却摘号导致重试落空 | `handle429` 可选接口抑制（照 OAuth 429 先例） |
| 影响其他部署 | `enabled` 默认 false + `service_url` 为空即 no-op |
