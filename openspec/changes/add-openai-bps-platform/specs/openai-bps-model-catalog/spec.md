## Purpose

规定 BPS 的模型目录与 Composite 路由：BPS 分组发布哪些模型、别名与白名单如何生效、Composite 如何显式选择 BPS，以及上下文/压缩阈值的保守口径。

## ADDED Requirements

### Requirement: BPS 模型目录
BPS 分组的模型目录 MUST 只发布由合格 BPS 账号承接的模型，并 MUST 声明串行工具、文本输入与 HTTP 能力。默认候选模型 MUST 为 `gpt-6-astra`、`gpt-5.6-sol`。目录中的上下文窗口与压缩阈值 MUST 为保守本地策略，MUST NOT 声称是上游配额或能力保证。

#### Scenario: 目录能力声明
- **WHEN** 客户端拉取 BPS 分组模型目录
- **THEN** 每个模型 MUST 声明串行工具、文本输入、HTTP，且 MUST NOT 声明 WebSocket

### Requirement: 别名映射与白名单
管理员 MUST 能为 BPS 分组配置模型别名映射与白名单。删除全部映射规则 MUST 表示不限模型。别名 MUST 在计费与调度中映射到上游模型名，并 MUST 在用量日志中同时保留请求模型与上游模型。

#### Scenario: 别名映射
- **WHEN** 分组把 `my-codex` 映射到 `gpt-6-astra`
- **THEN** 客户端请求 `my-codex` MUST 调度到 `gpt-6-astra`，用量日志 MUST 记录两者

#### Scenario: 白名单外模型
- **WHEN** 客户端请求不在白名单内的模型
- **THEN** 系统 MUST 返回明确错误，且 MUST NOT 向上游发起请求

### Requirement: Composite 显式路由
Composite 分组 MUST 为 BPS 添加显式路由：精确路由直接发布别名；前缀路由 MUST 只筛选可枚举的账号模型；无有效候选时 MUST 返回空列表而不是回退全部平台模型。普通 `gpt-*` 自动识别规则 MUST 仍选择 OpenAI。

#### Scenario: 无有效候选
- **WHEN** Composite 分组配置了 BPS 前缀路由但没有任何 BPS 账号承接
- **THEN** 目录 MUST 返回空列表，MUST NOT 回退到 OpenAI 账号

### Requirement: 目录缓存与失效
目录 MUST 在账号/分组配置变化后失效或刷新，避免继续发布已删除账号或已下线模型的别名。

#### Scenario: 账号被移除
- **WHEN** 分组内最后一个承接某别名的 BPS 账号被删除
- **THEN** 该别名 MUST 从目录中消失
