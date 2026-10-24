## Purpose

规定 BPS 的计费、错误分类、冷却与重试边界，使成本核算与故障处置与既有平台一致，并避免「一次 403 拖垮整号」「限流无限重试」等误伤。

## ADDED Requirements

### Requirement: 按上游 usage 计费
系统 MUST 按上游返回的 `usage` 记录输入、缓存与输出 token，并沿用既有分组/渠道价格、倍率、用量日志与用户平台配额。上游成本 MUST 由管理员在分组配置，MUST NOT 从 Excel 点数或 Codex 配额推断。

#### Scenario: 正常计费
- **WHEN** BPS 请求成功并返回 usage
- **THEN** 用量日志 MUST 记录输入/缓存/输出 token 与对应费用

#### Scenario: 失败响应含 usage
- **WHEN** 请求失败但上游仍返回 usage
- **THEN** 系统 MUST 记录该次用量

### Requirement: 认证失败停止调度
上游返回 401 或令牌撤销时，系统 MUST 停止该账号调度，并 MUST 保留管理员主动停用/关闭调度状态。认证失败写入 MUST 以本次请求的 Access Token 与账号 ID 为条件，迟到响应 MUST NOT 标记新凭证失效。

#### Scenario: 迟到 401
- **WHEN** 账号已替换新 token，旧 token 请求的 401 迟到返回
- **THEN** 系统 MUST NOT 把新 token 标记为认证失败

### Requirement: 模型权限错误按模型冷却
`basispoints_model_access_changed` 与 `model_not_allowed` MUST 统一归一为 `bps_model_not_allowed`，并按映射后的上游模型冷却 30 分钟；账号其它模型 MUST 继续可用。原始错误码 MUST 保留在诊断信息中。

#### Scenario: 单模型无权限
- **WHEN** 账号对 `gpt-6-astra` 无权限而对其余模型有权限
- **THEN** 系统 MUST 只冷却 `gpt-6-astra`，其它模型请求 MUST 正常调度

### Requirement: 普通 403 不判定整号失效
普通 403 MUST 按单次请求失败处理，MUST NOT 直接停用整个账号。

#### Scenario: 单次 403
- **WHEN** 上游对一次请求返回普通 403
- **THEN** 账号 MUST 保持可调度，请求 MUST 按失败返回

### Requirement: 重试与冷却边界
参数错误（400）MUST NOT 重试；429 MUST 按 `Retry-After` 冷却；网络错误与 5xx 仅在「未绑定上下文且尚未输出」的新会话上允许有限换号。

#### Scenario: 429 冷却
- **WHEN** 上游返回 429 并带 `Retry-After: 30`
- **THEN** 系统 MUST 冷却该账号至少 30 秒后再调度

#### Scenario: 已绑定会话遇 5xx
- **WHEN** 已绑定上下文的会话遇到 5xx
- **THEN** 系统 MUST NOT 换号重放，MUST 向客户端返回失败

### Requirement: 诊断与快照一致性
认证失败 MUST 在同一事务中写入诊断信息与调度快照通知。

#### Scenario: 认证失败落库
- **WHEN** 请求因 401 标记账号认证失败
- **THEN** 诊断记录与调度快照通知 MUST 同时可见，不得只写其一
