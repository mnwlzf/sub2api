## Why

部分 ChatGPT 账号带有 Excel/BPS（Basis Points）权益：上游 `https://bps.openai.com/basispoints/api/responses` 用固定的 ChatGPT Access Token + 账号 ID 提供 Responses 协议，价格按点数结算，比订阅配额更适合做低成本 Coding 供给。本地 sub2api 目前只支持标准 OpenAI Platform / ChatGPT Codex 两条链路，无法接入这类账号。

参考实现 sub4api 已把 BPS 做成独立平台（`openai_bps`），但它是 sub2api v0.2.13 基线上的私有分支、已闭源停更，且与本地的平台常量、调度器、计费、模型目录、Redis 状态层耦合方式不同。直接移植会带来三方面风险：协议层（非公开契约）需要冻结来源、状态层（工具/压缩/会话绑定）需要按本地 Redis 能力重写、计费与调度语义需要重新对齐本地 `platform` 体系。

本提案把 BPS 平台作为**独立平台**立项：不改动既有 OpenAI 平台行为，新增平台值、账号类型、网关、工具桥、压缩、结构化输出、计费与目录能力，并用真实账号完成端到端验收。

## What Changes

- 新增平台 `openai_bps`（显示名 **OpenAI BPS**），账号类型 `oauth`，凭证为手动填写的 ChatGPT Access Token（可选覆盖 `chatgpt_account_id`；留空时从 JWT 提取，同时提取到期时间，仅解析不验签）。
- 新增固定上游端点 `https://bps.openai.com/basispoints/api/responses`，服务端注入 Bearer token、两个账号 ID 头与 `x-basispoints-auth-mode: chatgpt`；**不接受**客户端或账号配置替换上游地址，不转发客户端认证头。
- 新增 Responses 网关：仅 HTTP JSON/SSE，支持 function/custom/namespace 工具声明、`update_plan`、多轮工具结果、加密 reasoning；**不提供** WebSocket、Chat Completions、Claude Messages、图像/音频。
- 新增工具隧道：工具声明整理成稳定文本目录，经原生 `run_officejs` 协议载体承载；原始工具 item、`id`/`call_id`、arguments 存 Redis（7 天闲置过期），客户端回传结果时恢复为带稳定 `fc_` ID 的 `function_call_output`；一次最多交付一个工具调用。
- 新增压缩（`/responses/compact` 与 `compaction_trigger`）：向 BPS 发真实摘要请求（关闭客户端工具），摘要存 Redis，客户端只收到版本化不透明引用 `bpscmp_v1_`；保留 `turn_id` 与 `agent_iteration`，连续压缩与跨实例恢复可用。
- 新增结构化输出：`text.format` 的 `json_object` / `json_schema` 以 developer 提示词约束 + 本地终态校验实现（非上游原生约束解码）；schema ≤ 1 MiB、仅允许文档内引用；校验失败返回 `bps_invalid_structured_output`。
- 新增计费与错误语义：按上游 `usage` 计费（沿用分组/渠道价格、倍率、用量日志、用户平台配额）；认证失败停止账号调度、过期 token 调度前排除；`basispoints_model_access_changed` / `model_not_allowed` 归为 `bps_model_not_allowed` 并按映射后模型冷却 30 分钟；400 不重试、429 按 `Retry-After`、仅未绑定上下文且未输出的新会话可换号。
- 新增模型目录：BPS 分组发布串行工具 + 文本 + HTTP 的模型目录；默认候选 `gpt-6-astra`、`gpt-5.6-sol`；Composite 分组必须显式路由 BPS，普通 `gpt-*` 自动识别仍走 OpenAI。
- 新增管理端：账号创建/编辑（Access Token + 封禁风险二次确认 + 保存并测试）、凭证状态只读视图 `bps_credential_state`、分组模型映射/白名单、API Key 使用说明、渠道监测 BPS 模板。
- 新增迁移：平台枚举约束扩展（无新凭证表）；Redis 成为该平台必需依赖（不做进程内降级）。

## Capabilities

### New Capabilities

- `openai-bps-account`：BPS 平台与账号生命周期（平台常量、凭证解析与脱敏、JWT 到期、状态保留、风险确认、连接测试）。
- `openai-bps-responses-gateway`：固定端点与请求头注入、JSON/SSE 转发、会话身份（session_id / prompt_cache_key）、上下文绑定与换号边界。
- `openai-bps-tool-bridge`：工具目录生成、信封编解码、Redis 原始 item 存储与恢复、schema 校验、`update_plan` 回执、单工具交付。
- `openai-bps-compaction`：真实摘要压缩、版本化引用、轮次与迭代保持、连续压缩与跨实例恢复。
- `openai-bps-structured-output`：`json_object`/`json_schema` 提示词约束、本地校验、失败语义与用量保留。
- `openai-bps-billing-and-errors`：usage 计费、失败用量、认证/模型权限/限流错误的分类、冷却与重试边界。
- `openai-bps-model-catalog`：BPS 模型目录、别名映射、Composite 显式路由、候选模型与上下文保守值。
- `openai-bps-admin-ui`：管理端账号字段、凭证状态、分组配置、使用说明与渠道监测。

### Modified Capabilities

- 无。BPS 是新增平台，既有 OpenAI/Claude/Gemini 平台的行为与接口保持不变。

## Non-Goals

- 不实现 WebSocket 上游、Chat Completions、Claude Messages、图像/音频接口。
- 不做 Access Token 自动续期、Excel 本地凭证读取、上游配额探测。
- 不用 `previous_response_id` / `item_reference` 替代完整历史。
- 不承诺 BPS 协议长期稳定；协议以冻结的参考实现为准，上游变化时按平台开关整体下线。
- 不改动既有平台的上游地址、鉴权与计费路径。

## Impact

- **数据库**：新增迁移 `2xx_openai_bps_platform.sql`（平台枚举/约束扩展，纯加性）；无新凭证表，账号凭证仍走 `credentials` JSONB。
- **后端**：`domain` 平台常量、`service` 平台判定与调度、`repository` 平台过滤与计费、`handler` 新增 BPS 网关入口与路由、`config` 默认值、`wire` 装配。
- **Redis**：新增 BPS 状态命名空间（会话绑定、工具 item、压缩摘要），7 天闲置过期；Redis 不可用时 BPS 平台整体不可用（明确报错，不降级）。
- **前端**：平台徽标/筛选、账号创建编辑字段与风险确认、凭证状态组件、分组模型映射、API Key 使用说明、渠道监测模板。
- **运维**：需要真实 BPS 账号完成验收；账号存在封禁风险，需在 UI 与文档中明确提示；上游协议非公开契约，需保留平台级 kill switch。
- **预估工作量**：后端约 3.7k 行（参考实现 16 文件）+ 测试约 3k 行 + 前端约 800 行 + 迁移/文档；按 2 人并行约 6–8 个工作日（不含真实账号验收等待）。
