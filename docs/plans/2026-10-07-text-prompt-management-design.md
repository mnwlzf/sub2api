# sub2api 文本提示词管理：第一阶段详细设计

日期：2026-10-07
状态：**已按本文实施**（M0–M3 完成并验证；M4 前端进行中；M5 未开始）。实施证据见
[M0 路径核实](2026-10-07-m0-path-report.md)、[迁移约束验证](2026-10-07-migration-242-verification.md)、
[端到端验证](2026-10-07-e2e-verification.md)。
目标：中央模板、不可变版本、分组绑定、受控账号覆盖、请求级一致注入、可观测与回滚。图片不纳入。

## 1. 产品目标与验收口径

管理员维护文本提示词候选，发布固定版本后绑定分组；网关对满足模型和协议条件的请求应用该版本。后台可解释某次请求用了什么、为什么没用、重试时是否保持一致。模板是数据，不是可执行脚本，不授予网关额外文件、工具或网络执行权限。

“破甲”可作为运营中的候选名称，但系统不设置一个声称普遍有效的“破甲成功”布尔值。分别记录发布、实际注入、行为评估三个维度。HTTP 200、正文包含候选和拒答变化是不同证据。已有 Sol 样本只支持特定条件下出现响应变化，不代表所有渠道或所有后续任务有效。

第一阶段不做图片、自动生成模板、拒答后自动换词、自动联网更新候选、线上流量双发、跨会话记忆管理和自动优化。工具调用类文本请求可保留原工具结构，但模板不会增加工具或改写工具结果。

## 2. 仓库依据与尚需核实的边界

已读依据：

- [Group schema](../../backend/ent/schema/group.go)：分组已有模型路由、fallback、平台和 MCP 注入字段；新增策略不能另造一套账号授权逻辑。
- [AccountGroup schema](../../backend/ent/schema/account_group.go)：账号与分组是多对多关系，主键为 account_id/group_id。
- [Chat 公共管线](../../backend/internal/service/openai_gateway_cc_pipeline.go)：原生 Chat、Responses→Chat、Messages→Chat 共享部分管线，但并非所有行为已经统一。
- [OpenAI 请求构建](../../backend/internal/service/openai_gateway_request_body.go)：已有平台感知、模型兼容和出站 URL 处理。
- [迁移规则](../../backend/migrations/README.md)：迁移有校验和，采用新增、向前迁移，不能改历史迁移。
- [前端脚本](../../frontend/package.json)：Vue 3 / TypeScript，已有 typecheck、Vitest、i18n 和构建检查。

搜索还确认已有 Claude OAuth system 注入配置和处理路径。本功能不替换它。实施前必须逐条核实出站请求的最终修改点、签名点、缓存键和 fallback 位置；本设计没有声称所有平台路径已经审计完成。

## 3. 方案选择

| 方案 | 优点 | 缺点 | 决策 |
|---|---|---|---|
| 分组直接存正文 | 实现少 | 重复内容、历史与回滚困难 | 不采用 |
| 中央模板＋不可变版本＋绑定 | 可复用、可追踪、行为稳定 | 需要独立数据模型和管理页面 | 采用 |
| 规则引擎自动选词与自适应重试 | 灵活 | 变量多、额外费用、难复现 | 延后 |

第一阶段每个作用域仅绑定一个版本。适用模型可以配置多个精确名称，但不引入优先级规则链或模板堆叠。

## 4. 管理流程

1. 创建模板，填写名称、描述、来源说明。
2. 编辑草稿，填写正文、适用客户端模型、上游模型、支持的出站 profile。
3. 预览 UTF-8 字节数、正文哈希、协议投影和与前一版本的差异。
4. 发布固定版本。正文、模型范围、profile 集合一起冻结。
5. 分组启用并选择具体版本。显示预计适用账号与排除原因。
6. 必要时在该分组下为单个账号设置继承、禁用或覆盖。
7. 用无上游调用的预览验证结构，再由管理员主动运行有预算的 A/B。
8. 观察日志，关闭绑定或切回旧版本。

发布与启用是两个操作。发布 v3 不会让仍绑定 v2 的分组自动升级。

## 5. 数据模型

以下名称为拟新增结构，最终命名遵循仓库约定。使用 PostgreSQL 关系表、外键和 Ent schema，不把整套业务埋进账号凭据 JSON。

### 5.1 prompt_templates

- id：主键。
- name：显示名称，最大 100 字符，活动模板内唯一。
- description、source_url、source_note：说明；URL 仅记录，不自动下载。
- archived_at：归档时间；归档后不能新增绑定，现有绑定继续运行。
- revision：乐观锁版本。
- created_by、updated_by、created_at、updated_at。

正文不存模板主表。模板不可在存在版本或引用时物理删除。

### 5.2 prompt_template_drafts

- template_id：唯一外键，每模板一个当前草稿。
- body：非空 UTF-8 文本，第一阶段上限 32 KiB。
- client_models、upstream_models：精确模型名集合；客户端名称先匹配，上游映射后再次匹配。
- supported_profiles：版本允许的协议适配 profile ID 集合。
- revision、updated_by、updated_at。

空模型集合不表示全选，发布必须显式填写。第一阶段不开放任意正则或动态变量。草稿允许修改，以 revision 避免管理员互相覆盖。

### 5.3 prompt_template_versions

- id、template_id、version_no：唯一约束 (template_id, version_no)。
- body、body_sha256、body_bytes：发布后的原始 UTF-8 正文及摘要。
- client_models、upstream_models、supported_profiles：发布快照。
- manifest_sha256：对确定性序列化的正文摘要、范围和 profile 清单计算摘要。
- change_note、published_by、published_at。

所有字段发布后不可变；服务层拒绝编辑，数据库 trigger 防止 UPDATE/DELETE 已发布版本。发布在事务中锁定模板行、校验草稿 revision、分配版本号、插入快照与审计事件。重放发布请求使用幂等键，避免双击产生两个版本。

归档是模板生命周期，不把版本正文改成新状态。普通回滚仍可选择历史发布版本。

### 5.4 group_prompt_bindings

- group_id：唯一外键。
- mode：disabled / version；缺行等价 disabled。
- version_id：mode=version 时必填，否则为空。
- revision、updated_by、updated_at。

禁用分组是该分组所有账号的总开关，账号覆盖不能绕过它。分组复制默认把新组绑定设为 disabled，并在结果中说明；避免复制分组后意外启用候选。

### 5.5 account_group_prompt_overrides

- (account_id, group_id)：唯一键，复合外键关联 account_groups。
- mode：inherit / disabled / version；缺行等价 inherit。
- version_id：仅 version 模式非空。
- revision、updated_by、updated_at。

采用关联作用域而非账号全局字段：同一账号在 A 组的覆盖不影响 B 组。移除分组成员时删除覆盖行，但保留审计历史。

### 5.6 评估与审计

prompt_evaluations：固定模板版本、版本摘要、模型、协议 profile、渠道配置指纹、用例集摘要、参数、评估人、时间、样本数、结论和局限。

prompt_evaluation_runs：每个实验请求的 A/B 分支、HTTP/完成状态、延迟、实际 usage、脱敏响应、错误、顺序及重复编号。摘要不能替代样本记录。

prompt_admin_events：创建、发布、绑定、禁用、覆盖、归档和回滚；记录操作者、作用域、前后 ID/摘要、时间、请求 ID。正文不写入普通操作日志。

prompt_request_events：一次逻辑请求的一条策略记录，加每次出站尝试的应用记录。字段见第 10 节；应覆盖失败请求，不能只依赖成功计费日志。

版本外键采用 RESTRICT；不物理删除历史版本。数据库 CHECK 保证 mode 与 version_id 配套。

## 6. 策略解析与重试一致性

### 6.1 优先级

部署总开关关闭 → 禁用。

分组 disabled 或无绑定 → 禁用，忽略账号覆盖。

分组 enabled → 账号覆盖 disabled 优先；覆盖 version 选择覆盖版本；inherit 使用分组版本。

版本不匹配客户端模型 → skipped_model_scope；不进行无意义注入。版本匹配客户端模型，但所选上游映射/profile 不兼容 → 排除该账号，不静默改成无模板发送。

模板归档不改变运行中的绑定。全部配置变化在新逻辑请求解析时生效。

### 6.2 冻结点

“逻辑请求”指一次下游 HTTP 请求；所有上游尝试共享同一策略上下文。

1. 保留不可变的原始请求体。
2. 读取路由后的初始有效分组，并获取该分组绑定、覆盖集合的一致快照。绑定更新事务增加 group policy generation；读取端使用一致事务快照。
3. 调度器按现有授权、容量和模型规则选择首个可用账号。
4. 在首次网络发送前，依据该账号覆盖解析并冻结策略：enabled、version_id、manifest_hash、注入语义及 profile 兼容范围。首次选择 disabled 也必须冻结。
5. 后续账号重试或 fallback 分组必须具有相同有效策略身份；不同版本即使正文相同也不视为同一版本。否则跳过该候选。
6. 无兼容候选时返回结构化错误，不撤掉模板重发。

跨分组 fallback 仍受原有权限控制；提示词策略只增加兼容约束，不扩大授权。若业务需要不同策略，必须是新下游请求。

### 6.3 同一对话的下一轮

客户端每发起一轮都是新逻辑请求，读取当时绑定；第一阶段只保证请求内一致，不保证整个会话永久固定版本。管理员切换版本会影响后续轮次，UI 明示这一点。不得声称已经提供会话级版本锁定。

Responses previous_response_id / conversation 等服务端历史引用暂不列入可注入 profile：无法在未验证的情况下保证历史候选不累积。启用策略的此类请求明确返回 unsupported_history_mode；策略关闭时保持原路径。后续通过专门契约测试后再开放。

## 7. 协议适配与注入

### 7.1 首批支持矩阵

| 入口 | 实际出站 | 第一阶段 |
|---|---|---|
| Chat HTTP | Chat HTTP | 支持经验证的 API-key profile |
| Chat HTTP | Responses HTTP | 验证转换完整后支持 |
| Responses HTTP，自包含 input | Responses HTTP | 支持经验证的 API-key profile |
| Responses HTTP，自包含 input | Chat HTTP | 验证回退后支持 |
| Claude 原生、Gemini 原生、Codex/OAuth 特殊路径 | 原生/混合 | 不默认宣称支持；保留现有行为，启用不兼容策略时明确报错 |
| WebSocket、Realtime、历史引用请求 | 任意 | 不支持注入 |
| 图片、视频、音频端点 | 任意 | 完全排除 |

支持依据为 profile 契约测试，不靠模型名猜测。首期覆盖此前文本测试所用 Chat 链路，并将自包含 Responses 路径一并纳入验收。

### 7.2 Chat profile

- 固定 system 角色，不开放随意切换 developer 或 user。
- 新增独立文本 system 消息，置于已有连续前导 system/developer 消息之后、普通对话之前。
- 不合并、不替换客户端已有消息，不调整其相对顺序。
- 原有多模态 content 数组、tool_calls、tool_call_id、工具结果保持结构不变。
- 不支持 system 或不支持多个系统消息的上游不使用该 profile；不能偷偷降级为 user。
- 该位置定义序列化行为，不承诺模型如何裁决冲突指令。

### 7.3 Responses profile

- 注入最终出站 instructions；有原字符串时使用 original + "\n\n" + body，无值/空字符串时使用 body。
- 非字符串 instructions 视为不支持的输入类型，返回参数错误，不强制 stringify。
- 不改 input、tools、tool_choice、reasoning、stream、输出格式或输出 token 上限。
- 已有兼容流程生成的基础 instructions 也保留，不能被模板替换。

### 7.4 顺序与幂等

每次尝试从原始请求构建：既有输入检查 → 模型映射/协议转换/平台兼容调整 → 模板适配 → 最终 body 约束检查 → 基于最终 body 的缓存键与签名 → 发送。

原输入审计继续执行；现有针对出站正文的必要检查不得被绕过。模板正文不得作为网关指令执行。

必须定位“最后一次可改变提示词字段的操作”并放在注入之前。签名不能在注入前算完。请求级缓存及任何内容摘要必须反映最终注入正文；已有纯上游 prompt caching 机制不擅自增加私有协议字段。

幂等依赖内部 AttemptContext 和原始 body，不通过搜索用户正文、删掉相同字符串或向上游添加私有标记实现。合法用户重复输入不会被误删。

纯文本功能不把多模态输入变成纯文本；如果混合请求包含原生 image-generation 工具或任务，应在 capability 分类阶段标记非目标请求并记录跳过原因。

## 8. 服务分层

- PromptTemplateService：草稿、发布、归档、不可变快照。
- PromptBindingService：分组/账号关系绑定、乐观锁、兼容性预检。
- PromptPolicyResolver：从一致快照和账号选择得到冻结策略。
- PromptRequestAdapter：纯函数式协议投影，返回新 body 和应用摘要。
- PromptEvaluationService：管理员显式发起、预算受控的独立评估。
- PromptAuditRepository：管理事件与运行事件。

以上均为拟新增职责，不表示已经存在同名类型。

普通热路径不请求任何外部模板站点。版本内容可按 version_id/hash 永久缓存，绑定和覆盖必须有失效机制。

第一版以数据库一致读取保证新请求配置正确，不引入允许过期绑定继续生效的 TTL 缓存。性能基准后再决定加入 generation 校验缓存；不得先依赖 Redis pub/sub 提供并不存在的强一致性。

总开关作为部署配置控制新增功能，默认 false。部署切换不承诺瞬间取消在途请求；紧急停用新请求用分组绑定禁用并等待已发请求结束。不会对已经输出的 SSE 做透明回放。

## 9. 管理 API 和页面

拟新增管理员接口（沿仓库统一路由前缀、鉴权与错误封装落地）：

| 方法/相对路径 | 行为 |
|---|---|
| GET/POST /prompt-templates | 列表/创建 |
| GET/PATCH /prompt-templates/:id | 详情/元数据与归档 |
| GET/PUT /prompt-templates/:id/draft | 读取/更新草稿 |
| POST /prompt-templates/:id/versions | 发布，携带 draft_revision 与幂等键 |
| GET /prompt-templates/:id/versions | 历史版本 |
| GET /prompt-versions/:id | 固定快照 |
| GET/PUT /groups/:id/prompt-binding | 绑定读写 |
| GET/PUT /groups/:gid/accounts/:aid/prompt-override | 关系级覆盖 |
| POST /prompt-previews | 无上游调用的结构预览 |
| POST /prompt-evaluations | 创建有限预算 A/B 任务 |
| GET /prompt-evaluations/:id | 结果、状态、错误 |
| POST /prompt-evaluations/:id/cancel | 取消未发送请求，不承诺撤销已计费调用 |

写入提交 expected_revision，冲突返回 409 和最新 revision，不覆盖他人配置。绑定仅允许已发布版本；不兼容返回 422 与账号/profile 原因。

页面：

1. 文本提示词列表：名称、最新发布版、绑定数、归档状态；不把 latest 当默认绑定。
2. 模板详情：正文编辑、版本 diff、发布记录、适用范围、验证标签。
3. 分组设置：开关、固定版本选择、影响账号数量、覆盖列表、回滚操作。
4. 账号在分组中的设置：继承/禁用/覆盖；显示父分组关闭时覆盖不生效。
5. 请求详情：最终版本、策略来源、各次尝试和跳过原因。
6. 评估页面：基线/候选对照、样本数、费用、失败与局限。

默认只显示元信息，查看正文与测试响应限管理员权限；不在普通用户 API 泄露模板正文或账号 ID。

## 10. 日志、错误和计费

请求记录：request_id、attempt_no、group_id、account_id、client_model、upstream_model、input_protocol、outbound_profile、policy_generation、binding_source、version_id、manifest_hash、applied、reason、added_bytes、apply_duration_ms。

reason 使用固定枚举：disabled、group_disabled、account_disabled、skipped_model_scope、skipped_non_text_task、applied、unsupported_profile、unsupported_history_mode、no_compatible_account、config_unavailable、invalid_config、adapter_error。

错误策略：

- 未启用：走原路径，不引入额外 DB 依赖。
- 已启用但无法读取/验证策略：503 config_unavailable，不带错误配置继续请求。
- 输入类型不支持：400，指出字段/协议，省略正文。
- 没有保持冻结策略的账号：503 no_compatible_account。
- 模板长度在保存/发布时阻止；最终请求超过已知限制时返回清晰错误，不截断客户历史。
- 未知模型上下文长度由上游正常报错，不伪造精确 token 预算。
- SSE 提交后错误使用现有流式错误行为，不自动换账号重播。

所有请求增量 token 都可能产生费用；以上游 usage 为计费依据。added_bytes 为精确字节，估算 token 必须标注 estimated，不能替代真实 usage。不开启自动双发生产请求。

运行审计默认不保存客户正文、Authorization、完整出站 body。评估响应默认保存 7 天、元数据保存 90 天，保留期可配置；版本和管理审计不随评估清理删除。运行事件写入失败应打日志和指标，不使已经成功的上游请求被重试；管理配置的审计与修改同事务。

## 11. A/B 验证

预览只验证构造，无费用。在线 A/B 必须显式创建任务，固定模型、渠道、采样参数、版本摘要、用例集、调用上限和超时；默认一对、无自动重试。取消只停止后续发送。

基线禁用本次模板，候选启用版本；其余网关已有兼容性处理保持一致。默认每对使用同一账号，不允许自动跨模型/渠道 fallback 污染实验；失败计入失败，不补样本凑成功率。多轮实验两组必须具有相同追问序列，分别保存各自 assistant 历史。

观察维度：注入是否正确、完成/拒答/追问/替代建议/部分完成、格式遵循、工具调用有效性、延迟、真实 usage。人工评估保留判定说明；不把“不包含拒绝关键词”自动等同成功。只做无害格式遵循、普通编程任务和合成指令层级用例即可完成工程验收，不把绕过限制作为上线条件。

既有候选导入为草稿，不自动发布、不默认启用；记录来源和已知测试模型。已有 Astra-on-Sol 测试按实际组合保留，不能改标成 Sol 专属候选。

## 12. 测试矩阵与发布门槛

| 维度 | 必测条件 |
|---|---|
| 版本 | 并发发布、幂等重放、已发布不可修改、被引用版本不可删除 |
| 绑定 | 缺配置、关闭、继承、覆盖、分组总开关、跨组账号隔离、复制默认关闭 |
| 协议 | 四种支持路径、字符串/数组消息、原 system/developer、工具调用、多模态内容保真 |
| 一致性 | 首次失败、同账号重试、跨账号、fallback 组、配置并发更新、不兼容候选耗尽 |
| 签名 | 最终 body 签名校验通过；注入后任何修改能被测试检测 |
| 流式 | 首 token 前失败与提交后失败、取消，不重复输出 |
| 历史引用 | 启用时明确拒绝未支持路径；关闭时保持既有行为 |
| 隐私 | 普通日志无正文/密钥，预览与评估需要管理员权限 |
| 回滚 | 切回旧版、关闭分组、关闭功能、旧程序可运行增量 schema |

发布门槛：所有支持路径契约测试通过；新功能关闭时既有代表性请求行为无回归；没有重复注入；已启用的配置失败不静默忽略；一次上线演练能从请求 ID 追到固定版本并切回旧版。

## 13. 部署与回滚

先新增 schema，再部署默认关闭的后端与 UI；所有新分组默认关闭。第一步只开测试分组。发布模板不改现有绑定。灰度用独立分组，不在同组内随机切模板。

回滚顺序：停评估任务 → 禁用测试分组绑定 → 等待在途请求结束 → 必要时关闭部署功能开关/回退程序。保留新表和历史版本，第一阶段不做破坏性 down migration。用后续向前迁移修复 schema。

## 14. 实施前必须完成的核实项

这些是工程任务，不要求用户重新选择产品方向：

- 找出各支持路径的最终 body 修改、签名、缓存键与审计调用，形成位置清单。
- 确认复合分组与 fallback 的 effective group 来源以及权限检查。
- 确认配置快照读取如何接入现有 scheduler，避免持锁跨网络调用。
- 检查已存在的 Claude/OAuth/MCP 注入冲突；不兼容路径先不开放绑定。
- 核实 migration 下一编号、Ent 生成命令、依赖注入入口和管理员路由约定。
- 对新增 DB 查询和日志写入做本地基准，确认不增加外部网络依赖。

对应的执行拆分见 [实施计划](2026-10-07-text-prompt-management-plan.md)。
