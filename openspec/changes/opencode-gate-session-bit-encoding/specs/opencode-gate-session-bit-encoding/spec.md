## Purpose

规定 Sub2API 作为 OpenCode Zen 上游客户端时，如何让免费层模型（模型 ID 以 `-free` 结尾）通过上游门禁中「会话 id 位不变量」这一项要求：`X-Opencode-Session` 的 12 位 hex 字段低 12 位必须等于 `0xFFE`，且同一会话跨轮次必须稳定以保住上游 prompt cache。

本能力取代「用哈希派生 hex、只保证形状」的旧做法。

## ADDED Requirements

### Requirement: 会话 id 位不变量
系统 MUST 把「门禁合法会话 id」定义为**形状与位不变量同时满足**：匹配 `^ses_[0-9a-f]{12}[0-9A-Za-z]{14}$`（长度恒为 30）**且**第 13–15 个字符（hex 段低 12 位）等于 `ffe`。

系统 MUST 提供一个统一判定函数供所有调用点使用。MUST NOT 在任何调用点以「形状正则匹配」替代该判定。

#### Scenario: 实测放行的真实客户端 id
- **WHEN** 会话值为 `ses_ecb74175effe…`（hex 段低 12 位为 `ffe`）
- **THEN** 系统 MUST 判定其合法

#### Scenario: 实测被拒的真实客户端 id
- **WHEN** 会话值为 `ses_d1bfaf4aff65…`（hex 段低 12 位为 `f65`）
- **THEN** 系统 MUST 判定其非法

#### Scenario: 哈希派生值
- **WHEN** 会话值为任何由哈希派生 hex 的 `ses_` 值（形状合法、低 12 位不是 `ffe`）
- **THEN** 系统 MUST 判定其非法

#### Scenario: 形状非法
- **WHEN** 会话值为 UUID、短串、空串、缺 `ses_` 前缀、hex 段含大写或总长不是 30
- **THEN** 系统 MUST 判定其非法

### Requirement: 按官方客户端编码铸造
系统铸造会话 id 时，hex 段 MUST 与官方客户端 `tA(descending=true, counter=1)` 逐位相同，等价闭式为 `((epoch - 1 - now) << 12) | 0xFFE` 取低 48 位，其中 `epoch = 27 << 36`（毫秒）。

该等价性 MUST 由单元测试对多个时间点（含窗口边界与窗口外）逐位验证，MUST NOT 仅断言「低 12 位为 `ffe`」。

铸造 MUST 同时产出 14 位 base62 后缀。后缀 MUST 来自 `crypto/rand`（随机铸造路径），或在按来源铸造时由来源确定性派生。

MUST 提供「按来源铸造」与「随机铸造」两条路径：前者用于有稳定来源的常规请求，后者仅用于所有稳定来源都缺失时的最后兜底。

#### Scenario: 铸造值满足位不变量
- **WHEN** 系统调用任意一条铸造路径
- **THEN** 产出 MUST 以 `ses_` 开头、总长 MUST 为 30
- **THEN** 第 5–16 字符 MUST 为 12 位小写十六进制且低 12 位 MUST 为 `ffe`
- **THEN** 末 14 字符 MUST 属于 `0-9A-Za-z`

#### Scenario: 铸造值与官方编码逐位一致
- **WHEN** 给定任意毫秒时间戳
- **THEN** 系统产出的 hex 段 MUST 等于官方 `tA(descending=true, counter=1)` 的输出

#### Scenario: 自洽校验
- **WHEN** 系统在时刻 T 铸造会话 id
- **THEN** `(hex >> 12) + T` MUST 等于 `(27 << 36) - 1`

#### Scenario: 时钟越界不破坏不变量
- **WHEN** 系统时钟越过 `epoch` 或严重偏移
- **THEN** 铸造值的低 12 位 MUST 仍为 `ffe`

### Requirement: 有界铸造缓存与跨轮次稳定
系统 MUST 提供有界的会话铸造缓存，把「规范化之前的会话来源字符串」映射到已铸造的会话 id。缓存容量 MUST 有上限（当前为 4096），MUST 说明淘汰策略（当前为 FIFO）。

缓存的查、铸、写 MUST 在同一临界区内完成，使并发下同一来源只铸造一次。

缓存被淘汰后 MUST 重新铸造；重铸值 MUST 仍满足位不变量。系统 MUST NOT 因缓存未命中而返回错误。

#### Scenario: 同一来源跨轮次稳定
- **WHEN** 同一来源字符串的两个连续请求经过门禁
- **THEN** 两次出站 `X-Opencode-Session` MUST 相同

#### Scenario: 不同来源不同 id
- **WHEN** 两个不同来源字符串经过门禁
- **THEN** 两者 MUST 得到不同 id

#### Scenario: 并发同源
- **WHEN** 多个 goroutine 并发用同一来源取会话 id
- **THEN** 它们 MUST 得到同一个 id，且 MUST 只铸造一次

#### Scenario: 缓存有界
- **WHEN** 不同来源的数量超过缓存容量
- **THEN** 缓存条目数 MUST NOT 超过容量
- **THEN** 被淘汰来源的下一次请求 MUST 重新铸造出仍满足门禁的值

#### Scenario: 缓存命中不重复铸造
- **WHEN** 同一来源在缓存有效期内再次请求
- **THEN** 系统 MUST NOT 再次调用铸造逻辑

### Requirement: 哈希型客户端会话值必须重铸
系统 MUST 仅当客户端提供的会话值通过位不变量判定时才原样发送。形状合法但位不合法的值 MUST 被重铸，MUST NOT 原样透传。

理由：任何用哈希派生 hex 的实现（含上游插件与本站旧算法）都会产出这种值，原样透传会把上游 `403 FreeTierError` 直接带进请求。

重铸 MUST 走按来源铸造路径，使同一来源跨轮次稳定。

#### Scenario: 哈希型客户端值
- **WHEN** 客户端送来形状合法但低 12 位不是 `ffe` 的 `ses_` 值
- **THEN** 出站会话值 MUST NOT 等于客户端送来的值
- **THEN** 出站会话值 MUST 满足位不变量
- **THEN** 同一来源的下一次请求 MUST 得到相同的出站值

#### Scenario: 门禁合法的客户端值
- **WHEN** 客户端送来同时满足形状与位不变量的 `ses_` 值
- **THEN** 系统 MUST 原样发送该值

#### Scenario: 非 `ses_` 形状的客户端值
- **WHEN** 客户端送来 UUID 或任意非规范字符串
- **THEN** 系统 MUST 发送铸造出的合法值

### Requirement: 覆盖全部会话注入路径
位不变量判定与铸造 MUST 在门禁的全部会话注入路径上生效：Chat Completions、Responses、Anthropic Messages 三条出站 funnel，以及管理端连通性测试路径。

系统 MUST NOT 存在任何以「形状正则匹配」放行会话值的旁路。

#### Scenario: 三条出站 funnel
- **WHEN** 免费层请求经任一出站 funnel 发出
- **THEN** 出站 `X-Opencode-Session` MUST 满足形状与位不变量

#### Scenario: 无客户端会话标识
- **WHEN** 账号为 Zen（路径 `/zen/v1`，不满足既有自动生成条件）且客户端未提供任何会话标识
- **THEN** 系统 MUST 自行派生出满足位不变量的会话值
- **THEN** 出站请求 MUST NOT 因缺少会话头被上游拒绝

#### Scenario: 镜像到 affinity 头
- **WHEN** 系统写出 `X-Opencode-Session`
- **THEN** 同一值 MUST 同时写入 `X-Session-Affinity` 与 `X-Session-Id`
- **THEN** 该镜像 MUST NOT 被理解为门禁要求（实测非必需，仅为形态对齐）

### Requirement: UA 版本不是门禁检查项
系统 MUST 在代码注释中如实记录：门禁对 `User-Agent` 只要求 `opencode/<version>` 形态且不低于版本下限，**具体版本号不参与门禁判定**。系统 MUST NOT 把「对齐 UA 版本」记录为免费层 403 的修复手段。

UA 取值 MUST 仍不低于版本下限（实测 `1.17.9` 返回 `426 UpgradeRequired`、`1.18.0` 通过），且 MUST 保留该下限结论。

#### Scenario: 版本号不影响门禁
- **WHEN** UA 为 `opencode/1.18.31 …` 或 `opencode/1.19.0 …`
- **THEN** 门禁判定 MUST NOT 因版本号不同而不同（实测两者均 200）

#### Scenario: 低于下限
- **WHEN** UA 为 `opencode/1.17.9 …`
- **THEN** 上游 MUST 返回 `426 UpgradeRequired`

### Requirement: 排查工具与文档
系统 MUST 在仓库内提供可复用的排查手册，至少覆盖：错误类型分流（`403 FreeTierError` / `429 FreeUsageLimitError` / `426 UpgradeRequired`）、单变量消融法、现成工具与其用法、当前门禁要求清单（带日期与版本）、以及从官方发布产物重新推导算法的方法。

live 探针工具 MUST 对请求头中的凭据逐头掩码，并 MUST 在写出/打印 dump 前对整份内容再抹一遍原始 API key。live 验收测试中故意触发 `403` 的子测试 MUST 排在正向子测试之后，并在注释中说明原因（上游对 403 按 key 封禁约 15 分钟）。

#### Scenario: 探针不泄漏凭据
- **WHEN** 探针以真实 API key 运行并写出 dump
- **THEN** dump 中 MUST NOT 出现完整 key（`Authorization` 头与 `=== AUTH:` 行均为掩码形态）

#### Scenario: live 测试顺序
- **WHEN** 运行 live 门禁验收测试
- **THEN** 预期 200 的正向子测试 MUST 先于故意触发 403 的对照组执行
