## Purpose

规定 OpenAI BPS 平台与账号的生命周期：平台标识、凭证形态、JWT 解析、状态保留与只读凭证状态，确保账号在调度、编辑、测试、停用各环节行为可预测。

## ADDED Requirements

### Requirement: 独立平台标识
系统 MUST 提供平台值 `openai_bps`（显示名 OpenAI BPS），与 `openai`、`anthropic` 等既有平台并列。BPS 账号 MUST 仅支持 `oauth` 类型，MUST NOT 被 OpenAI 平台的调度、模型目录、计费路径选中；反之亦然。

#### Scenario: 平台隔离
- **WHEN** 一个分组同时包含 OpenAI 账号与 BPS 账号
- **THEN** BPS 分组 MUST 只使用 BPS 账号，OpenAI 分组 MUST 只使用 OpenAI 账号
- **THEN** Composite 分组 MUST 通过显式路由选择 BPS，普通 `gpt-*` 自动识别 MUST 仍选择 OpenAI

### Requirement: Access Token 凭证
账号凭证 MUST 以 `credentials.access_token` 保存 ChatGPT Access Token。`credentials.chatgpt_account_id` MUST 可选：填写时覆盖从 JWT 提取的账号标识。系统 MUST NOT 接受账号级或客户端级的上游地址覆盖。

#### Scenario: 留空账号 ID
- **WHEN** 创建 BPS 账号时只填写 Access Token
- **THEN** 系统 MUST 从 JWT payload 解析 `chatgpt_account_id` 并用于上游请求头
- **THEN** 解析 MUST NOT 验签，且解析失败 MUST NOT 阻断账号创建

#### Scenario: 覆盖账号 ID
- **WHEN** 账号同时填写了 Access Token 与 `chatgpt_account_id`
- **THEN** 上游请求 MUST 使用填写的账号 ID，而不是 JWT 中的值

### Requirement: 到期时间与凭证状态
系统 MUST 从 JWT payload 提取 `exp` 作为本地到期时间；无 `exp` 时 MUST 视为无到期信息。凭证状态 MUST 至少区分：正常、自然过期、上游撤销、认证失败，并 MUST 以只读字段 `bps_credential_state` 暴露状态、到期时间、最近发现时间与错误码。凭证 MUST 在管理端脱敏展示。

#### Scenario: 自然过期
- **WHEN** 当前时间超过 JWT `exp`
- **THEN** 该账号 MUST 在调度前被排除
- **THEN** 管理端 MUST 显示「已过期」且提示每 30 秒更新，不依赖列表自动刷新

#### Scenario: 上游撤销
- **WHEN** 正常请求或连接测试收到 401/令牌撤销
- **THEN** 系统 MUST 记录认证失败并停止该账号调度
- **THEN** 认证失败记录 MUST 只更新与本次请求 Access Token 与账号 ID 匹配的凭证，迟到响应 MUST NOT 标记新凭证失效

### Requirement: 状态保留语义
管理员主动停用、关闭调度、暂停状态 MUST NOT 因替换 Access Token 而自动解除。编辑账号时留空凭证 MUST 保留原 Access Token 与已有诊断信息。

#### Scenario: 替换 token 不解除停用
- **WHEN** 管理员对已主动停用的 BPS 账号替换新的 Access Token 并保存
- **THEN** 该账号 MUST 仍保持停用/关闭调度状态，直到管理员显式恢复

### Requirement: 创建与编辑的风险确认
创建或保存 BPS 账号前 MUST 显示账号限制/封禁风险提示并要求二次确认；取消 MUST NOT 创建账号且 MUST 保留已填输入。

#### Scenario: 取消风险确认
- **WHEN** 管理员在风险确认弹窗点击取消
- **THEN** 系统 MUST NOT 创建或更新账号，表单输入 MUST 保留

### Requirement: 连接测试
系统 MUST 提供「保存并测试连接」，用保存后的账号分别测试文本与压缩请求。测试开始 MUST 只表示请求进行中；收到真实响应后 MUST 显示 HTTP 状态、上游模型与请求 ID；失败 MUST 保留原始错误码。关闭测试窗口或测试结束 MUST 刷新账号状态。

#### Scenario: 测试失败保留错误码
- **WHEN** 连接测试收到上游 403
- **THEN** 测试窗口 MUST 显示 HTTP 403 与上游原始错误码
- **THEN** 账号状态 MUST 在测试结束后刷新
