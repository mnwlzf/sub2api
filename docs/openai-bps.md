# OpenAI BPS 平台接入

> 状态：**阶段 0–3 实现中**。平台默认关闭；文本闭环落地后再灰度，工具桥/压缩/结构化输出按 `openspec/changes/add-openai-bps-platform/tasks.md` 推进。

BPS（Basis Points / ChatGPT Excel 权益）是 ChatGPT 账号的一种额度形态，上游固定为
`https://bps.openai.com/basispoints/api/responses`，用 **ChatGPT Access Token** 鉴权、只提供
Responses HTTP 协议（JSON/SSE）。本仓库把它做成**独立平台** `openai_bps`，与 OpenAI 平台完全隔离。

## 前置条件

1. **Redis**：BPS 的会话绑定/工具 item/压缩摘要都存 Redis，7 天闲置过期；Redis 不可用时该平台不可用（不做进程内降级）。
2. **真实 BPS Access Token**：没有 token 只能做模拟上游测试。
3. **合规确认**：BPS 权益账号用于中转可能违反上游条款，存在封禁风险；平台默认关闭，开启前请自行评估。

## 配置

```yaml
gateway:
  openai_bps:
    enabled: false                 # 平台级 kill switch，默认关闭
    default_models:                # 分组未显式配置时的候选模型
      - gpt-6-astra
      - gpt-5.6-sol
    context_window: 200000         # 目录/压缩的保守本地值，不是上游配额承诺
    compact_threshold: 160000
```

- 关闭 `enabled` 后：BPS 账号不参与调度、入口返回明确错误，账号与数据保留。
- 上游地址固定，不接受账号 `base_url` 或客户端参数覆盖。

## 账号

- 平台：**OpenAI BPS**；类型仅 **oauth**。
- 凭证：`access_token`（ChatGPT Access Token，手填）；`chatgpt_account_id` 可选，留空时从 JWT payload 提取（只解析不验签）。
- 编辑时留空 `access_token` 表示保留原值；替换 token 不会解除管理员主动停用/关闭调度。
- 账号存在封禁风险，创建/保存时需二次确认。

## 支持范围（按阶段推进）

| 能力 | 状态 |
|---|---|
| Responses 文本（JSON/SSE） | 阶段 3 |
| function/custom/namespace 工具、`update_plan`、多轮工具结果、加密 reasoning | 阶段 4 |
| `/responses/compact` 与 `compaction_trigger` 压缩 | 阶段 5 |
| `text.format` 结构化输出（提示词约束 + 本地校验） | 阶段 6 |
| 计费、错误分类与冷却、渠道监测 | 阶段 7 |
| 模型目录、别名、Composite 显式路由 | 阶段 8 |
| 管理端账号字段/凭证状态/使用说明 | 阶段 9 |

**不提供**：WebSocket、Chat Completions、Claude Messages、图像/音频、Token 自动续期、Excel 本地凭证读取、上游配额探测。
不能使用 `previous_response_id` / `item_reference` 代替完整历史；客户端应保持 `session_id` 或 `prompt_cache_key` 并携带完整历史。

## 客户端配置（Codex）

```toml
# ~/.codex/config.toml
[model_providers.sub2api-bps]
name = "sub2api BPS"
base_url = "https://your-gateway/v1"
env_key = "SUB2API_BPS_KEY"
wire_api = "responses"
supports_websockets = false
```

API Key 使用 BPS 分组；模型目录由分组配置的别名/白名单决定。

## 排障

| 错误码 | 含义 | 处理 |
|---|---|---|
| `bps_platform_disabled` | 平台开关关闭 | 打开 `gateway.openai_bps.enabled` |
| `bps_account_unavailable` | 会话已绑定账号但该账号不可用 | 恢复账号或新建会话 |
| `bps_context_expired` | Redis 上下文超过 7 天闲置 | 新建会话 |
| `bps_upstream_error` | 上游非 2xx | 看诊断中的 HTTP 状态与原始错误码 |

## 回滚

关闭 `gateway.openai_bps.enabled` 即可停止全部 BPS 流量；账号、分组与 Redis 数据保留，不影响其它平台。
