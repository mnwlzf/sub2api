# BPS 协议实现来源

本仓库的 BPS 适配保留 sub2api 自身的独立平台、账号调度、Redis 状态、计费和 HTTP 入口。下列固定版本用于协议移植与兼容性对照；不导入其它项目的部署配置、插件 ABI 或凭证采集逻辑。

## 冻结基线

| 来源 | 提交 | 用途 | 许可 |
|---|---|---|---|
| `huoji120/sub4api` | 最后功能提交 `e12188f2`（2026-10-15，已闭源停更） | 主参考：`backend/internal/service/openai_bps_*.go`（16 文件约 3.7k 行）、`migrations/245_openai_bps_platform.sql`、前端 `OpenAIBPSAccountFields.vue` / `BPSCredentialStatus.vue`、`docs/openai-bps.md` / `docs/openai-bps-sources.md` | LGPL-3.0 |
| `ranxi2001/sub2api` | `055a1cd1470b3b866d04aad8d1514bd34cac73ab` | 工具信封解码、custom 原始文本标记、结构化输出、工具目录、计划参数处理（`backend/internal/service/basispoints/`） | LGPL-3.0 |
| `Nonary/ghcp_proxy` | `ad23ce2db3b5212c0355762d981c3877322fb160` | 工具隧道、完整调用回放、加密 reasoning、计划成功回执、结果 ID 规范化（`excel_upstream.py`、`responses_replay_ids.py`） | Unlicense |
| `1812095643/sub2api-excel2api-plugin` | `ad2039077e5a625c846ed405d3473f7bc74b601a` | `tool/args` 信封、轮次保持、工具结果回放对照 | 未声明（仅对照，不复制代码） |

sub4api 的 `docs/openai-bps-sources.md` 另声明了原始协议来源链：`hloolx/codex2api`（README 声明 MIT，提交 `9d02d3f5…`、`c125e560…`、`20ff3e86…`、`d39f7e36…`、`4dea83ec…`），并对照 `JaxsonWang/cpa-plugin-oai-basispoints@05b2d97e…`。本仓库沿用其对照关系。

## 移植原则

1. 只移植**协议事实与算法**（信封格式、事件序列、错误码、Redis 状态语义、压缩/结构化策略），不复制品牌、部署脚本、插件 ABI 或任何凭证采集代码。
2. 新文件头部注明来源仓库、提交号与许可；本仓库同为 LGPL-3.0，与 LGPL-3.0 来源兼容。
3. 本地实现按 sub2api v0.2.16 架构重写：平台清单、账号模型、调度、计费、Redis 客户端均使用本仓库既有设施。
4. 上游协议非公开契约：若真实账号验收与冻结基线不一致，先记录差异再决定是否调整实现，禁止凭猜测修改协议。

## 验收要求

- 真实 BPS 账号逐项验证：端点、鉴权头、`store` 语义、SSE 事件序列、`usage` 字段、错误码。
- 差异记录在 `openspec/changes/add-openai-bps-platform/design.md` 的 Open Questions。
