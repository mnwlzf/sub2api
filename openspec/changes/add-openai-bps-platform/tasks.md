# 任务清单（BPS 平台）

> 状态：**提案阶段，未开始实现**。勾选状态在实现过程中更新；每阶段结束必须通过该阶段的验收项。

## 0. 立项与参考冻结

- [x] 0.1 冻结参考实现提交：`ranxi2001/sub2api@055a1cd1`（LGPL-3.0）、`Nonary/ghcp_proxy@ad23ce2d`（Unlicense）、`1812095643/sub2api-excel2api-plugin@ad203907`；把来源、提交号、文件映射写入 `docs/openai-bps-sources.md`
- [ ] 0.2 用真实 BPS 账号手工验证：端点、鉴权头、`store` 语义、SSE 事件序列、usage 字段、错误码（验收前必须完成）
- [x] 0.3 输出 `docs/openai-bps.md`（面向管理员：配置、支持范围、限制、风险提示）

## 1. 数据模型、平台注册与迁移

- [x] 1.1 `internal/domain` 新增 `PlatformOpenAIBPS = "openai_bps"` 与显示名/排序/图标映射
- [x] 1.2 迁移：核对后确认**无需新增迁移**——本地已在 `242_drop_platform_check_constraints.sql` 移除平台 CHECK 约束，平台由 `domain/platforms.go` 清单派生；在 tasks 中记录该偏差，不再新增迁移
- [x] 1.3 `service.Account` 平台判定（`IsOpenAIBPS`、`IsOpenAICompatible` 口径）、账号创建/更新校验（仅 oauth、凭证必填 AT、平台互斥）
- [ ] 1.4 `repository` 平台过滤、分组平台约束、账号列表/统计的 BPS 分支
- [x] 1.5 `config` 默认值：`gateway.openai_bps.enabled=false`、`default_models=[gpt-6-astra, gpt-5.6-sol]`、`context_window=200000`、`compact_threshold=160000`
- [x] 1.6 平台级 kill switch 生效路径：关闭时调度排除、入口返回明确错误
- [x] 1.7 验收：`go build ./...`；平台常量/校验/迁移单测通过；既有平台回归无变化

## 2. 凭证与账号生命周期

- [ ] 2.1 `service/openai_bps_credentials.go`：AT 解析、JWT payload 提取 `chatgpt_account_id`/`exp`（不验签）、脱敏、覆盖语义
- [ ] 2.2 `service/openai_bps_credential_state.go`：只读状态（自然过期/已撤销/认证失败/正常）、最近发现时间、错误码；认证失败仅按本次 AT+账号 ID 条件写入
- [ ] 2.3 账号状态保留：替换 token 不解除管理员停用/关闭调度；编辑留空保留原 token
- [ ] 2.4 连接测试 `openai_bps_test_connection.go`：保存并测试、双模型（文本/压缩）、HTTP 状态/上游模型/请求 ID/原始错误码
- [ ] 2.5 验收：凭证单测（提取/覆盖/过期/脱敏/条件写入）；账号创建/更新测试；状态保留测试

## 3. Responses 网关（文本闭环）

- [ ] 3.1 `handler` 新增 BPS Responses 入口与路由（仅 `/v1/responses` 与 `/responses/compact`；其它端点 404/明确错误）
- [ ] 3.2 `service/openai_bps_gateway.go`：固定端点、服务端鉴权头注入、客户端认证头剥离、请求体白名单归一
- [ ] 3.3 JSON/SSE 转发与错误透传；`store=false` 语义；超时/断流/客户端取消处理
- [ ] 3.4 会话身份：`session_id`/`prompt_cache_key` → 稳定 session key（含首条用户消息派生）；Redis 绑定账号/workspace
- [ ] 3.5 调度接入：账号选择、并发、代理、优先级；未绑定新会话的有限换号；已绑定不可用返回 `bps_account_unavailable`
- [ ] 3.6 验收：httptest 模拟上游覆盖 JSON/SSE/错误/取消；会话绑定与换号边界测试；文本闭环真实账号冒烟

## 4. 工具隧道

- [ ] 4.1 `service/openai_bps_protocol.go` + `openai_bps_envelope.go`：工具目录生成、重复合并/冲突报错、function（`name/arguments`，兼容 `tool/args`）与 custom（`summary/code`）信封编解码
- [ ] 4.2 `service/openai_bps_state.go`：Redis 命名空间、7 天闲置续期、原子绑定、原始 item/`id`/`call_id`/arguments 存取、跨实例读写
- [ ] 4.3 工具结果恢复：`function_call_output`（稳定 `fc_` ID）、加密 reasoning 重建（类型+空 summary+原密文）、`update_plan` 别名/状态/回执对齐
- [ ] 4.4 单工具交付与终态校验；只有本次声明的工具可输出；JSON Schema 校验、外部 `$ref` 拒绝、信封 1 MiB 上限
- [ ] 4.5 验收：工具目录/信封/回放/schema/未知工具/冲突声明/双层 JSON/连续工具往返测试；Redis miniredis + 独立客户端跨实例验证

## 5. 压缩

- [ ] 5.1 `service/openai_bps_turn.go`：`turn_id`/`agent_iteration` 保持、压缩前后轮次语义
- [ ] 5.2 真实摘要请求（关闭客户端工具）、摘要 Redis 存储、`bpscmp_v1_` 版本化引用、引用不发给上游
- [ ] 5.3 连续压缩与旧摘要兼容；跨实例恢复；无显式会话 ID 时由引用恢复原会话
- [ ] 5.4 压缩失败返回错误、不替换客户端历史；异常断流不伪造成功
- [ ] 5.5 验收：压缩轮次保持、连续压缩、旧摘要、跨实例、失败路径测试；真实账号「压缩 → 新对话 → 工具调用」冒烟

## 6. 结构化输出

- [ ] 6.1 `service/openai_bps_structured.go`：`json_object`/`json_schema` 提示词注入、schema 校验（≤1 MiB、仅文档内引用、`strict=false` 也校验）、大整数精度
- [ ] 6.2 JSON/SSE 同一套终态校验；未验证文本隔离；工具/拒答/压缩独立处理
- [ ] 6.3 失败语义：HTTP 502 / `response.failed` / `bps_invalid_structured_output`；已取得 usage 仍记录
- [ ] 6.4 验收：JSON/SSE、无效 schema、外部引用、大整数、未验证文本隔离、拒答测试

## 7. 计费、错误与监测

- [ ] 7.1 `billing` 接入：usage 记录（输入/缓存/输出）、失败响应用量、分组/渠道价格与倍率、用户平台配额
- [ ] 7.2 错误分类：认证失败停止调度、过期排除、`bps_model_not_allowed` 30 分钟模型冷却、403 不整号失效、400 不重试、429 `Retry-After`
- [ ] 7.3 认证失败写诊断与调度快照通知同事务；迟到响应条件写入
- [ ] 7.4 渠道监测 BPS 模板（Responses 探活）
- [ ] 7.5 验收：计费/错误/冷却/重试/监测测试；用量日志字段核对

## 8. 模型目录与 Composite

- [ ] 8.1 `openai_bps_catalog.go`：BPS 目录（串行工具/文本/HTTP）、别名映射、白名单、候选模型
- [ ] 8.2 `openai_bps_composite_catalog.go`：Composite 显式路由（精确别名/前缀筛选）、无候选返回空列表、普通 `gpt-*` 仍走 OpenAI
- [ ] 8.3 验收：目录/别名/白名单/Composite 路由/空候选测试；Codex 客户端模型目录冒烟

## 9. 管理端 API 与前端

- [ ] 9.1 handler/dto：账号创建/更新新增 BPS 字段（AT、账号 ID 覆盖、风险确认标记）、凭证状态只读字段
- [ ] 9.2 前端：平台徽标/筛选、账号创建编辑（AT + 风险二次确认 + 保存并测试）、`BPSCredentialStatus.vue`、分组模型映射/白名单、API Key 使用说明、渠道监测模板、i18n（zh/en）
- [ ] 9.3 验收：`pnpm run check:i18n`、`pnpm run typecheck`、相关 vitest 通过；手工走查创建→测试→使用

## 10. 集成、验收与上线

- [ ] 10.1 全量回归：`go build ./...`、`go test ./internal/... ./migrations/`（含既有平台）、前端 typecheck/vitest
- [ ] 10.2 真实账号端到端验收清单（见 `verification.md`）逐项打勾
- [ ] 10.3 灰度：单账号单分组 → 小流量 → 全量；观察错误率/工具成功率/压缩成功率/Redis 命中
- [ ] 10.4 文档：`docs/openai-bps.md`、`docs/openai-bps-sources.md`、变更说明与回滚步骤
- [ ] 10.5 回滚演练：关闭 `gateway.openai_bps.enabled`，确认既有平台不受影响
