## Purpose

规定 BPS 平台在管理端与使用端的可见能力：账号创建/编辑字段、凭证状态展示、分组配置、API Key 使用说明与渠道监测模板，确保管理员能安全地配置与排障。

## ADDED Requirements

### Requirement: 平台徽标与筛选
管理端账号列表、分组列表与模型广场 MUST 显示 BPS 平台徽标，并 MUST 支持按该平台筛选。

#### Scenario: 平台筛选
- **WHEN** 管理员在账号列表筛选 OpenAI BPS
- **THEN** 列表 MUST 只显示 BPS 账号

### Requirement: 账号表单字段
账号创建/编辑弹窗 MUST 提供 Access Token 输入（脱敏显示、留空保留原值）、可选账号 ID 覆盖、代理、分组与模型映射；平台选择 BPS 时 MUST 隐藏不适用字段（如 API Key）。

#### Scenario: 编辑留空
- **WHEN** 管理员编辑 BPS 账号且不填写 Access Token
- **THEN** 系统 MUST 保留原 Access Token

### Requirement: 凭证状态展示
账号列表与编辑窗口 MUST 展示 BPS 凭证状态：正常/已过期/已撤销/认证失败、到期时间、最近发现时间与错误码。自然过期提示 MUST 每 30 秒更新，不依赖列表自动刷新。

#### Scenario: 已过期提示
- **WHEN** 账号 JWT 已过期
- **THEN** 列表 MUST 显示「已过期」，且提示 MUST 在 30 秒内随到期时间更新

### Requirement: 使用说明
API Key 使用说明 MUST 为 BPS 分组提供 Codex 配置示例（`wire_api="responses"`、`supports_websockets=false`）与模型目录说明。

#### Scenario: 使用说明可见
- **WHEN** 用户查看 BPS 分组的 API Key 使用说明
- **THEN** 页面 MUST 展示可直接复制的 Codex 配置

### Requirement: 渠道监测
渠道监测 MUST 支持 BPS 平台模板，使用 Responses 探活。BPS 模板 MUST NOT 提供上游配额探测。

#### Scenario: 监测探活
- **WHEN** 管理员为 BPS 分组创建监测任务
- **THEN** 监测 MUST 通过 Responses 端点探活，并记录状态码与延迟
