## 1. 配置与依赖注入

- [x] 1.1 `config.go` 新增 `GatewayOpenCodeWarpRotateConfig`（`enabled` / `service_url` / `token` / `timeout_seconds` / `max_rotations_per_request` / `min_interval_seconds`），字段挂到 `GatewayConfig` 的 `opencode_warp_rotate`
- [x] 1.2 `service/opencode_warp_rotate.go` 定义 `WarpExitRotator` 接口
- [x] 1.3 `SetWarpExitRotator` setter 注入（照 `SetPluginManager`），**不改** 22 参数构造函数
- [x] 1.4 `repository/opencode_warp_rotate_service.go` 实现 HTTP 客户端：`POST {service_url}/ensure-fresh`、Bearer token、`{"proxy_id":N}`、95s 默认超时、显式清空 `Transport.Proxy`（站内服务不能被 HTTP_PROXY 劫持）
- [x] 1.5 `service_url` 为空时 `NewOpenCodeWarpRotator` 返回 nil（no-op）
- [x] 1.6 `handler/wire.go` 的 `ProvideOpenAIGatewayHandler` 里 `gatewayService.SetWarpExitRotator(repository.NewOpenCodeWarpRotator(cfg))`（不改 wire 图签名，无需重新生成 `wire_gen.go`）
- [x] 1.7 `deploy/config.example.yaml` 新增配置块与注释

## 2. 触发判定与账号级开关

- [x] 2.1 `isOpenCodeFreeUsageLimit429`：平台 + 429 + 响应体，三重限定
- [x] 2.2 合法 JSON 只认结构化字段（`error.type` 等），非法 JSON 才退回子串匹配
- [x] 2.3 `openCodeWarpRotateMode` 读取 `credentials.warp_rotate_on_429`，缺省/非法回落 auto
- [x] 2.4 `shouldRotateOpenCodeWarpExit`：`off` 关闭 / `on` 强制开启（可全局关闭时灰度）/ 其他跟随全局 `enabled`
- [x] 2.5 `ProxyID == nil` 或无代理绑定时跳过且不报错

## 3. 并发去重

- [x] 3.1 按 `proxyID` 做 `singleflight.Group` 去重
- [x] 3.2 `min_interval_seconds` 在 singleflight 内部判定（被合并的调用拿到同一结论）
- [x] 3.3 失败也记录时间戳，避免轮换服务不可用时被打满

## 4. 空闲连接失效

- [x] 4.1 `repository/http_upstream.go` 新增 `CloseUpstreamIdleConnections(proxyURL, accountID)`
- [x] 4.2 按 `proxyKey` 匹配、只调 `CloseIdleConnections()`、不删缓存条目（不打断在途请求）
- [x] 4.3 service 侧用可选接口断言调用，不改 `HTTPUpstream` 接口定义
- [x] 4.4 顺序保证：先轮换、后关空闲连接

## 5. 收口点接入

- [x] 5.1 `openai_gateway_forward.go`：`handleFailoverSideEffects` **之前**插入钩子，轮换成功时把 `retryableOnSameAccount` 置 true 传给 `newOpenAIAccountFailoverError`
- [x] 5.2 `openai_gateway_cc_pipeline.go`：`failoverOpenAIUpstreamHTTPError` 同一钩子
- [x] 5.3 `applyOpenCodeWarpRotateRetryBudget`：只设 `SameAccountRetryMax`（默认 2）+ 300ms 延迟，**不设** `SameAccountRetryDeadline`
- [x] 5.4 不选 `newOpenAIAccountFailoverErrorWithClassificationHeaders`（纯构造函数、复用面广、在副作用之后）

## 6. 429 冷却抑制（调研结论的必要修正）

- [x] 6.1 钩子返回 `(ctx, rotated)`，用 ctx value 标记「本次请求已轮换」
- [x] 6.2 `ShouldRetryOpenCodeWarpRotated429(ctx, account, body)` 供 `RateLimitService` 断言
- [x] 6.3 `ratelimit_service.go` 的 `handle429` 增加可选接口断言并早退（照 `ShouldRetryOpenAIOAuth429` 先例）
- [x] 6.4 验证 `context.WithoutCancel` + `WithTimeout` 保留 value，标记能到达 `handle429`

## 7. 可观测

- [x] 7.1 `appendOpenCodeWarpRotateOps`：ops 事件 `Kind = "opencode_warp_rotate"`，`UpstreamStatusCode = 0`（不干扰 failover 事件监控）
- [x] 7.2 原因分类：`rotated` / `failed` / `timeout` / `skipped_disabled` / `skipped_no_proxy` / `skipped_min_interval`
- [x] 7.3 单飞合并以 `shared=singleflight` 标记
- [x] 7.4 结构化 `slog.Info("opencode_warp_rotate", ...)`

## 8. 测试

- [x] 8.1 `opencode_go + 429 + FreeUsageLimitError` → 恰 1 次
- [x] 8.2 openai / grok / deepseek + 429 → 0 次
- [x] 8.3 `opencode_go + 400/500/403` → 0 次
- [x] 8.4 `opencode_go + 429` 但 body 不含 `FreeUsageLimitError` → 0 次
- [x] 8.5 `ProxyID == nil` → 0 次且不报错
- [x] 8.6 rotator 返回 error / ctx 超时 → 不 panic，回落原 failover
- [x] 8.7 并发 10 goroutine → singleflight 只调 1 次
- [x] 8.8 service 级 `/responses`：断言 `RetryableOnSameAccount`、`SameAccountRetryMax`、`Retry-After` 保留、deadline 为零、idle-conn 清理被调用且顺序在 rotator 之后
- [x] 8.9 CC 路径 `failoverOpenAIUpstreamHTTPError` 同样覆盖（含其他平台 0 次）
- [x] 8.10 `handle429` 冷却抑制：带标记不写冷却、不带标记保持原行为、非 opencode_go 平台不抑制
- [x] 8.11 repository：`CloseUpstreamIdleConnections` 只关匹配 proxyKey、不删条目；轮换服务客户端成功/503/ok=false/坏 JSON/超时/非法 proxy_id
- [x] 8.12 回归：`openai_account_runtime_block_fastpath_test.go`、`gateway_pool_mode_retry_test.go`、`account_pool_*_test.go`、`openai_opencode_freetier_test.go`、`openai_opencode_session_test.go`

## 9. 验证

- [x] 9.1 `go build ./...`
- [x] 9.2 `go test -tags=unit ./internal/service/... ./internal/handler/... ./internal/repository/...`
- [x] 9.3 提 PR #20（未自行合并）
- [ ] 9.4 CI 状态：**未运行**。PR 与分支推送均未产生任何 workflow run，对照实验（从 main 新建同内容分支并推送）同样为 0 个 run，属仓库级现象，与本次改动无关。详见 `verification.md` 的「未覆盖 / 不确定」第 1 条
