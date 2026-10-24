## Purpose

规定 BPS 工具隧道：把 Responses 工具声明编码成文本目录，经 `run_officejs` 协议载体往返，并用 Redis 保存原始调用以恢复客户端工具语义。目标是在上游只接受文本输出的前提下，保持 Codex 工具链完整可用。

## ADDED Requirements

### Requirement: 工具目录生成
系统 MUST 把本次请求的工具声明（含 `additional_tools`）整理成稳定的文本目录。重复且完全相同的声明 MUST 合并为一项；同名但定义冲突的声明 MUST 报错并拒绝请求。目录顺序 MUST 稳定，不得因 map 遍历顺序变化。

#### Scenario: 冲突声明
- **WHEN** 请求声明两个同名但参数 schema 不同的 function
- **THEN** 系统 MUST 返回 400 并说明冲突，MUST NOT 静默选择其一

### Requirement: 信封编解码
function 调用 MUST 使用 `name`/`arguments` JSON 信封，并 MUST 兼容 `tool`/`args` 旧信封。custom 工具 MUST 使用 `summary=codex2api.custom/<完整工具名>` 与 `code` 原文，并 MUST 兼容旧 JSON 信封。信封 MUST NOT 超过 1 MiB。

#### Scenario: 兼容旧信封
- **WHEN** 模型输出 `tool`/`args` 形状的调用
- **THEN** 系统 MUST 正确解析并转换为标准 `function_call`

#### Scenario: 信封超限
- **WHEN** 工具调用信封超过 1 MiB
- **THEN** 系统 MUST 拒绝该调用并返回明确错误

### Requirement: 只输出已声明工具
系统 MUST 只允许本次请求声明过的工具出现在客户端响应中。未知工具名 MUST 被拒绝，不得透传给客户端执行。

#### Scenario: 未知工具
- **WHEN** 模型输出未声明的工具名
- **THEN** 系统 MUST 拒绝该调用并记录诊断，MUST NOT 向客户端交付

### Requirement: 参数校验
function 参数 MUST 按声明的 JSON Schema 校验；外部 `$ref`（网络或跨文档引用）MUST 被拒绝。校验失败 MUST 拒绝该调用。

#### Scenario: 参数不符合 schema
- **WHEN** 模型输出缺少必填字段的 arguments
- **THEN** 系统 MUST 拒绝该调用，MUST NOT 交付给客户端

### Requirement: Redis 原始调用存储与恢复
系统 MUST 把完整原始 item、`id`、`call_id`、arguments 存入 Redis，并在客户端回传工具结果时恢复为带稳定 `fc_` 前缀 ID 的 `function_call_output`。加密 reasoning MUST 重建为「类型 + 空 summary + 原密文」。存储 MUST 7 天闲置过期，每次读写 MUST 续期，并 MUST 跨实例可读写。

#### Scenario: 跨实例恢复
- **WHEN** 客户端在实例 A 收到工具调用，在实例 B 回传结果
- **THEN** 实例 B MUST 从 Redis 恢复原始调用并正确续接上游请求

#### Scenario: 上下文过期
- **WHEN** 客户端回传结果的会话已超过 7 天闲置
- **THEN** 系统 MUST 返回 `bps_context_expired`，要求新建会话

### Requirement: 单工具交付与终态校验
一次回合 MUST 最多向客户端交付一个工具调用。文本 MUST 实时输出；工具事件 MUST 在完整解析、schema 校验、Redis 保存完成后才输出。异常断流或终态丢失已完成工具 item 时 MUST NOT 伪造成功。

#### Scenario: 两个工具调用
- **WHEN** 模型在同一回合输出两个工具调用
- **THEN** 系统 MUST 只交付第一个，并在下一回合按协议续接其余调用

### Requirement: update_plan 适配
原生 `update_plan` MUST 对齐步骤别名、状态与成功回执；失败结果 MUST 保留并如实回传。

#### Scenario: 计划回执
- **WHEN** 模型调用 `update_plan` 且参数合法
- **THEN** 客户端 MUST 收到与官方 Codex 一致的步骤别名与成功回执
