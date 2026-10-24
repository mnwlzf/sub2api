## Context

本仓库是 sub2api v0.2.16 基线的活跃分支，平台体系由 `internal/domain` 的平台常量、`internal/service` 的账号/调度/计费、`internal/handler` 的协议入口共同构成；Redis 已是必需依赖（缓存、限流、锁、WS 状态）。

BPS（Basis Points）是 ChatGPT 账号的 Excel 权益：上游 `https://bps.openai.com/basispoints/api/responses` 接受 ChatGPT Access Token 与账号 ID，返回标准 Responses JSON/SSE。它与既有两条 OpenAI 链路的差异：

| 维度 | OpenAI Platform | ChatGPT Codex（现有） | OpenAI BPS（本提案） |
|---|---|---|---|
| 鉴权 | API Key | OAuth/SetupToken | ChatGPT Access Token + 账号 ID 头 |
| 上游 | api.openai.com | chatgpt.com/backend-api/codex | bps.openai.com/basispoints/api/responses |
| 协议 | Responses/Chat/Images | Responses（HTTP+WS） | 仅 Responses HTTP JSON/SSE |
| 工具 | 原生 | 原生 | 需文本目录 + `run_officejs` 隧道 |
| 计费 | 官方价目 | 订阅/点数 | 上游 usage，管理员配置价格 |
| 状态 | 无 | WS 连接/回放 | Redis 会话绑定 + 工具 item + 压缩摘要 |

参考实现 sub4api（LGPL-3.0，已闭源停更）在 v0.2.13 基线上实现了该平台，代码位于 `backend/internal/service/openai_bps_*.go`（16 文件约 3.7k 行）、迁移 `245_openai_bps_platform.sql`、前端 `OpenAIBPSAccountFields.vue` / `BPSCredentialStatus.vue`，并有 `docs/openai-bps.md` / `docs/openai-bps-sources.md`。本设计只移植**协议事实与算法**，按本地架构重写。

## Goals / Non-Goals

**Goals**
- 以独立平台接入 BPS，不影响既有平台任何行为。
- 打通 Responses JSON/SSE、工具隧道、压缩、结构化输出四条主链路，并具备真实账号可验收的完整闭环。
- 计费、错误、调度语义与本地既有平台一致；状态层多实例可用。
- 提供平台级 kill switch 与清晰的封禁风险提示。

**Non-Goals**
- 不做 WS/Chat/Messages/图像/音频；不做 token 自动续期、Excel 本地凭证读取、上游配额探测。
- 不承诺协议稳定；不引入新数据库表存凭证；不做进程内状态降级。

## Decisions

### D1：独立平台 `openai_bps`，而非 OpenAI 平台的账号子类型
理由：上游地址、鉴权头、协议能力、错误码、计费口径都不同；独立平台可复用现有「平台过滤 → 分组 → 调度 → 计费」骨架，同时把差异关在一个平台内，风险可控（可整体禁用）。代价：平台常量、目录、前端徽标、Composite 路由、渠道监测都要各加一处。

### D2：上游地址固定，鉴权头服务端注入
- 端点常量写死；账号级 `base_url` 与客户端请求头一律不参与。
- 注入：`Authorization: Bearer <AT>`、两个账号 ID 头（`chatgpt-account-id` 与 BPS 侧要求的账号头）、`x-basispoints-auth-mode: chatgpt`。
- 客户端传入的认证头 MUST NOT 转发；其余头按白名单透传。
- 理由：BPS 是单一上游，开放 base_url 只会带来 SSRF 与密钥外泄面。

### D3：凭证模型
- 账号 `type=oauth`，`credentials.access_token` 手填；`chatgpt_account_id` 可选覆盖。
- JWT 只解析 payload 提取 `chatgpt_account_id` 与 `exp`，**不验签**（验签需要公钥且无意义）；解析失败按「无到期信息」处理，不阻断创建。
- 到期状态本地计算；上游提前撤销由正常请求或连接测试反馈（不做后台探测）。
- 编辑时留空保留原 token 与诊断；替换 token 不解除管理员主动停用/关闭调度。
- 凭证脱敏展示；`bps_credential_state` 只读暴露状态、到期时间、最近发现时间、错误码。

### D4：协议移植来源冻结与署名
- 工具信封、`custom` 原文标记、结构化输出、工具目录、计划参数：移植自 `ranxi2001/sub2api@055a1cd1` 的 `internal/service/basispoints/`（LGPL-3.0，与本仓库同许可），新文件注明来源。
- 工具隧道、完整调用回放、加密 reasoning、计划成功回执、结果 ID 规范化：对照 `Nonary/ghcp_proxy@ad23ce2d`（Unlicense）。
- `tool/args` 信封、轮次保持、工具结果回放：对照 `1812095643/sub2api-excel2api-plugin@ad203907`。
- 来源链与提交号写入 `docs/openai-bps-sources.md`，随代码提交。
- 理由：BPS 协议非公开契约，必须冻结对照版本，否则无法判断上游漂移还是实现 bug。

### D5：工具隧道（文本目录 + `run_officejs` 载体）
- 工具声明（含 `additional_tools`）整理成稳定文本目录：重复且相同的声明合并，冲突声明报错。
- function 默认 `name/arguments` JSON 信封，兼容 `tool/args`；custom 默认 `summary=codex2api.custom/<完整名>` + `code` 原文，兼容旧 JSON 信封；信封上限 1 MiB。
- `run_officejs` 仅作协议载体：代理不执行 OfficeJS 与客户端工具。
- 只有本次声明的工具可输出给客户端；function 参数按 JSON Schema 校验；外部 `$ref` 禁用。
- 原始 item、`id`、`call_id`、arguments 存 Redis；客户端回传结果时恢复为稳定 `fc_` ID 的 `function_call_output`；加密 reasoning 重建为类型 + 空 summary + 原密文。
- 原生 `update_plan` 对齐步骤别名、状态与成功回执；失败结果保留。
- 一次最多交付一个工具调用；文本实时输出，工具事件在完整解析、校验、保存后才输出。

### D6：Redis 状态层（必需，无降级）
- 命名空间：`bps:session:<apiKeyID>:<sessionKey>`（绑定账号/workspace/轮次）、`bps:item:<session>:<callID>`、`bps:compact:<session>:<ref>`。
- 7 天闲置过期（每次读写续期）；原子绑定（Lua/`SET NX`）避免并发换号。
- 会话按 API Key 隔离；无显式 `session_id`/`prompt_cache_key` 时用首条用户消息派生稳定身份。
- 已绑定上下文的会话不再换号；绑定账号不可用返回 `bps_account_unavailable`，缺失上下文返回 `bps_context_expired`。
- 理由：工具 item 与压缩摘要必须跨实例可恢复；进程内缓存会在多副本下丢状态。

### D7：压缩（真实摘要 + 版本化引用）
- `/responses/compact` 与带 `compaction_trigger` 的 Responses 请求走真实摘要请求（关闭客户端工具），摘要写入 Redis。
- 客户端只收到 `bpscmp_v1_<opaque>` 引用，引用本身不发给 BPS；后续请求由服务端恢复摘要。
- 记录 `turn_id` 与 `agent_iteration`：恢复后无新用户消息保持同一轮次，仅按新增工具结果递增迭代；重试相同请求不改变上游请求体。
- 连续压缩保留原始轮次锚点；旧摘要记录可读。
- 压缩失败返回错误，不替换客户端原历史；异常断流或终态丢失已完成工具 item 时不伪造成功。

### D8：结构化输出（提示词约束 + 本地校验）
- `text.format.type=json_object|json_schema`：把要求写入 developer 消息，BPS 请求不携带原生 `text.format`，客户端响应保留请求的格式声明。
- 最终 JSON 必须满足 schema（即使 `strict=false`）；保留大整数精度；schema ≤ 1 MiB，仅文档内引用；最终文本 ≤ 16 MiB 且受既有响应大小限制。
- 结构化文本等终态校验通过后才发送；工具调用阶段与明确拒答按独立协议项处理。
- 校验失败：未开始响应 → HTTP 502；已开始 SSE → `response.failed`；已取得 usage 仍记录。不自动改写或重试生成。

### D9：计费
- 按上游 `usage` 记录输入/缓存/输出 token；沿用分组/渠道价格、倍率、用量日志、用户平台配额。
- 可取得用量的失败响应同样记录用量。
- 上游成本由管理员在分组配置，不从 Excel 点数或 Codex 配额推断。

### D10：错误分类、冷却与换号边界
| 上游信号 | 处理 |
|---|---|
| 401 / token 撤销 | 停止该账号调度（认证失败），保留管理员停用状态 |
| token 过期 | 调度前排除；`bps_credential_state` 显示自然过期 |
| `basispoints_model_access_changed` / `model_not_allowed` | 归一为 `bps_model_not_allowed`，按映射后模型冷却 30 分钟，账号其它模型可用 |
| 普通 403 | 不判定整账号失效，按请求失败处理 |
| 400 参数错误 | 不重试 |
| 429 | 按 `Retry-After` 冷却 |
| 网络错误 / 5xx | 仅「未绑定上下文且未输出」的新会话可有限换号 |
- 认证失败只更新与本次请求 AT + 账号 ID 匹配的凭证记录，迟到响应不得标记新凭证失效。
- 认证失败写诊断与调度快照通知必须在同一事务。

### D11：模型目录与 Composite
- BPS 分组只使用 BPS 账号；目录声明串行工具、文本输入、HTTP。
- 默认候选模型 `gpt-6-astra`、`gpt-5.6-sol`；支持别名映射与白名单；删除全部规则表示不限模型。
- Composite 分组必须为 BPS 添加显式路由：精确路由直接发布别名，前缀路由筛选可枚举的账号模型；无有效候选返回空列表。普通 `gpt-*` 自动识别仍选 OpenAI。
- 目录的 200k 上下文 / 160k 压缩阈值是保守本地策略，不是上游配额承诺。

### D12：管理端与风险确认
- 创建/编辑账号：Access Token 字段、可选账号 ID、代理、分组、模型映射；创建与保存时弹出「账号限制/封禁风险」二次确认，取消不创建。
- 「保存并测试连接」：保存后打开测试窗口，可选模型测试文本与压缩；开始只表示请求中，实际响应显示 HTTP 状态、上游模型、请求 ID，失败保留原始错误码；关闭弹窗刷新账号状态。
- 分组编辑：模型价格/倍率/白名单/模型映射；BPS 分组徽标与平台筛选。
- API Key 使用说明：Codex 配置 `wire_api="responses"`、`supports_websockets=false` 与模型目录。
- 渠道监测：新增 BPS 模板，用 Responses 探活（不提供上游配额探测）。

### D13：可观测与 kill switch
- 结构化日志字段：`platform=openai_bps`、`account_id`、`session_key`、`turn_id`、`agent_iteration`、`upstream_status`、`error_code`、`state_hit`（Redis 命中/过期）。
- 平台级开关：`gateway.openai_bps.enabled`（默认 false）——关闭后该平台账号不参与调度、入口返回明确错误，迁移与数据保留。
- 指标：请求量/失败率/工具调用轮次/压缩次数/Redis 命中率。

## Risks / Trade-offs

1. **协议非公开**：上游随时可能变化；缓解：冻结来源、平台级 kill switch、真实账号冒烟用例。
2. **封号风险**：BPS 权益账号用于中转可能违反上游条款；缓解：UI 强提示 + 文档免责 + 平台默认关闭。
3. **工具隧道复杂度**：信封/回放/schema 校验链路长，出错会静默丢工具调用；缓解：单工具交付、终态校验后才输出、完整测试矩阵。
4. **Redis 强依赖**：多实例下不可降级；缓解：启动时探测 Redis，不可用则平台不可用并明确报错。
5. **计费准确性**：失败响应用量、压缩/结构化路径的 usage 需要逐路径验证；缓解：按路径写测试并在验收清单逐项打勾。
6. **工作量**：后端约 3.7k 行 + 测试；缓解：分阶段交付（先文本闭环，再工具/压缩/结构化）。

## Migration Plan

1. 迁移 `2xx_openai_bps_platform.sql`：扩展平台枚举/约束（纯加性，可回滚为空操作）。
2. 平台常量与判定、账号 CRUD、调度过滤、目录、前端徽标（默认关闭，不影响既有流量）。
3. 网关文本闭环 → 工具隧道 → 压缩 → 结构化输出 → 计费/错误细化。
4. 打开 `gateway.openai_bps.enabled` 灰度：单账号单分组 → 小流量 → 全量。
5. 回滚：关闭平台开关即可停止全部 BPS 流量；数据与账号保留，不影响其它平台。

## Open Questions

- 真实 BPS token 的权限范围与速率限制未知，需测试账号验收。
- 上游是否接受 `store=false`、是否返回 `previous_response_id` 语义，需实测。
- 点数计价与人民币结算的换算口径由运营决定，不在代码内硬编码。
- 是否需要按账号维度限制并发（上游连接数未知）。
