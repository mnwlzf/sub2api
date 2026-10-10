## Why

生产账号 31568（`platform=opencode_go`、`type=apikey`、`proxy_id=17` = `socks5h://warp-test:1080`）在免费层配额耗尽后，客户端稳定收到 `429` + `Retry-After: 49711`（约 13.8 小时）。

opencode 免费层的配额**按出口 IP 记账**，每日 00:00 UTC 重置。上游 429 响应体是：

```json
{"type":"error","error":{"type":"FreeUsageLimitError","message":"Rate limit exceeded. Please try again later."}}
```

关键事实：**换出口不需要改 proxy 绑定**——SOCKS5 端点不变，变的只是它背后的 WARP 出口 IP。

现状（逐行核对过）：429 时 sub2api **完全不会同账号重试**。

| 路径 | 现状 | 结果 |
| --- | --- | --- |
| `shouldRetryOpenAIOAuth429OnSameAccountWithResponse`（`service/openai_account_runtime_block_fastpath.go`） | `isOpenAIOAuthAccount` 要求 OpenAI OAuth/SetupToken，opencode_go apikey 恒 false | 不重试 |
| 池模式分支 | 账号 `credentials` 里没有 `pool_mode` | 不重试 |
| 结论 | 排除账号 → 同组无其他可用账号 | 客户端收 429 |

## What Changes

- 新增可选注入的 `WarpExitRotator`（`service/opencode_warp_rotate.go`）与外部轮换服务 HTTP 客户端（`repository/opencode_warp_rotate_service.go`）。**不改** `NewOpenAIGatewayService` 的 22 参数构造函数，用 setter 注入（照 `SetPluginManager`）。
- 在两个共用的 failover 收口点挂载轮换钩子，**三重限定**（`Platform == PlatformOpenCodeGo` 且 `statusCode == 429` 且响应体含 `FreeUsageLimitError`）：
  - `service/openai_gateway_forward.go`（`/v1/responses` 必经点，`handleFailoverSideEffects` 之前）；
  - `service/openai_gateway_cc_pipeline.go`（`failoverOpenAIUpstreamHTTPError`，`/chat/completions` 与 CC 回退链的收口点）。
- 轮换成功后把该次 failover 标为可在同账号上重试：`RetryableOnSameAccount=true` + `SameAccountRetryMax`（默认 2）+ 小延迟。**刻意不设 `SameAccountRetryDeadline`**——一旦非零，`handler/failover_loop.go` 会绕过次数上限在窗口内无限重试。
- **轮换后显式关闭该代理的空闲连接**（`repository/http_upstream.go` 新增 `CloseUpstreamIdleConnections`，用可选接口断言调用）。OpenAI profile + `socks5h` 走 `upstreamProtocolModeOpenAIH2`，一条 HTTP/2 隧道被多路复用、`IdleConnTimeout=90s`；不关空闲连接的话重试会复用旧隧道，功能静默失效。
- 按 `proxyID` 做 singleflight 并发去重，再叠一个可配置的最小间隔（`min_interval_seconds`）防止抖动。
- 新增配置块 `gateway.opencode_warp_rotate.{enabled, service_url, token, timeout_seconds, max_rotations_per_request, min_interval_seconds}`，`enabled` 默认 **false**（合并即安全）；同时支持账号级凭据开关 `warp_rotate_on_429`（照 `credentials.free_tier_gate` 范式）便于灰度。
- 新增 ops 事件与结构化日志（轮换成功/失败/超时/跳过/单飞合并），`Kind = "opencode_warp_rotate"`，`UpstreamStatusCode` 留 0 以免干扰 failover 事件的监控语义。
- **额外必要改动**：`RateLimitService.handle429` 增加可选接口断言 `ShouldRetryOpenCodeWarpRotated429`。仅把钩子放在 `handleFailoverSideEffects` 之前并不足以让重试生效——`handle429` 仍会写 5 秒冷却并摘号，下一次选号就把账号排除。这与既有 OpenAI OAuth 分支（`ShouldRetryOpenAIOAuth429`）是同一类问题、同一套解法。

## Capabilities

### New Capabilities

- `opencode-warp-rotate-on-429`：opencode_go 免费层 429 时先轮换 WARP 出口 IP、再在同一账号上重试的完整链路，包含三重限定触发、并发去重、空闲连接失效、同账号重试预算、账号级灰度开关与可观测。

## Non-goals

- **不修改账号 credential 的 `pool_mode`**，池模式语义保持不变。
- **不碰免费层门禁逻辑**（403 `FreeTierError` 与 429 `FreeUsageLimitError` 是两回事）。
- **不扫数据库错误表、不做定时轮询**：只在「当次请求的错误路径」上触发。
- 不修改 `billing_service.go` 的定价逻辑（零 diff）。
- 不新增数据库列、不需要迁移。
