# 参考实现冻结清单（BPS 协议）

本提案只移植**协议事实与算法**，不导入参考项目的部署配置、插件 ABI 或凭证采集逻辑。以下提交号是协议对照基线；上游漂移时先用该基线判断是实现 bug 还是协议变化。

## 主参考：sub4api（LGPL-3.0）

- 仓库：`huoji120/sub4api`（已闭源停更，最后功能提交 `e12188f2`，2026-10-15）
- 本地冻结副本：`.sub4api-review-20261023/`（浅克隆，depth 150）
- 相关文件映射：

| 参考文件（`backend/internal/service/`） | 行数 | 本提案用途 |
|---|---|---|
| `openai_bps_protocol.go` | 613 | 工具目录、信封编解码、schema 校验、压缩引用（`bpscmp_v1_`/`compaction_trigger`） |
| `openai_bps_envelope.go` | 248 | function/custom 信封与兼容形状（`name/arguments`、`tool/args`、`summary/code`） |
| `openai_bps_gateway.go` | 572 | 上游请求构造、JSON/SSE 转发、错误处理 |
| `openai_bps_state.go` | 230 | Redis 会话绑定、工具 item、压缩摘要 |
| `openai_bps_turn.go` | 51 | 轮次与迭代语义 |
| `openai_bps_structured.go` | 159 | 结构化输出提示词与本地校验 |
| `openai_bps_credentials.go` | 101 | AT/JWT 解析与脱敏 |
| `openai_bps_credential_state.go` | 180 | 凭证状态机与条件写入 |
| `openai_bps_catalog.go` | 40 | BPS 模型目录 |
| `openai_bps_composite_catalog.go` | 106 | Composite 显式路由 |
| `openai_bps_custom_transport.go` | 33 | 自定义 transport（连接复用/超时） |
| `openai_bps_test_connection.go` | 70 | 连接测试 |
| `openai_bps_test.go` | 627 | 参考测试矩阵（网关/工具/压缩/计费） |
| `openai_bps_alignment_test.go` | 320 | 协议对齐测试（SSE 事件/usage/错误码） |
| `openai_bps_structured_test.go` | 184 | 结构化输出测试 |
| `openai_bps_credential_state_test.go` | 217 | 凭证状态测试 |
| `migrations/245_openai_bps_platform.sql` | — | 平台枚举扩展（迁移形状测试 `openai_bps_platform_migration_test.go`） |
| `docs/openai-bps.md` / `docs/openai-bps-sources.md` | — | 管理员文档与来源链 |
| 前端 `OpenAIBPSAccountFields.vue` / `BPSCredentialStatus.vue` | — | 账号字段与凭证状态 |

参考实现合计约 3.7k 行 Go（含测试）。本提案按本地 v0.2.16 架构重写，不逐行照搬；行数仅用于估算工作量。

## 协议来源链（参考实现自身声明）

sub4api 的 `docs/openai-bps-sources.md` 声明了以下来源，本提案沿用其对照关系：

- `ranxi2001/sub2api@055a1cd1470b3b866d04aad8d1514bd34cac73ab`，`backend/internal/service/basispoints/`：工具信封解码、custom 原始文本标记、结构化输出、工具目录、计划参数。LGPL-3.0。
- `Nonary/ghcp_proxy@ad23ce2db3b5212c0355762d981c3877322fb160`，`excel_upstream.py`、`responses_replay_ids.py`：工具隧道、完整调用回放、加密 reasoning、计划成功回执、结果 ID 规范化。Unlicense。
- `1812095643/sub2api-excel2api-plugin@ad2039077e5a625c846ed405d3473f7bc74b601a`：`tool/args` 信封、轮次保持、工具结果回放对照。
- 原始 BPS 协议来源（sub4api 声明）：`hloolx/codex2api`（README 声明 MIT），提交 `9d02d3f5…`、`c125e560…`、`20ff3e86…`、`d39f7e36…`、`4dea83ec…`；另对照 `JaxsonWang/cpa-plugin-oai-basispoints@05b2d97e…`。

## 许可与署名要求

- 移植自 LGPL-3.0 项目（ranxi2001/sub2api、sub4api）的文件：新文件头部注明来源仓库、提交号与 LGPL-3.0；本仓库同为 LGPL-3.0，兼容。
- 移植自 Unlicense 项目（Nonary/ghcp_proxy）的算法：注明来源与提交号。
- 不复制参考项目的品牌、赞助商内容、部署脚本或任何凭证采集代码。

## 验收前必须完成

- [ ] 用真实 BPS 账号对照冻结基线逐项验证：端点、鉴权头、`store` 语义、SSE 事件序列、usage 字段、错误码。
- [ ] 若与冻结基线不一致，记录差异并在 `design.md` 的 Open Questions 中更新结论，再进入实现。
