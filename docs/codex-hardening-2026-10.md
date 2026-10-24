# Codex 协议加固与运营能力（2026-10 落地）

本文件记录本轮从 `huoji120/sub4api` 借鉴并落地到本仓库的 Codex 相关改动：**P0 出站线序去特征化、P1 turn-metadata 逐字节复刻与 UUIDv7 语义、P2 打票与票据管理、P3 请求时区/位置 persona、P4a 降智检测**。

> 参考实现：sub4api（LGPL-3.0，已闭源停更）。本仓库只借鉴其**协议事实与算法**，全部按本地 v0.2.16 基线重写/适配；`modeltrace_*` 保留 MIT 版权声明（见 `backend/internal/service/modeltrace_LICENSE`）。

---

## 一、P0 出站线序收口（默认生效）

**问题**：代理链为兼容历史行为会补齐一些官方 Codex 客户端从不发送的请求头，上游可据此判定"非官方客户端"。

**实现**：`backend/internal/service/openai_codex_outbound_headers.go`

- `projectCodexOutboundHeaders(h, websocket)`：**最后一道**出站头收口。
  - HTTP 删除：`OpenAI-Beta`、`session_id`、`conversation_id`、`x-codex-installation-id`、`accept-language`
  - WS 删除：上述去掉 `OpenAI-Beta`（WS 握手官方确实发送）
  - 保留官方头：`version`、`x-client-request-id`、`session-id`/`thread-id`（并补齐连字符成对）
- 调用点：
  - HTTP 非透传 `openai_gateway_forward.go::buildUpstreamRequest` 末尾
  - HTTP 透传 `openai_gateway_passthrough.go::buildUpstreamRequestOpenAIPassthrough` 末尾
  - WS 三条 lane 统一经 `openai_ws_forwarder_payload.go::buildOpenAIWSHeaders` 末尾
- **豁免**：legacy `/responses/compact` 与 messages 兼容桥仍以 `session_id` 归并上游会话，跳过收口（否则破坏其契约）。
- **配套补齐**：`ensureCodexOfficialSessionHeaders` 在缺少连字符会话头时按 `prompt_cache_key` 派生账号命名空间内的 UUIDv7 `session-id`/`thread-id`，并补齐 `x-client-request-id`；`session-id`/`thread-id`/`x-client-request-id` 同时加入 HTTP 与透传白名单，使真实客户端头可直接透传。
- 工具别名：`python__sub2api` → `python__custom`（`openai_codex_tool_names.go`），避免上游识别出代理名。
- **产品取舍**：OAuth /responses 线不再透传客户端自报的 `OpenAI-Beta`（含 `responses_multi_agent` 等 beta 特性）；legacy compact 仍保留独立 beta token。官方 HTTP 业务头集合本就不含该头。
- WS 附加：Codex OAuth 固定 WSv2 Beta（`responses_websockets=2026-02-06`）；透传官方条件头（parent-thread / subagent / memgen / timing-metrics）；`tool_namespaces_info` 只留在帧内 `client_metadata`，不塞 Upgrade 头。
- WS error 事件：新增 `openAIWSErrorEventPolicySignal`，识别 `server_err` 变体并归一为策略层可消费的 `server_error/upstream_error`，使 WS 失败也能触发账号冷却与 failover。

**回滚开关**：`gateway.disable_codex_identity_enforcement`（与既有身份强制统一开关共用；关闭后收口完全不动头集合）。

## 二、P1 turn-metadata 线序与 UUIDv7（默认生效）

- `backend/internal/service/openai_codex_turn_metadata.go`：按 codex-rs serde 声明序输出键、未知键走 flatten(BTreeMap) 字典序、只转义非 ASCII（`& < >` 不转义、`\u007f` 不转义），复刻官方 `AsciiJsonFormatter`。所有调用点（HTTP 头、client_metadata、WS 帧）自动获得一致线序。
- `openai_codex_fingerprint.go` / `openai_codex_account_identity.go` / `openai_compact_probe.go`：session/thread/turn/context-window 改为 **UUIDv7** 派生（时间戳取锚点之前的确定性值，绝不产生未来时间戳；原值已是 v7 时保留其 48-bit 时间戳并 clamp 到当前时刻），`installation_id` 仍为 v4，`window_id` 恒为 `<thread-uuid>:<ordinal>`（不再出现裸 UUID）。打票探测与降智探测的合成身份同步改为 v7 + 官方 window 形状。
- 单测：`openai_codex_turn_metadata_order_test.go`（键序/转义）、`openai_codex_outbound_headers_test.go`（HTTP/WS 名单与开关）。

## 三、P2 打票与票据管理（默认关闭）

**票据** = 官方 Codex 的 `x-codex-turn-state`（`gAAAAA` 前缀，默认 292 字节；team/business 账号 332 字节）。官方语义：本回合首个响应铸造、回合内原样回带。

- 核心：`backend/internal/service/codex_ticket_service.go`（Apply/Observe/Statuses/BlocksAccount/Start/Stop）、`codex_ticket_harvest.go`（扫描/探测/代理池/重试抖动/手动打票）、`codex_ticket_history.go`（流水/事件/失效/参与开关/节奏）、`codex_ticket_observation.go`（真实流量捕获 + 失效判定）。
- 存储：`accounts.extra["codex_turn_ticket:<model>"]`；流水/失效表见迁移 251–253。
- 注入时机：仅当客户端**没有**回带 turn-state 时注入（`openai_gateway_forward.go` / `openai_gateway_passthrough.go` / `buildOpenAIWSHeaders`）；Cookie 只复用 Cloudflare 基础设施项。
- 代理池：复用「IP 设置」代理，无可用代理时返回 `ErrCodexTicketNoProxy`，**不裸连上游**。
- 管理接口（`/api/v1/admin`）：`GET /accounts/:id/codex-tickets`、`/codex-ticket-history`、`/codex-ticket-events`、`POST /accounts/:id/codex-tickets/harvest`、参与开关、全局 `GET/PUT /codex-ticket/settings`、`/codex-ticket/pool` 等 14 条（见 `handler/admin/codex_ticket_handler.go`）。
- 配置（`gateway.openai_codex_ticket.*`，默认 `enabled=false`）：`target_length`(292)、`ttl_seconds`(3600)、`refresh_before_seconds`(600)、`harvest_probe_interval_seconds`(6)、`harvest_attempt_timeout_seconds`(90)、`harvest_retry_min/max_seconds`(10/30)、`harvest_refresh_seconds`(1800)、`models`(gpt-6-astra, gpt-5.6-sol)、`fail_closed`(false)。

**验真**：形状校验（`ticket_shape_v1`）+ ModelTrace 指纹验真（`ticket_fingerprint_v1`）。探测挑战与 P4a 同源；判定 `degraded` 时不落票（`reason_code=fingerprint_degraded`），`failed/uncertain` 不拦截以免误杀健康账号。真实上游取票率仍需线上验证。

**其它接线**：调度器接入 `BlocksAccount`（默认关闭、fail-open）；管理员编辑账号时保留票据键（`MergeCodexTicketExtra`）；备份导出前脱敏（`RedactCodexTicketExtra`）。

## 四、P3 请求时区 / 位置 persona（默认生效，账号可配）

- `openai_request_timezone.go`：账号级 `extra.openai_request_timezone`，默认 `Asia/Singapore`；可选列表排除中国大陆与港澳台；`ValidateOpenAIRequestTimezoneExtra` 在账号创建/更新时校验。
- `openai_request_locale.go`：只改明确标记的环境上下文——`<environment_context>` 内已有 `timezone` 替换、缺失 `current_date` 时按目标时区补齐（已有日期原样保留）；web_search `user_location` 的 country/region/city/timezone 按 persona 改写；普通对话文本不动。
- 接线：HTTP `Forward`、WS ingress、WS v2 passthrough 首帧与后续帧。
- 接口：`GET /api/v1/admin/accounts/openai-request-timezones` → `{default, timezones}`。

## 五、P4a 降智检测（默认关闭）

- `modeltrace_challenge.go`：1..355 范围、292–332 个整数的挑战（与 ModelTrace 参考实现逐字节一致）。
- `modeltrace_fingerprint.go` + `modeltrace_unified_bank.json`：ordered-block + nuisance-Hellinger z-score → softmax 概率，输出 `normal/degraded/uncertain/failed` 四态。
- `codex_probe_template.go` + `.jsonl`：可编辑探测模板（embed 默认值 + 设置覆盖，非法模板拒绝发请求）。
- `codex_degradation_service.go`：账号原生凭据直连 ChatGPT Codex responses，不经过公开网关、不消耗用户 API Key；`RunNativeDiagnostic` / `CheckAccount`。
- `codex_auto_degradation_check.go`：默认关闭，10 分钟间隔，判定降智的 OpenAI OAuth/SetupToken 账号临时停调 30 分钟；**只解除自己写入的封锁**（`source=codex_degradation_check`），其它来源封锁原样保留；探测失败不封不解。
- 管理接口：`GET /codex-degradation/models|settings|probe-template`、`PUT ...`、`POST /run`、`POST /accounts/:id/diagnose`、`GET /accounts/:id/results`。
- 配置（`gateway.openai_codex_degradation.*`）：`enabled`(false)、`interval_minutes`(10)、`block_minutes`(30)、`models`(["gpt-6.1-sol"])、`probe_timeout_seconds`(120)。
- 迁移 254–255（结果表 + due 扫描部分索引）。

## 六、迁移与装配

- 迁移：`251_codex_ticket_attempts.sql`、`252_codex_ticket_attempts_lifecycle.sql`、`253_codex_ticket_invalidations.sql`、`254_codex_degradation_probe_results.sql`、`255_codex_degradation_due_index.sql`（全部 `IF NOT EXISTS` 纯加性）。
- 装配：`cmd/server/wire.go` 新增 `provideCodexTicketService`；`service.ProviderSet` 新增 `ProvideCodexDegradationService`；`handler` 新增两个 provider；`routes/admin.go` 注册两组路由；`main.go` 调用 `service.StartCodexBackgroundWorkers`。
- 运行期接口：`openai_codex_feature_hooks.go`（网关 → 打票/降智的窄接口 + 进程级默认运行时）。

## 七、已知限制 / 后续

1. 打票指纹验真依赖 ModelTrace 指纹库覆盖的模型（当前库以 gpt-6.1-sol 为主）；库外模型走 uncertain 放行，真实上游取票率需线上验证。
2. 打票依赖代理池；生产需先在「IP 设置」配置可用代理。
3. 失效票据后跨副本调度缓存无法主动失效（最坏短窗口内继续注入旧票，下一次观测纠正）。
4. 自动降智检测已接入 Redis leader lock（hostname-pid 持有、TTL 3 个扫描周期）；Redis 不可用时降级为各实例都跑（探测只读、幂等）。
5. 账号编辑已保留票据键、备份导出已脱敏；批量导入/其它导出路径未单独处理。
6. 前端页面（票据中心、降智检测面板、时区下拉）本轮未做，接口已就绪。
7. 下线了 HTTP/WS 的下划线 `session_id`/`conversation_id` 与 `x-codex-installation-id` 头：这些头官方从不发送，原隔离值对上游不可见；如未来上游策略变化，可用 `gateway.disable_codex_identity_enforcement` 回滚。

## 八、验证

```bash
cd backend
go build ./...
go test -p 2 ./internal/service/ -run 'OpenAIGateway|OpenAIForward|Codex|ModelTrace|RequestLocale|ProjectCodexOutboundHeaders' -count=1
go test -p 2 ./internal/repository/ -run 'Codex' -count=1
go test -p 2 ./internal/handler/admin/ -run 'Codex' -count=1
go test -p 2 ./migrations/ -run 'Codex' -count=1
go vet ./internal/service/ ./internal/handler/admin/ ./internal/server/routes/ ./cmd/server/
```
