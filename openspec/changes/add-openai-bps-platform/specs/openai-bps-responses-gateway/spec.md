## Purpose

规定 BPS Responses 网关的入口范围、固定上游、鉴权头注入、JSON/SSE 转发与会话身份，确保代理行为对客户端呈现为普通 Responses 服务，同时不泄露客户端认证信息、不越权换号。

## ADDED Requirements

### Requirement: 入口范围
系统 MUST 仅对 BPS 账号提供 HTTP Responses 入口（`/v1/responses`、`/responses/compact`）。WebSocket、Chat Completions、Claude Messages、图像、音频等端点 MUST NOT 对 BPS 平台开放，并 MUST 返回明确错误。

#### Scenario: 不支持的端点
- **WHEN** 客户端对 BPS 分组调用 `/v1/chat/completions`
- **THEN** 系统 MUST 返回明确错误，且 MUST NOT 向上游发起请求

### Requirement: 固定上游与鉴权注入
上游地址 MUST 固定为 `https://bps.openai.com/basispoints/api/responses`，MUST NOT 接受账号或客户端配置替换。系统 MUST 注入 `Authorization: Bearer <access_token>`、两个账号 ID 头与 `x-basispoints-auth-mode: chatgpt`。客户端提供的认证头（`Authorization`、`x-api-key`、cookie 等）MUST NOT 转发。

#### Scenario: 客户端伪造认证头
- **WHEN** 客户端请求携带 `Authorization: Bearer attacker`
- **THEN** 上游 MUST 只收到服务端注入的 BPS Access Token

#### Scenario: 账号配置自定义地址
- **WHEN** 账号配置了 `base_url`
- **THEN** BPS 网关 MUST 忽略该配置并使用固定端点

### Requirement: JSON 与 SSE 转发
系统 MUST 支持 Responses 的 JSON 与 SSE 两种响应形态，并保持事件序列与终止事件语义。客户端取消、上游断流、超时 MUST 被正确处理：不得伪造 `response.completed`，不得把失败写成成功。

#### Scenario: 上游断流
- **WHEN** SSE 流在 `response.completed` 之前中断
- **THEN** 系统 MUST 向客户端返回失败语义，MUST NOT 伪造完成事件

#### Scenario: 客户端取消
- **WHEN** 客户端在流式过程中断开
- **THEN** 系统 MUST 取消上游请求并释放并发槽位

### Requirement: 会话身份与 Redis 绑定
系统 MUST 由 `session_id` 或 `prompt_cache_key` 派生稳定会话键；两者都缺失时 MUST 以首条用户消息派生稳定身份。会话 MUST 按 API Key 隔离。建立上下文后 MUST 在 Redis 原子绑定账号与 ChatGPT workspace，绑定 MUST 跨实例可见。已绑定会话 MUST NOT 换号；绑定账号不可用时 MUST 返回 `bps_account_unavailable`，上下文缺失时 MUST 返回 `bps_context_expired`。

#### Scenario: 已绑定会话不换号
- **WHEN** 会话已绑定账号 A，账号 A 当前不可调度
- **THEN** 系统 MUST 返回 `bps_account_unavailable`，MUST NOT 把该会话切换到账号 B

#### Scenario: 未绑定新会话可换号
- **WHEN** 新会话尚未建立上下文且尚未输出，首次请求遇到网络错误/429/5xx
- **THEN** 系统 MAY 在有限次数内切换账号重试

### Requirement: 平台开关
系统 MUST 提供平台级开关（默认关闭）。关闭时 BPS 账号 MUST 不参与调度，入口 MUST 返回明确错误，既有数据与账号 MUST 保留。

#### Scenario: 平台关闭
- **WHEN** `gateway.openai_bps.enabled=false`
- **THEN** 所有 BPS 入口请求 MUST 被拒绝且不触达上游
- **THEN** 其它平台流量 MUST 不受影响
