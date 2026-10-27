## Purpose

规定 OpenAI 网关计费档位（`service_tier`）在两种边界下的取值方式：分组强制 fast 时不得把更高的 ultrafast 静默降级；上游档位声明不可信时不得据此降低计费。

## ADDED Requirements

### Requirement: 强制 fast 是「至少 fast」

开启了 `force_openai_fast` 的分组，系统 MUST 把出站请求的 `service_tier` 提升到 `priority`，但 MUST NOT 覆写客户端已经显式请求的更高档位（当前仅 `ultrafast`）。

该规则 MUST 在所有出站收口点一致生效：`/v1/responses`、`/v1/chat/completions`、`/v1/messages`、OpenAI 自动透传、Realtime WS 的 `response.create` 帧。

未开启该分组开关、账号平台不是 `openai`、或请求上下文缺少有效分组时，系统 MUST NOT 修改 `service_tier`。

#### Scenario: ultrafast 请求不被降级
- **WHEN** 分组开启 `force_openai_fast`，客户端请求体 `service_tier` 为 `ultrafast`
- **THEN** 发往上游的 `service_tier` MUST 仍为 `ultrafast`
- **THEN** 计费档位 MUST 为 `ultrafast`

#### Scenario: 缺失或更便宜的档位被提升
- **WHEN** 分组开启 `force_openai_fast`，客户端未发送 `service_tier` 或发送 `default` / `auto` / `scale` / `flex` / `fast` / `priority`
- **THEN** 发往上游的 `service_tier` MUST 为 `priority`

### Requirement: 上游档位声明可选择不参与计费降档

系统 MUST 支持账号级凭据 `response_tier_non_authoritative` 声明该账号上游响应的档位不可信。开启后，上游声明的基础档位（`default` / `standard`）MUST NOT 降低计费档位，计费 MUST 采用请求实际发送的档位。

开启后，上游声明的更便宜档位 `flex` MUST 仍然生效（继续降档），以免掩盖真实降级。

开关未开启（缺省）时，计费 MUST 沿用既有契约：上游声明更便宜的已知档位则降档，声明更贵或未知档位则忽略。

无请求档位的场景，开关 MUST NOT 因上游声明而把计费档位提升为非空值。

OpenAI OAuth / SetupToken 账号的既有豁免（Codex 私有端点常以 `default` 回报 Fast 轮次）MUST 保持不变，且同样扩展到 `standard` 写法。

#### Scenario: 中转回报 default 时不降档
- **WHEN** 账号 `type=apikey`、`platform=openai`、`credentials.response_tier_non_authoritative=true`
- **AND** 请求发送 `service_tier=priority`，上游响应声明 `service_tier=default`
- **THEN** 计费档位 MUST 为 `priority`
- **THEN** 用量日志 MUST 记录 `priority`
- **THEN** MUST NOT 产生 `billing.service_tier_downgraded` 审计记录

#### Scenario: 真实 flex 降级仍然生效
- **WHEN** 同一账号开启该开关，请求发送 `service_tier=priority`，上游响应声明 `service_tier=flex`
- **THEN** 计费档位 MUST 为 `flex`

#### Scenario: 开关关闭时行为不变
- **WHEN** 账号未设置该凭据，请求发送 `priority`，上游声明 `default`
- **THEN** 计费档位 MUST 为 `default`，与改动前一致
