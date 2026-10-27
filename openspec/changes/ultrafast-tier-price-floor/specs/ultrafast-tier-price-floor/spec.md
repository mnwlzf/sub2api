## Purpose

规定 OpenAI 网关 `ultrafast` 服务档位的计费倍率取值，保证该档位永远不低于同渠道的 fast 档，避免因缺少独立定价而按兜底倍率低价卖出。

## ADDED Requirements

### Requirement: ultrafast 价格不得低于 fast

计算 `ultrafast` 档位的计费倍率时，系统 MUST 取「模型专属 ultrafast 倍率」与「渠道 fast 倍率」的较大值。

两者均未配置（均为 0 / 未设置）时，系统 MUST 回退到通用兜底倍率 2.0，与改动前一致。

`priority` / `fast` / `flex` 三个档位的取值逻辑 MUST 保持不变。

系统 MUST NOT 因该规则新增数据库列、管理界面字段或修改任何已有渠道定价数值。

#### Scenario: 渠道只配了 fast
- **WHEN** 渠道 `fast_multiplier = 8`，模型没有专属 ultrafast 倍率
- **THEN** `ultrafast` 计费倍率 MUST 为 8

#### Scenario: 模型专属倍率高于 fast
- **WHEN** 模型专属 ultrafast 倍率 = 6，渠道 `fast_multiplier = 3`
- **THEN** `ultrafast` 计费倍率 MUST 为 6

#### Scenario: fast 高于模型专属倍率
- **WHEN** 模型专属 ultrafast 倍率 = 6，渠道 `fast_multiplier = 8`
- **THEN** `ultrafast` 计费倍率 MUST 为 8，MUST NOT 为 6

#### Scenario: 渠道未配任何倍率
- **WHEN** 渠道既没有 ultrafast 专属倍率也没有 fast 倍率
- **THEN** `ultrafast` 计费倍率 MUST 回退为 2.0
