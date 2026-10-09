## Purpose

规定 Sub2API 在 `opencode_go` 账号遇到免费层配额耗尽（`429` + `FreeUsageLimitError`）时，先通过外部轮换服务换掉该账号代理端点背后的 WARP 出口 IP，再用**同一账号**重试，使请求不必等到每日 00:00 UTC 配额重置。

## ADDED Requirements

### Requirement: 三重限定触发

系统 MUST 仅在同时满足以下三个条件时触发出口轮换：

1. 账号 `platform` 为 `opencode_go`；
2. 上游 HTTP 状态码为 `429`；
3. 上游响应体标识错误类型为 `FreeUsageLimitError`。

任一条件不满足时，系统 MUST NOT 调用轮换服务，MUST 保持改动前的 failover 行为。

#### Scenario: 命中免费层配额耗尽

- **WHEN** opencode_go 账号收到 `429` 且响应体为 `{"type":"error","error":{"type":"FreeUsageLimitError",...}}`
- **THEN** 系统 MUST 调用轮换服务恰好一次（并发场景见「并发去重」）

#### Scenario: 其他平台的 429

- **WHEN** `openai` / `grok` / `deepseek` 等非 opencode_go 账号收到 `429`
- **THEN** 系统 MUST NOT 调用轮换服务

#### Scenario: opencode_go 的非 429 错误

- **WHEN** opencode_go 账号收到 `400` / `403` / `500`
- **THEN** 系统 MUST NOT 调用轮换服务

#### Scenario: 429 但不是免费层配额

- **WHEN** opencode_go 账号收到 `429` 且响应体不含 `FreeUsageLimitError`
- **THEN** 系统 MUST NOT 调用轮换服务

### Requirement: 出口轮换不改变代理绑定

系统 MUST NOT 因本能力修改账号的代理绑定或账号凭据。轮换只作用于代理端点背后的出口 IP。

#### Scenario: 无代理绑定

- **WHEN** 账号 `proxy_id` 为空
- **THEN** 系统 MUST 跳过轮换且 MUST NOT 返回错误
- **THEN** 请求 MUST 继续走原有 failover 分支

### Requirement: 轮换后同账号重试

轮换成功时，系统 MUST 把该次 failover 标记为可在同一账号上重试，并 MUST 施加一个有限的次数上限。系统 MUST NOT 设置同账号重试截止时间（`SameAccountRetryDeadline`），因为非零截止时间会让 handler 绕过次数上限。

#### Scenario: 轮换成功

- **WHEN** 轮换服务返回可用出口
- **THEN** 返回的 failover 错误 MUST 满足 `RetryableOnSameAccount == true`
- **THEN** 该错误 MUST 带非零的同账号重试次数上限
- **THEN** 该错误的同账号重试截止时间 MUST 为零值
- **THEN** 上游的 `Retry-After` 等响应头 MUST 原样保留

#### Scenario: 轮换失败或超时

- **WHEN** 轮换服务返回错误、返回不可用出口，或请求超时
- **THEN** 系统 MUST NOT panic
- **THEN** 系统 MUST NOT 把该次 failover 标记为可同账号重试
- **THEN** 请求 MUST 继续走原有 failover 分支

### Requirement: 轮换后关闭该代理的空闲连接

HTTP/2 多路复用的空闲隧道会复用轮换前建立的连接。系统 MUST 在轮换成功后、发起重试前，显式关闭该代理对应客户端的空闲连接，且 MUST NOT 打断在途请求。

#### Scenario: 轮换成功后的连接清理

- **WHEN** 出口轮换成功
- **THEN** 系统 MUST 对匹配该代理的客户端调用 `CloseIdleConnections`
- **THEN** 该调用 MUST 发生在轮换调用之后
- **THEN** 系统 MUST NOT 从客户端缓存中删除条目

### Requirement: 并发去重与抖动抑制

系统 MUST 按代理 ID 对并发轮换请求做去重；MUST 支持一个可配置的最小间隔以抑制串行抖动。

#### Scenario: 同一代理的并发 429

- **WHEN** 同一代理上并发的 N 个请求同时命中免费层 429
- **THEN** 轮换服务 MUST 只被调用一次
- **THEN** 全部 N 个请求 MUST 观察到同一次轮换的结果

#### Scenario: 最小间隔内的重复 429

- **WHEN** 同一代理在上一次轮换后的最小间隔内再次命中 429，且配置了非零最小间隔
- **THEN** 系统 MUST 跳过本次轮换并走原有 failover 分支

### Requirement: 已轮换请求不得被 429 冷却摘号

系统 MUST 在本次请求已成功轮换出口时，跳过该账号的 429 冷却与调度封禁；否则账号会在下一次选号时被排除，同账号重试静默失效。

#### Scenario: 已轮换的 429

- **WHEN** 本次请求已成功轮换出口，且随后进入账号级 429 错误处置
- **THEN** 系统 MUST NOT 持久化限流重置
- **THEN** 系统 MUST NOT 将该账号标记为不可调度

#### Scenario: 未轮换的 429

- **WHEN** 本次请求未轮换出口
- **THEN** 系统 MUST 保持改动前的 429 冷却行为

### Requirement: 默认关闭与账号级灰度

系统 MUST 默认关闭本能力，使未配置轮换服务的部署行为完全不变。系统 MUST 支持账号级开关以按账号灰度。

#### Scenario: 默认配置

- **WHEN** 未配置 `gateway.opencode_warp_rotate` 或 `enabled` 为 false
- **THEN** 系统 MUST NOT 调用轮换服务

#### Scenario: 账号级开启

- **WHEN** 全局 `enabled` 为 false，但账号凭据 `warp_rotate_on_429` 为 `on`
- **THEN** 系统 MUST 对该账号启用出口轮换

#### Scenario: 账号级关闭

- **WHEN** 全局 `enabled` 为 true，但账号凭据 `warp_rotate_on_429` 为 `off`
- **THEN** 系统 MUST NOT 对该账号启用出口轮换

### Requirement: 可观测

系统 MUST 为轮换的成功、失败、超时、跳过与并发合并记录 ops 事件与结构化日志。事件 MUST NOT 影响 failover 事件的监控语义。

#### Scenario: 轮换事件

- **WHEN** 发生任意一次轮换尝试
- **THEN** 系统 MUST 记录一条带原因分类的 ops 事件
- **THEN** 被 singleflight 合并的调用 MUST 被标记为共享
