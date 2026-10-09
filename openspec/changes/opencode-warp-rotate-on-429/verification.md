# 验证：opencode_go 429 先换 WARP 出口再同账号重试（2026-10-09）

## 交付物

| 项 | 位置 |
| --- | --- |
| 接口与钩子 | `backend/internal/service/opencode_warp_rotate.go` |
| 轮换服务 HTTP 客户端 | `backend/internal/repository/opencode_warp_rotate_service.go` |
| 空闲连接失效 | `backend/internal/repository/http_upstream.go` 的 `CloseUpstreamIdleConnections` |
| 插入点 | `backend/internal/service/openai_gateway_forward.go`、`backend/internal/service/openai_gateway_cc_pipeline.go` |
| 429 冷却抑制 | `backend/internal/service/ratelimit_service.go` 的 `handle429` |
| 配置 | `backend/internal/config/config.go`、`deploy/config.example.yaml` |
| DI | `backend/cmd/server/wire.go` + 重新生成的 `wire_gen.go`、`backend/internal/handler/wire.go` |
| 测试 | `backend/internal/service/opencode_warp_rotate_test.go`、`backend/internal/repository/opencode_warp_rotate_service_test.go` |

## 与调研结论的差异（以实际代码为准）

### 1. 仅把钩子放在 `handleFailoverSideEffects` 之前不足以让重试生效

调研结论 5 是「钩子必须在 `handleFailoverSideEffects` 之前」。逐行核对实际代码后确认这**不充分**：

```
handleFailoverSideEffects
  → handleOpenAIAccountUpstreamError
  → rateLimitService.HandleUpstreamError
  → handle429                                  // opencode_go 未被任何分支吸收
  → applyCNProviderReactive429 → 未命中
  → apply429FallbackRateLimit("no_reset_time") // 默认 5 秒冷却
  → notifyAccountSchedulingBlocked + accountRepo.SetRateLimited
```

handler 拿到 `FailoverContinue` 后 `continue` 回到循环顶部 `SelectAccountWithLoadAwareness`，**被摘出调度快照的账号不会被重新选中**，同账号重试落空。

既有代码已经解决过同一问题：`handle429` 开头对 OpenAI OAuth 通过 `s.runtimeBlocker.(interface{ ShouldRetryOpenAIOAuth429(...) })` 早退。本改动照同一范式新增 `ShouldRetryOpenCodeWarpRotated429(ctx, account, body)`，读取请求 ctx 上的「已轮换」标记。

标记传播链路已用测试固定：

```
rotateOpenCodeWarpExitOn429 → context.WithValue
  → handleOpenAIAccountUpstreamError
  → openAIAccountStateContext(context.WithoutCancel + WithTimeout)  // 均保留 value
  → HandleUpstreamError → withTempUnschedulableModel(WithValue)     // 保留 value
  → handle429
```

对应测试：`TestOpenCodeWarpRotate_MarkerSurvivesToHandle429`（断言冷却与摘号均未发生）。

### 2. 不能把 DI 写在 `handler/wire.go`

`backend/.golangci.yml` 的 depguard 规则 `handler-no-repository` 明确禁止 `internal/handler/**` 依赖 `internal/repository`（`handler/auth_wechat_oauth_test.go` 之所以能 import，是因为它带 `//go:build unit`，golangci-lint 默认不分析带 tag 的文件）。

因此轮换器改由 `cmd/server` 提供（`provideOpenCodeWarpRotator`），`ProvideOpenAIGatewayHandler` 只接收 `service.WarpExitRotator` 接口参数。`wire_gen.go` 用仓库自带版本的 wire 工具重新生成：

```
cd backend && go run github.com/google/wire/cmd/wire ./cmd/server
```

### 3. 行号与调研一致

`openai_gateway_forward.go` 的 429 failover 分支、`openai_gateway_cc_pipeline.go` 的 `failoverOpenAIUpstreamHTTPError`、`openai_account_runtime_block_fastpath.go` 的 OAuth 429 判定、`repository/http_upstream.go` 的 `removeClientLocked` 均与调研描述的位置一致，无需按行号偏移。

## 测试结果

命令（Go 1.27.0，`GOCACHE`/`GOTMPDIR` 指向仓库外缓存目录）：

```
cd backend && go build ./...
cd backend && go test -tags=unit -count=1 ./internal/service/... ./internal/handler/... ./internal/repository/...
```

| 包 | 结果 |
| --- | --- |
| `internal/service` | FAIL：仅 `TestOllamaProbeCallback_StaleLongDoesNotOverrideNewShort`（**既有失败**，见下） |
| `internal/service/openai_ws_v2` | ok |
| `internal/handler` | ok |
| `internal/handler/admin` | ok |
| `internal/handler/dto` | ok |
| `internal/handler/quotaview` | ok |
| `internal/repository` | ok |
| `cmd/server`、`cmd/profit-preview`、`cmd/cleanup-ingress-reject-logs` | ok |

`go build ./...` 与 `go vet ./...` 全绿。

### 既有失败（与本改动无关，未修）

```
--- FAIL: TestOllamaProbeCallback_StaleLongDoesNotOverrideNewShort
    ratelimit_service_ollama_429_test.go:405: stale long callback must not pass the CAS
```

该用例走 Ollama Cloud 429 的 CAS 路径，本改动只在 `handle429` 开头新增了一个 `opencode_go` 分支，不触及该路径。

## 覆盖矩阵

| # | 场景 | 测试 |
| --- | --- | --- |
| 1 | `opencode_go + 429 + FreeUsageLimitError` → rotator 恰 1 次 | `TestOpenCodeWarpRotate_TriggerMatrix` |
| 2 | openai / grok / deepseek + 429 → 0 次 | 同上；另 `TestOpenCodeWarpRotate_CCPipelineIgnoresOtherPlatforms` |
| 3 | `opencode_go + 400/500/403` → 0 次 | 同上 |
| 4 | `opencode_go + 429` 但 body 不含 `FreeUsageLimitError` → 0 次 | 同上（含空 body） |
| 5 | `ProxyID == nil` → 0 次且不报错 | 同上 |
| 6 | rotator 返回 error / ctx 超时 → 不 panic，走原 failover | `TestOpenCodeWarpRotate_RotatorFailureFallsBack` |
| 7 | 并发 10 goroutine → singleflight 只调 1 次 | `TestOpenCodeWarpRotate_Concurrent429CollapsesToOneRotation` |
| 8 | service 级：429 → `RetryableOnSameAccount` / `SameAccountRetryMax` / `Retry-After` / deadline 为零 / idle-conn 顺序 | `TestOpenCodeWarpRotate_ForwardMarksSameAccountRetryAndClosesIdleConnections`、`TestOpenCodeWarpRotate_ForwardWithoutRotationKeepsLegacyFailover` |
| 9 | CC 路径 `failoverOpenAIUpstreamHTTPError` | `TestOpenCodeWarpRotate_CCPipelineMarksSameAccountRetry` |
| 10 | `handle429` 冷却抑制与标记传播 | `TestOpenCodeWarpRotate_Suppresses429CooldownOnlyWhenRotated`、`TestOpenCodeWarpRotate_MarkerSurvivesToHandle429` |
| 附加 | 最小间隔跳过、不同 proxy 独立轮换、账号级 on/off 覆盖全局、未注入 rotator 为 no-op | `TestOpenCodeWarpRotate_MinIntervalSkipsRepeatRotation`、`TestOpenCodeWarpRotate_DifferentProxiesRotateIndependently`、TriggerMatrix 的账号级用例、`TestOpenCodeWarpRotate_NoRotatorInjectedIsNoop` |
| 附加 | repository：只关匹配 proxyKey 的空闲连接、不删缓存条目、幂等；轮换客户端成功/503/ok=false/不可用/坏 JSON/超时/非法 proxy_id | `TestCloseUpstreamIdleConnections_*`、`TestOpenCodeWarpRotator_*` |

## 回归

`openai_account_runtime_block_fastpath_test.go`、`gateway_pool_mode_retry_test.go`、`account_pool_*_test.go`、`openai_opencode_freetier_test.go`、`openai_opencode_session_test.go`、`failover_loop_test.go` 全部保持通过。

## 未覆盖 / 不确定

1. **GitHub Actions 未触发（非本改动引起）**：PR #20 创建后既没有 `CI` 也没有任何其它 workflow run。用对照实验确认这是仓库级现象而非分支问题——从 `main` 新建一个内容完全相同的分支并推送，同样产生 0 个 run：
   ```
   git branch -f ci-probe main && git push origin ci-probe
   gh api "repos/mnwlzf/sub2api/actions/runs?per_page=3"   # 最新仍是 2026-10-08T18:41 的旧 run
   gh api "repos/mnwlzf/sub2api/commits/<sha>/check-suites"  # 只有 vercel 一个 suite，无 Actions
   ```
   该分支已删除。仓库本身正常（`archived=false`、`disabled=false`、`visibility=public`，5 个 workflow 均为 `state=active`），最后一次成功 run 是 2026-10-08T18:41。仓库级 Actions 开关无法用当前 token 读取（`repos/{owner}/{repo}/actions/permissions*` 全部返回 403，需要 fine-grained "Actions policies" 权限），因此**无法在本会话内定位或修复**。CI 状态：**未运行 / 无 check**。
2. **golangci-lint v2.13 未在本地运行**：安装因 module checksum mismatch 失败（经代理下载的 `ryanrolds/sqlclosecheck@v0.6.0` 与 sum.golang.org 记录不一致），系统内也没有现成二进制。已用 `go build ./...`、`go vet ./...`、`gofmt -l` 替代；depguard 的 `handler-no-repository` 规则已人工核对（handler 无 repository import）。
3. **未做真实上游端到端验证**：轮换服务 `warp-rotate:9110` 与生产账号 31568 均未在本地接入，验证停留在单元/服务级替身。
4. **`min_interval_seconds` 默认值**：示例配置给 30，代码里 `<= 0` 表示不额外限流。生产灰度时需确认该值是否合适——它决定「同一 proxy 多久内只允许一次轮换」。
5. **轮换服务耗时会占用客户端请求**：服务端硬超时约 90 秒，期间请求处于等待。若客户端超时短于该值，可能先断开；此时 ctx 取消会让轮换返回 `timeout` 并回落原 failover。
6. **`Retry-After` 透传**：轮换成功后上游 429 的 `Retry-After` 仍保留在 failover 错误里。同账号重试预算耗尽后客户端仍会收到 429 与 `Retry-After`——这是预期行为（本改动只是让重试有机会发生，不改变耗尽后的对外语义）。
7. **未验证 handler 层端到端循环**：`gateway_handler_responses.go` 的 `FailoverContinue → continue → SelectAccountWithLoadAwareness` 依赖真实调度快照，本地未搭建该集成环境。缓解：`TestOpenCodeWarpRotate_MarkerSurvivesToHandle429` 断言了「不写冷却、不摘号」，这正是重试能被重新选中的前提。
