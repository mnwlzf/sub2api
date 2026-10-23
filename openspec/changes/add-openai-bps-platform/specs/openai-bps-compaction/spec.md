## Purpose

规定 BPS 压缩能力：用真实摘要请求替代客户端历史，以版本化不透明引用在 Redis 保存摘要，并保持轮次与迭代语义，使「压缩 → 新对话 → 工具调用」闭环可用。

## ADDED Requirements

### Requirement: 真实摘要压缩
系统 MUST 对 `/responses/compact` 与带 `compaction_trigger` 的 Responses 请求向 BPS 发起真实摘要请求，并在该请求中关闭客户端工具。压缩 MUST 返回可读的文本摘要与不透明引用，不得用空摘要或伪造引用代替。

#### Scenario: 压缩请求
- **WHEN** 客户端调用 `/responses/compact`
- **THEN** 系统 MUST 向上游发起真实摘要请求
- **THEN** 客户端 MUST 收到文本摘要与 `bpscmp_v1_` 前缀的版本化引用

### Requirement: 引用不外发上游
客户端回传的 `bpscmp_v1_` 引用 MUST 只在服务端解析，MUST NOT 出现在发往 BPS 的请求体中。系统 MUST 用 Redis 中的摘要恢复上下文。

#### Scenario: 恢复压缩上下文
- **WHEN** 客户端在后续请求中携带压缩引用
- **THEN** 系统 MUST 从 Redis 取回摘要并重建上下文，上游 MUST NOT 收到该引用字符串

### Requirement: 轮次与迭代保持
压缩记录 MUST 同时保存 `turn_id` 与迭代基数。恢复后若没有新的用户消息，系统 MUST 保持同一轮次，并仅按新增工具结果递增 `agent_iteration`；重试相同请求 MUST NOT 改变上游请求体。

#### Scenario: 压缩后继续同一轮
- **WHEN** 压缩后客户端仅回传工具结果、没有新的用户消息
- **THEN** 系统 MUST 保持原 `turn_id`，`agent_iteration` MUST 递增 1

### Requirement: 连续压缩与旧记录兼容
系统 MUST 支持连续多次压缩，并 MUST 保留最初轮次锚点。旧版本摘要记录 MUST 仍可读取；新记录 MUST 在跨实例恢复后保持轮次语义。

#### Scenario: 连续压缩
- **WHEN** 会话连续执行两次压缩
- **THEN** 第二次压缩 MUST 保留第一次记录的原始轮次锚点
- **THEN** 恢复后 MUST 能继续同一轮次

### Requirement: 失败不破坏客户端历史
压缩失败时系统 MUST 返回错误，MUST NOT 用不完整摘要替换客户端原始历史。异常断流或终态丢失已完成工具 item 时 MUST NOT 伪造成功。

#### Scenario: 压缩上游失败
- **WHEN** 摘要请求返回 5xx
- **THEN** 系统 MUST 返回错误，客户端原始历史 MUST 保持不变
