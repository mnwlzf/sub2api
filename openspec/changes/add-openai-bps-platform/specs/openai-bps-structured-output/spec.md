## Purpose

规定 BPS 的结构化输出能力：由于上游不支持原生约束解码，系统以 developer 提示词约束 + 本地终态校验实现 `json_object` / `json_schema`，并定义失败语义与用量保留。

## ADDED Requirements

### Requirement: 结构化输出请求解析
系统 MUST 识别 `text.format.type=json_object` 与带 `name/schema/strict` 的 `json_schema`。要求 MUST 写入 developer 消息；发往 BPS 的请求 MUST NOT 携带原生 `text.format`；返回给客户端的响应 MUST 保留请求声明的格式。

#### Scenario: json_schema 请求
- **WHEN** 客户端请求 `text.format.type=json_schema`
- **THEN** 上游请求体 MUST 不含原生 `text.format`
- **THEN** 客户端响应 MUST 保留该格式声明

### Requirement: 本地终态校验
最终文本 MUST 被校验：`json_object` MUST 是一个 JSON 对象；`json_schema` MUST 满足 schema，即使 `strict=false` 也必须校验。校验 MUST 保留大整数精度（不得经由 float64 丢精度）。schema MUST 不超过 1 MiB，且 MUST 只允许文档内引用。

#### Scenario: 大整数
- **WHEN** 模型输出包含超过 2^53 的整数
- **THEN** 校验与返回 MUST 保持原始精度

#### Scenario: 外部引用
- **WHEN** schema 含网络或跨文档 `$ref`
- **THEN** 系统 MUST 在请求上游前返回 400

### Requirement: 输出时机与隔离
结构化文本 MUST 在终态校验通过后才发送。JSON 与 SSE MUST 使用同一套校验规则；普通文本 MUST 仍实时输出。工具调用阶段与明确拒答 MUST 按独立协议项处理；压缩 MUST 始终生成真实文本摘要。

#### Scenario: 校验失败未开始响应
- **WHEN** 最终文本不满足 schema 且尚未向客户端写出响应
- **THEN** 系统 MUST 返回 HTTP 502 与错误码 `bps_invalid_structured_output`

#### Scenario: 校验失败已开始 SSE
- **WHEN** 校验失败发生在 SSE 已经开始之后
- **THEN** 系统 MUST 发送 `response.failed` 事件
- **THEN** 系统 MUST NOT 自动改写答案或重试生成

### Requirement: 用量保留
结构化输出校验失败时，已取得的上游 usage MUST 仍被记录与计费。

#### Scenario: 失败仍计费
- **WHEN** 校验失败但上游返回了 usage
- **THEN** 用量日志 MUST 记录该次请求的 usage
