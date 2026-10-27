## Purpose

规定分组级「强制服务档位」的存储、取值与管理端交互，使后台用量记录的服务档位与上游实际服务一致。

## ADDED Requirements

### Requirement: 分组可声明强制档位

分组 MUST 支持 `forced_service_tier` 配置，取值语义：

- 空字符串：不强制，沿用 `force_openai_fast` 的 `priority` 行为
- `priority`：强制 Fast
- `ultrafast`：强制 UltraFast

写入前系统 MUST 归一化：`fast` 视为 `priority`，其余未知值 MUST 回落为空字符串。

平台不支持 OpenAI Fast（非 openai / composite）时，系统 MUST 清空该字段。

复制分组时 MUST 复制该字段。

#### Scenario: 强制 UltraFast 覆盖客户端档位
- **WHEN** 分组 `forced_service_tier = "ultrafast"`，客户端请求体分别不带 `service_tier`、`default`、`flex`、`fast`、`priority`、`ultrafast`
- **THEN** 发往上游的 `service_tier` MUST 全部为 `ultrafast`
- **THEN** 用量记录的 `service_tier` MUST 为 `ultrafast`

#### Scenario: 强制 Fast 不降级更高档
- **WHEN** 分组 `forced_service_tier = "priority"`，客户端请求 `ultrafast`
- **THEN** 发往上游的 `service_tier` MUST 保持 `ultrafast`

#### Scenario: 未配置时行为不变
- **WHEN** 分组 `forced_service_tier` 为空且 `force_openai_fast = true`
- **THEN** 行为 MUST 与改动前一致（提升到 `priority`，不降级客户端 `ultrafast`）

### Requirement: 管理端可配置该档位

管理端创建/更新分组接口 MUST 接受并回显 `forced_service_tier`；分组编辑表单 MUST 提供「不指定 / Fast / UltraFast」三选项。

#### Scenario: 保存后新请求立即生效
- **WHEN** 管理员把某分组的档位由「不指定」改为「UltraFast」并保存
- **THEN** 其后新发起的请求 MUST 以 `ultrafast` 出站并计价
- **THEN** 已建立的 WebSocket 会话 MUST 在重连后生效
