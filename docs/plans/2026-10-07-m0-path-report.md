# M0 路径核实报告：文本提示词注入的集成点

日期：2026-10-07
范围：只读代码追踪。未修改任何生产文件，未运行 build/test。
仓库：`sub2api`，工作树 HEAD `de7d5c0df`（`docs: design text prompt management`）+ 未提交的 Ent/Migration 产物（见第 9、10 节）。

约定：所有路径相对仓库根；`file:line` 为本次读取时的行号。

---

## 0. 关键结论速览（四条路径的唯一注入点）

| 入口 → 出站 | 函数 | 最终 body 变量 | 最后一次改写 body | 注入点（必须在此处或之前，且在任何后续改写之前） |
|---|---|---|---|---|
| Chat → Chat | `forwardAsRawChatCompletions` | `upstreamBody` | `sendCCUpstreamRequest` 内 `cc_pipeline.go:189` / `:192` | **`openai_gateway_cc_pipeline.go:188`（`sendCCUpstreamRequest` 入口，`ensureDeepSeekChatReasoningPlaceholders` 之前）** |
| Chat → Responses | `forwardAsChatCompletions` | `responsesBody` | `openai_gateway_chat_completions.go:368` | **`openai_gateway_chat_completions.go:369`（fast policy 之后、`buildUpstreamRequest`(384) 之前）** |
| Responses → Responses（原生 HTTP） | `Forward` | `body` | `openai_gateway_forward.go:751` | **`openai_gateway_forward.go:752`（`NormalizeCompactionTriggerInputOrder` 之后、`buildUpstreamRequest`(1059) 之前）**，但必须早于/重排 `:195` 的签名补齐 |
| Responses → Responses（透传） | `forwardOpenAIPassthrough` | `body` | `openai_gateway_passthrough.go:271`，且 `buildUpstreamRequestOpenAIPassthrough` 内 `:609` 仍会改 | **`openai_gateway_passthrough.go:578` 的 `buildUpstreamRequestOpenAIPassthrough` 内、`:609` 之前**，或 `:272`；同样受 `:195` 签名顺序约束 |
| Responses → Chat | `forwardResponsesViaRawChatCompletions` | `chatBody` | `openai_gateway_responses_chat_fallback.go:106`，然后 `cc_pipeline.go:189/192` | **`openai_gateway_responses_chat_fallback.go:107`（ollama clamp 之后、`sendCCUpstreamRequest`(125) 之前）**，或统一在 `cc_pipeline.go:188` |

**最重要的一条**：`sendCCUpstreamRequest`（`openai_gateway_cc_pipeline.go:174`）是所有 **Chat 出站**路径的公共汇聚点，它在 `http.NewRequestWithContext`（`:197`）之前还会改两次 body（`:189`、`:192`）。把 Chat 注入放在 `:188` 可以用一个点覆盖三条 Chat 出站路径，且后续两次改写都是 `sjson` 局部手术（只碰 `stream` / `tools` / `tool_choice` / assistant `reasoning_content`），不会删除新加的 system 消息。

---

## 1. 四条入站→出站文本路径

### 路由入口
- `internal/server/routes/gateway.go:378` `rootRoute(http.MethodPost, "/responses", bodyLimit, responsesHandler)`；`responsesHandler` 定义于 `:361`，OpenAI 平台走 `h.OpenAIGateway.Responses(c)`。
- `internal/server/routes/gateway.go:239` `/chat/completions`（v1 组）与 `:401` 根别名；OpenAI 平台走 `h.OpenAIGateway.ChatCompletions(c)`。
- `internal/server/routes/gateway.go:198` `/messages` → Anthropic 协议入口。

### 1.1 Chat 入站 → Chat 出站

入站 handler：
```go
// internal/handler/openai_chat_completions.go:23
func (h *OpenAIGatewayHandler) ChatCompletions(c *gin.Context)
```
- failover 循环：`internal/handler/openai_chat_completions.go:177`（`for {`）
- 账号选择：`:182` `SelectAccountWithSchedulerForCapability(..., OpenAIEndpointCapabilityChatCompletions, ...)`
- 本次尝试 body：`:252-255`（`forwardBody := body`，仅做渠道模型映射）
- 调用：`:263` `h.gatewayService.ForwardAsChatCompletions(c.Request.Context(), c, account, forwardBody, promptCacheKey, "")`

服务层：
```go
// internal/service/openai_gateway_chat_completions.go:54
func (s *OpenAIGatewayService) ForwardAsChatCompletions(ctx context.Context, c *gin.Context, account *Account, body []byte, promptCacheKey string, defaultMappedModel string) (*OpenAIForwardResult, error)
```
`:65 forwardAsChatCompletions(...)` 按 `shouldForwardOpenAIResponsesViaRawChatCompletions(account)`（`:189`）分流到：

```go
// internal/service/openai_gateway_chat_completions_raw.go:57
func (s *OpenAIGatewayService) forwardAsRawChatCompletions(ctx context.Context, c *gin.Context, account *Account, body []byte, defaultMappedModel string) (*OpenAIForwardResult, error)
```
- 最终 body 变量 `upstreamBody`，最后一次改写：`:192` `normalizeStrictChatDeveloperRoles(account, targetURL, upstreamBody)`
- 发送：`:206` `s.sendCCUpstreamRequest(ctx, c, account, targetURL, upstreamBody, clientStream, token, customUA, grokCacheIdentity)`

### 1.2 Chat 入站 → Responses 出站

同一个 `forwardAsChatCompletions`（`openai_gateway_chat_completions.go:65`），默认分支（未命中 raw-CC / 原生 Anthropic）：
- `:197-200` 解析 `apicompat.ChatCompletionsRequest`
- `:206-207` 模型映射 `resolveOpenAIForwardModel` / `normalizeOpenAIModelForUpstream`
- `:241-269` Responses 形状原样透传分支；`:270-284` 常规 `apicompat.ChatCompletionsToResponses(&chatReq)` → `responsesBody`
- `:302-333` Codex OAuth transform（`applyCodexOAuthTransformWithOptions` + `ensureCodexOAuthInstructionsField`）
- `:338-352` `prompt_cache_key` 注入
- `:354-368` `normalizeGPT6ResponsesSampling` + `s.applyOpenAIFastPolicyToBody`
- 发送：`:384` `s.buildUpstreamRequest(upstreamCtx, c, account, responsesBody, token, true, promptCacheKey, false)`，`:404` `s.doOpenAIUpstream(upstreamReq, proxyURL, account)`

### 1.3 Responses 入站 → Responses 出站

入站 handler：`internal/handler/openai_gateway_handler.go:388 func (h *OpenAIGatewayHandler) Responses(c *gin.Context)`
- failover 循环：`:666`（`for {`）
- 账号选择：`:675` `SelectAccountWithSchedulerForCapability(..., requiredCapability, ...)`
- 本次尝试 body：`:790` `attemptBody := h.deriveOpenAIForwardAttemptBody(reqLog, forwardBody, account, &passthroughFailoverState)`（`forwardBody` 定义于 `:572`，是不可变 canonical body）
- 调用：`:797` `h.gatewayService.Forward(c.Request.Context(), c, account, attemptBody)`

服务层：`internal/service/openai_gateway_forward.go:21 func (s *OpenAIGatewayService) Forward(ctx, c, account, body)`

两条子分支：
- **透传**：`internal/service/openai_gateway_passthrough.go:126 forwardOpenAIPassthrough(...)`；由 `openai_gateway_forward.go:273-303` 在 `passthroughEnabled` 时调用。body 在 `:271` 之后于循环 `:363` 内交给 `:370` `buildUpstreamRequestOpenAIPassthrough(...)`，发送 `:377` `doOpenAIUpstream`。
- **原生 Responses**：body 在 `openai_gateway_forward.go:721-751` 定稿（`ApplyPatches` / `marshalOpenAIUpstreamJSON` / `NormalizeCompactionTriggerInputOrder`），`:759-771` 可能再剥离失效密文项；`:1059` `s.buildUpstreamRequest(...)`，`:1078` `s.doOpenAIUpstream(...)`。

### 1.4 Responses 入站 → Chat 出站

分流点：`internal/service/openai_gateway_forward.go:208-210`
```go
if shouldForwardOpenAIResponsesViaRawChatCompletions(account) {
    return s.forwardResponsesViaRawChatCompletions(ctx, c, account, body)
}
```
```go
// internal/service/openai_gateway_responses_chat_fallback.go:27
func (s *OpenAIGatewayService) forwardResponsesViaRawChatCompletions(ctx context.Context, c *gin.Context, account *Account, body []byte) (*OpenAIForwardResult, error)
```
- `:65-71` `apicompat.ResponsesToChatCompletionsRequestWithOptions(&responsesReq, ...)`
- `:87` `chatReq.Model = upstreamModel`；`:88-90` `StreamOptions`
- `:92` `chatBody, err := json.Marshal(chatReq)` ← 出站 body 生成点
- `:96` `applyOpenAIFastPolicyToBody`；`:106` `clampOllamaCloudUpstreamMaxTokens`
- 发送：`:125` `s.sendCCUpstreamRequest(ctx, c, account, targetURL, chatBody, clientStream, apiKey, account.GetOpenAIUserAgent(), "")`

### 1.5 附：Messages 入站 → Chat 出站（不在四路径内，但同样走 CC 汇聚点）

```go
// internal/service/openai_gateway_messages_chat_fallback.go:31
func (s *OpenAIGatewayService) forwardAnthropicViaRawChatCompletions(ctx context.Context, c *gin.Context, account *Account, body []byte, defaultMappedModel string) (*OpenAIForwardResult, error)
```
入口为 `internal/service/openai_gateway_messages.go:28 ForwardAsAnthropic(...)`。

---

## 2. 每条路径的“最后一次改写”与 HTTP 发送点

### 公共汇聚点：Chat 出站
```go
// internal/service/openai_gateway_cc_pipeline.go:174
func (s *OpenAIGatewayService) sendCCUpstreamRequest(ctx, c, account, targetURL string, body []byte, stream bool, bearerToken, userAgent, grokCacheIdentity string) (*http.Response, error)
```
```go
:189  body = ensureDeepSeekChatReasoningPlaceholders(account, body)   // 改 assistant 消息
:192  body, err := applyOpenCodeFreeTierGateBody(account, body, openCodeGateDialectChat) // 改 stream/tools/tool_choice
:197  upstreamReq, err := http.NewRequestWithContext(upstreamCtx, http.MethodPost, targetURL, bytes.NewReader(body))
:250  resp, err := s.doOpenAIUpstream(upstreamReq, proxyURL, account)
```
→ **Chat 出站路径真正的最后改写点是 `:192`**，`http.NewRequest` 是 `:197`。注入必须放在 `:188`（`:189` 之前），否则 `ensureDeepSeekChatReasoningPlaceholders` / `applyOpenCodeFreeTierGateBody` 会成为注入之后的改写者（虽然它们只做局部手术，但设计文档第 7.4 节要求“模板适配 → 最终 body 约束检查”，语义上应把注入作为约束检查前的最后一步）。

### Responses 出站公共构建器
```go
// internal/service/openai_gateway_forward.go:1380
func (s *OpenAIGatewayService) buildUpstreamRequest(ctx, c, account, body []byte, token string, isStream bool, promptCacheKey string, isCodexCLI bool) (*http.Request, error)
:1415 body = normalizeDeepSeekResponsesRequestBody(account, body)
:1419 body, err := applyOpenCodeFreeTierGateBody(account, body, openCodeGateDialectResponses)
:1424 req, err := http.NewRequestWithContext(ctx, "POST", targetURL, bytes.NewReader(body))
:1547 applyMappedGPT55LiteCompatibility(req, account, body)   // ← 唯一会在 NewRequest 之后重写 req.Body 的函数
```
```go
// internal/service/openai_lite_mapped_gpt55.go:17
func applyMappedGPT55LiteCompatibility(req *http.Request, account *Account, body []byte) error
:29  if isOpenAIResponsesLiteWebSocketPayload(body) {
:35      req.Body = io.NopCloser(bytes.NewReader(body))   // 重新序列化 req.Body
```
→ 对 `gpt-5.5` + Responses-Lite + WS payload 组合，`applyMappedGPT55LiteCompatibility` 是 HTTP 层最后改写者。它只删除 `client_metadata` 里的一个键，不影响 `instructions`；但按“注入后不得再有 body 改写”的严格口径，注入应放在 `:1424` 之前（即 `buildUpstreamRequest` 内部 `:1415` 之前），透传构建器同理（`openai_gateway_passthrough.go:609` 之前）。

### 汇总表

| 路径 | 最后改写（file:line） | NewRequest / Do |
|---|---|---|
| Chat→Chat | `openai_gateway_cc_pipeline.go:192` | `:197` / `:250` |
| Chat→Responses | `openai_gateway_chat_completions.go:368` | `openai_gateway_forward.go:1424`（经 `:384`） / `openai_gateway_chat_completions.go:404` |
| Responses→Responses 原生 | `openai_gateway_forward.go:751`（`:759-771` 可能再剥密文） | `:1424`（经 `:1059`） / `:1078` |
| Responses→Responses 透传 | `openai_gateway_passthrough.go:271`，之后 `:609` | `:611` / `:377` |
| Responses→Chat | `openai_gateway_responses_chat_fallback.go:106`，之后 `cc_pipeline.go:192` | `cc_pipeline.go:197` / `:250` |

---

## 3. 请求签名 / Codex 请求签名

实现文件：`backend/internal/service/openai_responses_codex_signature.go`

```go
// :35
func ensureOpenAIResponsesCodexSignature(body []byte) ([]byte, bool, error)
:48   if !openAIResponsesIncludeHasCodexSignature(include) { ... sjson include += "reasoning.encrypted_content" }
:59   if key.Type != gjson.String || strings.TrimSpace(key.Str) == "" {
:60       next, err := sjson.SetBytes(out, "prompt_cache_key", deriveCodexSignaturePromptCacheKey(root))
```
```go
// :110
func deriveCodexSignaturePromptCacheKey(root gjson.Result) string
:111  for _, path := range []string{"client_metadata.session_id", "session_id", "conversation_id"}
:116  instructions := root.Get("instructions")
:117  input := root.Get("input")
:122  payload := make([]byte, 0, len(instructions.Str)+64)
:123  payload = append(payload, instructions.String()...)
:127      payload = append(payload, items[0].Raw...)   // input 数组首项
:132  sum := sha256.Sum256(payload)
:133  return codexSignatureCacheKeyPrefix + hex.EncodeToString(sum[:])[:32]
```

**结论（对注入顺序有直接影响）**：
- 这不是对出站 body 的密码学签名，而是**结构性补齐**：补 `include:["reasoning.encrypted_content"]` + 派生非空 `prompt_cache_key`。
- 但它**确实对 `instructions`（及 input 首项）做 sha256**（`:116-133`）。因此 **Responses `instructions` 注入必须在 `ensureOpenAIResponsesCodexSignature` 之前完成**，否则派生的 `prompt_cache_key` 不反映注入后的正文，违反设计文档第 7.4 节“签名不能在注入前算完”。
- 唯一调用点：
```go
// internal/service/openai_gateway_forward.go:195
if account.IsOpenAIResponsesEnsureCodexSignatureEnabled() {
:196    if signed, changed, signErr := ensureOpenAIResponsesCodexSignature(body); signErr != nil {
:199        body = signed
:200        originalBody = signed
```
  账号级开关：`internal/service/account.go:2329 IsOpenAIResponsesEnsureCodexSignatureEnabled()`（读 `Extra["openai_responses_ensure_codex_signature"]`，**默认 false**）。
- 该调用位于 `Forward` 的**早期**（在 `:208` raw-CC 分流、`:273` passthrough 分流、`:310-751` 全部 requestView patch 之前）。这与“注入应在最后改写点”冲突。
- **推荐处理**：把 `:195-202` 的签名补齐下沉到注入点之后（原生 Responses 在 `:752` 之后、`buildUpstreamRequest`(1059) 之前；透传在 `forwardOpenAIPassthrough` 注入点之后、`:370` 之前），并在下沉后重新读取 `promptCacheKey`（`:204` 目前紧跟其后读取）。
- 历史遗留：`SettingKeyEnableCCHSigning` 已废弃为 no-op（`internal/service/domain_constants.go:685`，`internal/service/settings_view.go:249`），`cch=` 段已从 billing block 移除（`internal/service/gateway_claude_oauth_body.go:923`）。**cch 不再与请求体签名有关**，不需要考虑。

---

## 4. 缓存键 / 内容哈希

| 位置 | 计算对象 | 是否需反映注入后的 prompt |
|---|---|---|
| `internal/service/openai_gateway_scheduling.go:163 GenerateSessionHash` → `internal/service/openai_content_session_seed.go:25 deriveOpenAIContentSessionSeed`（字段集含 `instructionsField`、`messagesField`，见 `:30-39`） | 入站 body（`model`/`tools`/`functions`/`instructions`/`messages`/`input`） | **不需要，且必须不受影响**。调用点在 handler 层：`internal/handler/openai_chat_completions.go:162`、`internal/handler/openai_gateway_handler.go:632`，都用注入前的 canonical body。若在 handler 之前注入，会改变 sticky 账号粘性 → 禁止。 |
| `internal/service/openai_gateway_forward.go:760 s.GenerateSessionHash(c, body)`（失效密文 lineage） | 出站 body | 需在注入**之前**取样（`:757 lineageEntryBody := body`），否则 lineage 会话键漂移。 |
| `internal/service/usage_billing.go:139 HashUsageRequestPayload(payload []byte) string`（`sha256.Sum256(payload)`） | 入站 body（`internal/handler/openai_chat_completions.go:269`、`internal/handler/openai_gateway_handler.go:803` 传的是原始 `body`） | 审计/去重口径若需覆盖出站正文，需要**新增**一个出站 body 哈希；当前哈希天然不含注入。 |
| `internal/service/openai_responses_rejected_field_retry.go:111 s.seenBodyHashes[sha256.Sum256(body)]` | 出站 body | 确定性注入下哈希稳定，无需改；但要求注入**幂等**（同一 attempt 重复构建结果一致）。 |
| `internal/service/openai_responses_codex_signature.go:132` | `instructions` + input 首项 | **需要**，见第 3 节。 |
| `internal/service/openai_gateway_grok_cache.go:112-122 resolveGrokCacheIdentity`（读 `prompt_cache_key` / `X-Grok-Conv-Id`） | 会话标识，非内容 | 调用点 `openai_gateway_chat_completions_raw.go:86` 在注入前，无需改。 |
| `internal/service/openai_codex_fingerprint.go:517-575`、`internal/service/openai_codex_account_identity.go:181-243` | 账号作用域化 `prompt_cache_key` | 与模板正文无关；注意它们运行在 `forward.go:568` / `:555`，晚于 `:195` 的签名派生，会覆盖派生的 key。 |
| `internal/service/pricing_service.go:603`、`internal/service/openai_codex_models_service.go:2062`、`internal/service/openai_images.go:209` | 各自的 body | **未确认 / Unverified**：未逐行确认这三个哈希是否作用于“出站对话 body”。从文件名判断更可能是定价快照、模型清单与图片请求缓存，与文本注入无关；实施前应逐条确认。 |

补充：`prompt_cache_key` 是**会话派生**而非内容派生（见 `deriveCodexSignaturePromptCacheKey` 的优先级注释 `:105-109`）。因此注入模板正文**不应**改变 `prompt_cache_key` 的会话语义；只有“缺失时兜底派生”这一支路会被 `instructions` 影响。

---

## 5. 有效分组与账号选择 / 失败转移

### 分组模型
```go
// internal/service/group.go:18
type Group struct {
:79   ClaudeCodeOnly  bool
:80   FallbackGroupID *int64
:82   FallbackGroupIDOnInvalidRequest *int64
:91   MCPXMLInject bool
```
- 复合分组：`internal/service/admin_group.go:231 requireCompositeGroup`，调用点 `:140/:153/:173/:199/:217`。
- fallback 链校验：`internal/service/admin_group.go:713-716`（沿 `FallbackGroupID` 走链）。
- 请求时复合路由：中间件 `internal/server/routes/gateway.go:549 compositeTargetPlatformMiddleware`（挂在 `:194`），内部用 `internal/service/composite_route_resolver.go:25 Resolve(ctx, groupID, model, endpoint)`；决策写入 context（`WithCompositeRouteDecision`，`:590`）。
- 请求时**有效分组**：OpenAI 网关直接用 `apiKey.GroupID`（`internal/handler/openai_chat_completions.go:174/184/236`、`internal/handler/openai_gateway_handler.go:663/677`）。`apiKey.Group` 由 `internal/repository/api_key_repo.go:205-206`（含 `FallbackGroupID`、`FallbackGroupIDOnInvalidRequest`）与 `:1016-1017` 装配。
- 请求时 fallback 分组**唯一**出现处（Anthropic prompt-too-long）：`internal/handler/gateway_handler.go:1012-1052`
```go
:1013 fallbackGroup, err := h.gatewayService.ResolveGroupByID(c.Request.Context(), *fallbackGroupID)
:1030 fallbackAPIKey := cloneAPIKeyWithGroup(apiKey, fallbackGroup)
```
  `ResolveGroupByID` 定义于 `internal/service/gateway_scheduling.go:864`。**注意**：该 fallback 在 `c.Request.Context()` 上换 key 后重试，属于“新逻辑请求”语义边界，需要单独决定提示词策略身份是否继承。

### 账号选择
- 公开入口：`internal/service/openai_account_scheduler.go:2116 SelectAccountWithSchedulerForCapability(ctx, groupID *int64, previousResponseID, sessionHash, requestedModel string, excludedIDs map[int64]struct{}, requiredTransport OpenAIUpstreamTransport, requiredCapability OpenAIEndpointCapability, requireCompact, previousResponseCanMove, useUpstreamTokenCost bool, platformOverride ...string)`；同文件 `:2100 SelectAccountWithScheduler`、`:2137 SelectAccountWithSchedulerForImages`。
- 核心：`:2163 selectAccountWithScheduler(...)` → `:2298 selectAccountWithSchedulerOnce(...)`。
- 高级调度器：`:382 func (s *defaultOpenAIAccountScheduler) Select(...)`（权重/粘性/TopK/负载，见 `:1400 selectByLoadBalance`、`:495 selectBySessionHash`）。
- 旧路径：`internal/service/openai_gateway_scheduling.go:1130 SelectAccountWithLoadAwareness` → `:1139 selectAccountWithLoadAwareness(...)`；候选池 `:1501 listSchedulableAccounts(ctx, groupID *int64, platform string)`。
- 并发槽：`internal/service/openai_gateway_scheduling.go:1533 tryAcquireAccountSlot`。

### 账号重试 / 失败转移（“同一提示词策略跨重试”必须在这里收口）
- **Responses / Messages**：`internal/handler/openai_gateway_handler.go:666`（`for {`）
  - 选号 `:675`，`failedAccountIDs` 于 `:645` 创建、`:744`/`:771`/失败分支累加
  - 每次尝试 body：`:790 deriveOpenAIForwardAttemptBody(..., forwardBody, account, &passthroughFailoverState)`
  - 发送：`:797 Forward(...)`
  - 服务层同账号重试：`:915-937`（`failoverErr.RetryableOnSameAccount` + `sameAccountRetryCount[account.ID]`）
- **Chat Completions**：`internal/handler/openai_chat_completions.go:177`（`for {`）
  - 选号 `:182`，body `:252-255`，发送 `:263`
- **Messages**：`internal/handler/openai_gateway_handler.go:1152 func Messages(c *gin.Context)`（同构循环）
- 服务层 HTTP 失败转移：`internal/service/openai_gateway_cc_pipeline.go:82 failoverOpenAIUpstreamHTTPError(...)`；错误类型 `UpstreamFailoverError`（`internal/service/openai_gateway_forward.go` 内构造，例如 `:1200 newOpenAIAccountFailoverError`）。
- 分组内 fallback 也存在于 `internal/service/openai_gateway_forward.go:1046-1049`（`rejectedFieldRetryState`）与 `:1047 compactModelFallbackRetried`（同账号重试，不是换号）。

**建议冻结位置**：在 `for {` 之前（`openai_chat_completions.go:177` 之前、`openai_gateway_handler.go:666` 之前）解析并冻结策略；把注入实现为“从不可变 `body`/`forwardBody` 派生的纯函数 + 冻结策略”，在每个 `forwardBody`/`attemptBody` 构造点调用（`:252`、`:790`）。这样天然满足“每次尝试从原始 body 构建、只注入一次、跨重试同策略”。

---

## 6. 现有提示词注入先例

### 6.1 Claude OAuth system prompt 注入（应作为主要模板）
设置读取：
```go
// internal/service/gateway_claude_oauth_body.go:1247
func (s *GatewayService) claudeOAuthSystemPromptInjectionSettings(ctx context.Context) (bool, string, string)
:1251  return s.settingService.GetClaudeOAuthSystemPromptInjectionSettings(ctx)
```
注入点（**两处**）：
```go
// internal/service/gateway_claude_oauth_body.go:390（通用版，供 OpenAI 协议兼容层复用）
:390  systemPromptInjectionEnabled, systemPrompt, systemPromptBlocks := s.claudeOAuthSystemPromptInjectionSettings(ctx)
:391  if systemPromptInjectionEnabled {
:392      systemPromptBlocks = claudeOAuthSystemPromptBlocksForModel(model, systemPromptBlocks)
:393      body = rewriteSystemForNonClaudeCodeWithPromptBlocks(body, normalizeSystemParam(systemRaw), systemPrompt, systemPromptBlocks)
```
```go
// internal/service/gateway_forward.go:222（/v1/messages 主路径）
:222  systemPromptInjectionEnabled, systemPrompt, systemPromptBlocks := s.claudeOAuthSystemPromptInjectionSettings(ctx)
:223  if systemPromptInjectionEnabled {
:224      if err := replaceBody(rewriteSystemForNonClaudeCodeWithPromptBlocks(body, systemRaw, systemPrompt, systemPromptBlocks)); err != nil {
```
system block 构造 helper（可直接借用其“拼数组”思路）：
- `internal/service/gateway_claude_oauth_body.go:527 normalizeSystemParam(system any) any`
- `:573 injectClaudeCodePrompt(body []byte, system any) []byte`（前置追加范式）
- `:906 rewriteSystemForNonClaudeCodeWithPromptBlocks(body []byte, system any, expansionPrompt string, blocksConfig string) []byte`
  - `:925 buildClaudeOAuthSystemPromptBlocksJSON(...)`、`:934 setJSONRawBytes(body, "system", buildJSONArrayRaw(systemBlocks))`
  - `:940-981` 把原 system 迁移成 messages 头部的 user/assistant 对（`buildJSONArrayRaw(items)`）
- 序列化 helper：`marshalAnthropicSystemTextBlock`、`buildJSONArrayRaw`、`setJSONRawBytes`

**注入时机相对签名的位置**：Claude 路径**没有** body 签名；注入发生在 `Forward` 早期（`gateway_forward.go:222`），而 HTTP 请求构造在 `gateway_forward.go:393` → `internal/service/gateway_upstream_request.go:21 buildUpstreamRequest` → `:131 http.NewRequestWithContext`。也就是说**先例是“早注入 + 依赖后续步骤保留 system 字段”，而不是“最后改写点注入”**。这与 Responses 路径需要满足的签名顺序（第 3 节）不同，不能照抄时机，只能照抄 block 构造与 helper 风格。

设置键与缓存：
- `internal/service/domain_constants.go:690 SettingKeyEnableClaudeOAuthSystemPromptInjection = "enable_claude_oauth_system_prompt_injection"`；`:694 SettingKeyClaudeOAuthSystemPromptBlocks`
- 读取：`internal/service/setting_gateway_runtime.go:1015 GetClaudeOAuthSystemPromptInjectionSettings`
- 进程内缓存结构：`internal/service/setting_gateway_runtime.go:861-968`（`fp, mp, cch, claudeOAuthSystemPromptInjection, cacheTTL1h, ...`，60s TTL + singleflight）

### 6.2 MCP XML 注入
- 开关：分组字段 `internal/service/group.go:91 MCPXMLInject bool`（Ent 字段 `mcp_xml_inject`，`internal/repository/group_repo.go:136/:316`）；默认 true，见 `internal/service/admin_group.go:514-517`（“默认为 true，仅当显式传入 false 时关闭”）。
- 实际注入：`internal/pkg/antigravity/request_transformer.go:401-403`
```go
if opts.EnableMCPXML && hasMCPTools(tools) {
    parts = append(parts, GeminiPart{Text: mcpXMLProtocol})
}
```
  常量 `:266 const mcpXMLProtocol = ...`；选项结构 `:48 EnableMCPXML bool`，默认 `:54 EnableMCPXML: true`。
- 注入位置是 Antigravity/Gemini 的 `systemInstruction` parts 数组，**在 identity patch 与用户 system 之后、结束标记之前**（`:385-408`）。

### 6.3 第三个、也是最贴近本需求的先例：`ForcedCodexInstructionsTemplate`
部署配置驱动、直接写 Responses 顶层 `instructions`：
- 配置：`internal/config/config.go:1028-1033`（`ForcedCodexInstructionsTemplateFile` / `ForcedCodexInstructionsTemplate`），启动时读盘：`:1940-1946`。
- 实现：`internal/service/openai_codex_instructions_template.go:18 applyForcedCodexInstructionsTemplate(...)`，渲染 `:40 renderForcedCodexInstructionsTemplate(...)`。
- 应用点：`internal/service/openai_gateway_messages.go:233-253`
```go
:235  forcedTemplateText = s.cfg.Gateway.ForcedCodexInstructionsTemplate
:241  existingInstructions, _ := reqBody["instructions"].(string)
:245  if _, err := applyForcedCodexInstructionsTemplate(reqBody, forcedTemplateText, forcedCodexInstructionsTemplateData{
:246      ExistingInstructions: strings.TrimSpace(existingInstructions), ... })
```
- 该路径**没有**调用 `ensureOpenAIResponsesCodexSignature`（`openai_gateway_messages.go:265` 反而 `delete(reqBody, "prompt_cache_key")`），所以顺序冲突只存在于 `Forward`。

---

## 7. 管理端 handler + 路由 + DI 约定

### 路由注册
- 文件：`internal/server/routes/admin.go`
- 总入口：`:18 func RegisterAdminRoutes(v1 *gin.RouterGroup, h *handler.Handlers, adminAuth middleware.AdminAuthMiddleware, auditLog middleware.AuditLogMiddleware, stepUpAuth middleware.StepUpAuthMiddleware, settingService *service.SettingService, panelRateLimiter *middleware.PanelRateLimiter)`
- 前缀与中间件：
```go
:27  admin := v1.Group("/admin")
:28  admin.Use(gin.HandlerFunc(adminAuth))
:30  admin.Use(panelRateLimiter.Global())
:32  admin.Use(gin.HandlerFunc(auditLog))
:33  admin.Use(middleware.AdminComplianceGuard(settingService))
```
  即 **admin 鉴权中间件在 `admin.go:28` 应用**；后续 `registerXxxRoutes(admin, h)` 只需按子组挂路由。
- 最佳模板（小而完整，含完整 CRUD）：
```go
// internal/server/routes/admin.go:756
func registerTLSFingerprintProfileRoutes(admin *gin.RouterGroup, h *handler.Handlers) {
	profiles := admin.Group("/tls-fingerprint-profiles")
	{
		profiles.GET("", h.Admin.TLSFingerprintProfile.List)
		profiles.GET("/:id", h.Admin.TLSFingerprintProfile.GetByID)
		profiles.POST("", h.Admin.TLSFingerprintProfile.Create)
		profiles.PUT("/:id", h.Admin.TLSFingerprintProfile.Update)
		profiles.DELETE("/:id", h.Admin.TLSFingerprintProfile.Delete)
	}
}
```
- 另一模板（同包独立 handler 包，`securityaudit.PromptAdminHandler`）：`internal/server/routes/admin.go:139 registerPromptAuditRoutes`。**命名警示**：仓库中 `prompt-*` 已被“提示词审计（prompt audit）”占用（`/admin/prompt-audit`），本功能应使用 `/admin/prompt-templates` 等不冲突前缀。

### Handler struct / DI 模式
```go
// internal/handler/admin/tls_fingerprint_profile_handler.go:13
type TLSFingerprintProfileHandler struct {
	service *service.TLSFingerprintProfileService
}
// :18
func NewTLSFingerprintProfileHandler(service *service.TLSFingerprintProfileService) *TLSFingerprintProfileHandler {
	return &TLSFingerprintProfileHandler{service: service}
}
```
- 注册到聚合结构：`internal/handler/handler.go:9 AdminHandlers`，字段样例 `:33 TLSFingerprintProfile *admin.TLSFingerprintProfileHandler`；顶层 `:50 Handlers`，`:60 Admin *AdminHandlers`。
- handler 方法形状（`tls_fingerprint_profile_handler.go`）：
```go
:56  func (h *TLSFingerprintProfileHandler) List(c *gin.Context) {
:57      profiles, err := h.service.List(c.Request.Context())
:58      if err != nil { response.ErrorFrom(c, err); return }
:62      response.Success(c, profiles)
:68  id, err := strconv.ParseInt(c.Param("id"), 10, 64)
:91  if err := c.ShouldBindJSON(&req); err != nil { response.BadRequest(c, "Invalid request: "+err.Error()); return }
```
- 响应/错误 helper：`response.Success`、`response.ErrorFrom`、`response.BadRequest`、`response.NotFound`（`internal/pkg/response`）。

### DTO 模式
- **本仓库有两种并存风格**：(a) 与 handler 同文件定义请求 DTO（`tls_fingerprint_profile_handler.go:23 CreateTLSFingerprintProfileRequest`、`:39 Update...`，`binding:"required"` 标签）；(b) 共享 DTO 包 `internal/handler/dto/`（`dto/types.go`、`dto/mappers.go`），如分组字段 `internal/handler/dto/types.go:139-141`。
- 若需前端复用与审计字段，优先 (b)；若功能自包含，用 (a) 更轻。

### DI（google/wire）
- 使用 `github.com/google/wire`（`cmd/server/wire.go:23`）。
- ProviderSet 位置：
  - handler：`internal/handler/wire.go:255-302`（admin handler 列表 `:260-297`，末尾 `:300-301 ProvideAdminHandlers, ProvideHandlers`）
  - service：`internal/service/wire.go:851 var ProviderSet = wire.NewSet(`，`:859 NewGroupService`，`:789 svc := NewSettingService(settingRepo, cfg)`
  - repository：`internal/repository/wire.go:67 var ProviderSet = wire.NewSet(`，`:70 NewGroupRepository`，`:88 NewSettingRepository`
- 装配入口：`cmd/server/wire.go:34 initializeApplication`，`:35-48 wire.Build(...)`
- 生成物：`cmd/server/wire_gen.go`（样例 `:260 tlsFingerprintProfileHandler := admin.NewTLSFingerprintProfileHandler(tlsFingerprintProfileService)`）
- 重新生成命令：`backend/Makefile:11-13`
```make
generate:
	go generate ./ent
	go generate ./cmd/server
```
→ 新增 service/repository/handler 必须同时改 `wire.go` 并**重新生成 `wire_gen.go`**（M0 阶段建议先确认生成工具可用）。

---

## 8. 设置 / 特性开关模式

### 全局设置（DB setting）
1. 常量：`internal/service/domain_constants.go`（`SettingKey*`），样例 `:690 SettingKeyEnableClaudeOAuthSystemPromptInjection`、`:694 SettingKeyClaudeOAuthSystemPromptBlocks`。
2. 视图结构：`internal/service/settings_view.go:250 EnableClaudeOAuthSystemPromptInjection bool`。
3. 读取器（带进程内缓存 + singleflight，热路径零 DB）：`internal/service/setting_gateway_runtime.go:1015`，缓存字段装载于 `:903-912`、`:937-942`。
4. 管理端读写：`internal/handler/admin/setting_handler_update.go:255-257`（`*bool`/`*string` 指针用于部分更新）、`:1762-1772`；审计差异 `internal/handler/admin/setting_handler_audit.go:464-471`。

### 部署配置开关（`cfg.Gateway.*`）
- 结构体：`internal/config/config.go:981 type GatewayConfig struct`
- “默认 false”布尔样例：
```go
// internal/config/config.go:1027
CodexImageGenerationBridgeEnabled bool `mapstructure:"codex_image_generation_bridge_enabled"`
```
  零值即 false，无需在 `Load` 里显式赋默认值（对比取反义命名 `:1021 DisableCodexIdentityEnforcement`，注释说明“取反义命名是为了让零值安全”）。
- 文件型配置 + 启动期缓存样例：`:1030 ForcedCodexInstructionsTemplateFile`、`:1033 ForcedCodexInstructionsTemplate`（`mapstructure:"-"`，`Load` 内 `:1940-1946` 读盘）。
- 运行期读取样例：`internal/service/openai_gateway_passthrough.go:703 s.cfg.Gateway.ForceCodexCLI`；`internal/service/openai_gateway_forward.go:1509`；`internal/service/openai_gateway_messages.go:234-235`。

**“默认关闭”开关推荐做法**：
1. `internal/config/config.go` 的 `GatewayConfig` 增加 `TextPromptInjectionEnabled bool \`mapstructure:"text_prompt_injection_enabled"\``（零值 false，不需要额外默认值代码）。
2. 热路径读取 `s.cfg != nil && s.cfg.Gateway.TextPromptInjectionEnabled`（与 `:703`、`:1509` 同风格，无锁无 DB）。
3. 总开关为 false 时**直接走原路径、不引入任何 DB 依赖**（设计文档第 10 节要求）。
4. 分组/账号级策略仍走 DB（第 5 节冻结流程）；`cfg.Gateway` 只作为部署级熔断。

---

## 9. Ent + 仓储层约定

### Ent schema 结构
```go
// backend/ent/schema/group.go:18
type Group struct{ ent.Schema }
:23 func (Group) Annotations() []schema.Annotation {
:24     return []schema.Annotation{ entsql.Annotation{Table: "groups"} }
:29 func (Group) Mixin() []ent.Mixin {
:30     return []ent.Mixin{ mixins.TimeMixin{}, mixins.SoftDeleteMixin{} }
:36 func (Group) Fields() []ent.Field { ... }
```
- Mixin 包：`backend/ent/schema/mixins`（`TimeMixin`、`SoftDeleteMixin`）。
- 字段修饰样例：`field.String("name").MaxLen(100).NotEmpty()`、`field.String("description").Optional().Nillable().SchemaType(map[string]string{dialect.Postgres: "text"})`、`field.Float(...).SchemaType(map[string]string{dialect.Postgres: "decimal(10,4)"})`（`group.go:40-49`）。
- **重要现状**：本功能相关 Ent schema 与生成产物**已在工作树中存在但未提交**（`git status` 显示 `backend/ent/schema/prompt_*.go`、`group_prompt_binding.go`、`account_group_prompt_override.go`、`prompt_admin_event.go`、`prompt_common.go` 及全部 `backend/ent/<entity>*.go` 生成物）。例如：
```go
// backend/ent/schema/prompt_template.go:19
type PromptTemplate struct{ ent.Schema }
:25  entsql.Annotation{Table: "prompt_templates"}
:29  func (PromptTemplate) Mixin() []ent.Mixin { return []ent.Mixin{ mixins.TimeMixin{} } }
```
  → M1 的 schema 骨架实际上已经落地（M0 报告需据此调整“待新增”表述）。
- **并发写入提示**：本报告成文期间（2026-10-07 19:48），工作树中另外出现了 `backend/internal/service/prompt_template.go`（`package service`，含模板/草稿/版本/绑定/覆盖/审计的领域定义，注释指向未来的 `prompt_policy.go` / `prompt_adapter.go`）。该文件不是本报告作者创建的，说明 M1 服务层已在并行推进。本节结论（schema 已存在）依然成立，但“待新增”清单应以上述实际文件为准。

### 代码生成
```make
# backend/Makefile:11-13
generate:
	go generate ./ent
	go generate ./cmd/server
```
测试目标：`test-unit: go test -tags=unit ./...`、`test-integration: go test -tags=integration ./...`（`Makefile:18-23`）。

### 仓储层模式
```go
// internal/repository/group_repo.go:22
type sqlExecutor interface { ExecContext(...); QueryContext(...) }
:27 type groupRepository struct { client *dbent.Client; sql sqlExecutor }
:65 func NewGroupRepository(client *dbent.Client, sqlDB *sql.DB) service.GroupRepository {
:66     return newGroupRepositoryWithSQL(client, sqlDB)
:71 func NewAdminGroupRepository(client *dbent.Client, sqlDB *sql.DB) service.AdminGroupRepository
```
- **接口定义位置**：在 `internal/service` 包，而非 repository 包 —— 例如 `internal/service/group_service.go:17 type GroupRepository interface`、`:41 GroupDuplicateRepository`、`:52 AdminGroupRepository`。构造器返回接口类型。
- **构造函数命名**：`NewXxxRepository(...) service.XxxRepository`。
- **错误包装约定**：`fmt.Errorf("...: %w", err)`；业务哨兵错误定义在 service 包并由 repo 返回，例如 `internal/repository/group_repo.go:60 return service.ErrGroupNotFound`（定义于 `internal/service/group_service.go:12`，用 `infraerrors.NotFound("GROUP_NOT_FOUND", ...)`）。
- 分页参数：`internal/pkg/pagination`（`group_service.go:25 List(ctx, params pagination.PaginationParams)`）。
- 新功能仓储（若尚未存在）建议：接口写入 `internal/service/prompt_*_service.go`，实现在 `internal/repository/prompt_*_repo.go`，并在 `internal/repository/wire.go:67 ProviderSet` 注册。**未确认 / Unverified**：本次未发现已存在的 prompt template repository 实现文件（`ls internal/repository | grep -i prompt` 为空）。

---

## 10. 迁移执行顺序与下一个安全文件名

```go
// internal/repository/migrations_runner.go:174-180
// 获取所有 .sql 迁移文件并按文件名排序。
// 命名规范：使用零填充数字前缀（如 001_init.sql, 002_add_users.sql）。
files, err := fs.Glob(fsys, "*.sql")
if err != nil {
	return fmt.Errorf("list migrations: %w", err)
}
sort.Strings(files) // 确保按文件名顺序执行迁移
```
- **排序规则：纯字典序（`sort.Strings`），按完整文件名**，不是数值排序。
- 校验和：`:196 sum := sha256.Sum256([]byte(content))`，已应用的迁移内容被修改会拒绝启动（`:105` 注释）。
- 非事务迁移后缀：`:53 const nonTransactionalMigrationSuffix = "_notx.sql"`。
- 迁移文件内嵌：`backend/migrations/migrations.go`（`//go:embed *.sql`）。

### 多个文件可共享同一数字前缀 —— 可以
因为排序是字典序，同前缀文件按后续字符排序，例如现存：
```
240_affiliate_ledger_operation_id.sql
241_add_payment_order_bonus_amount.sql
241_add_typesafe_platform.sql
241_student_verifications.sql
242_prompt_templates.sql
238_purge_unlimited_user_platform_quotas.sql
238b_content_moderation_engine_meta.sql
```
（另有 `238b_` 这种字母后缀写法，字典序上 `238b` > `238_` 但 < `239_`。）

### 下一个安全文件名
- **实测最高编号是 242，不是 241**：`backend/migrations/242_prompt_templates.sql` 已存在（未提交，`git status` 显示为 `??`），内容已包含 `prompt_templates` / `prompt_template_drafts` / `prompt_template_versions` / `group_prompt_bindings` / `account_group_prompt_overrides` / `prompt_admin_events` 全表与索引，并标注 forward-only + `IF NOT EXISTS` 幂等约定。
- 目录中**不存在** 243 及以上文件（`ls | grep -E "^24[3-9]|^25"` 无输出）。
- **推荐下一个文件名：`243_<snake_case_description>.sql`**，例如 `243_prompt_evaluation_tables.sql`。若需同批拆多文件，可用 `243_xxx.sql` + `243b_yyy.sql`（沿用既有 `238b_` 先例）。
- 若需要非事务 DDL（例如 `CREATE INDEX CONCURRENTLY`、触发器 `CREATE TRIGGER` 之外的长事务），必须命名为 `243_<desc>_notx.sql`（`migrations_runner.go:53`）。

---

## 11. 测试约定

### 构建标签
- 全仓库统计：`//go:build unit` × 494，`//go:build integration` × 94，`//go:build e2e` × 3，`//go:build live` × 1，`//go:build !unit` × 1。
- 运行方式：`backend/Makefile:18-23`
```make
test-unit:        go test -tags=unit ./...
test-integration: go test -tags=integration ./...
test-e2e-local:   go test -tags=e2e -v -timeout=300s ./internal/integration/...
```
- 服务层测试几乎全部使用 `//go:build unit`（例如 `internal/service/openai_gateway_chat_completions_test.go` 等）。

### 假上游 HTTP 服务 helper
- **核心 fake（推荐直接复用）**：
```go
// internal/service/openai_oauth_passthrough_test.go:30
type httpUpstreamRecorder struct {
	lastReq      *http.Request
	lastBody     []byte          // ← 捕获最终出站 body
	lastProxyURL string
	requests     []*http.Request
	bodies       [][]byte
	resp         *http.Response
	responses    []*http.Response
	err          error
}
```
  注入方式：直接构造 `&OpenAIGatewayService{cfg: ..., httpUpstream: upstream}`（见下），无需真实 `httptest.NewServer`。
- 另一种：`internal/service/openai_gateway_chat_completions_cancellation_test.go:61 type contextBoundHTTPUpstream struct`（用于取消/断连场景）。
- `httptest.NewServer` 也广泛使用，但主要在非 OpenAI 网关服务测试中（如 `content_moderation_*_test.go`、`openai_agent_identity_test.go`）。
- 相关只读 helper（本次仅确认存在，未逐一验证其语义）：`internal/handler/admin/admin_helpers_test.go`、`internal/handler/admin/admin_service_stub_test.go`。

### 断言“最终出站 body”的最佳模板
**首选模板（Responses `instructions` 注入，与本功能几乎同构）**：
```
internal/service/openai_compat_model_test.go:1560-1616
  TestForwardAsAnthropic_ForcedCodexInstructionsTemplate...
:1571  templateDir := t.TempDir()
:1573  os.WriteFile(templatePath, []byte("server-prefix\n\n{{ .ExistingInstructions }}"), 0o644)
:1587  upstream := &httpUpstreamRecorder{resp: &http.Response{...}}
:1593  svc := &OpenAIGatewayService{
:1594      cfg: &config.Config{Gateway: config.GatewayConfig{
:1595          ForcedCodexInstructionsTemplateFile: templatePath,
:1596          ForcedCodexInstructionsTemplate:     "server-prefix\n\n{{ .ExistingInstructions }}",
:1598      httpUpstream: upstream,
:1612  result, err := svc.ForwardAsAnthropic(context.Background(), c, account, body, "", "gpt-5.1")
:1615  require.Equal(t, "server-prefix\n\nclient-system", gjson.GetBytes(upstream.lastBody, "instructions").String())
```
第二个同构样例：同文件 `:1618-1663`（验证使用启动期缓存模板、不重新读盘）。

**Chat `messages` 注入模板**：
```
internal/service/openai_chat_roles_test.go:86
  require.Equal(t, "Keep these instructions.", gjson.GetBytes(upstream.lastBody, "messages.0.content").String())
```
```
internal/service/openai_gateway_chat_completions_raw_test.go:122
  require.True(t, gjson.GetBytes(upstream.lastBody, "stream_options.include_usage").Bool())
:275  require.Equal(t, "gpt-5.6-sol", gjson.GetBytes(upstream.lastBody, "model").String())
:430  require.Equal(t, "need tool", gjson.GetBytes(upstream.lastBody, "messages.1.reasoning_content").String())
```
```
internal/service/openai_gateway_apikey_item_id_test.go:56,112-115
  forwarded := upstream.lastBody
  require.False(t, gjson.GetBytes(upstream.lastBody, "input.0.id").Exists())
```

→ **建议新增测试直接照抄 `openai_compat_model_test.go:1560-1616` 的骨架**（`httpUpstreamRecorder` + `gjson.GetBytes(upstream.lastBody, ...)`），因为它的被测对象就是“向 Responses 顶层 `instructions` 注入正文”，与本功能验收口径（第 12 节“签名/注入顺序/不重复注入”）完全一致。

---

## 12. 未确认 / Unverified

1. **`applyMappedGPT55LiteCompatibility` 之外是否还有 `http.NewRequest` 之后改写 `req.Body` 的代码** —— 只抽查了 `buildUpstreamRequest`（`openai_gateway_forward.go:1547`）与 `buildUpstreamRequestOpenAIPassthrough`（`openai_gateway_passthrough.go:735`）两处调用点。未穷举全仓库 `req.Body =` 赋值。
2. **`internal/service/pricing_service.go:603`、`openai_codex_models_service.go:2062`、`openai_images.go:209` 的 `sha256` 作用对象** —— 未逐行确认是否作用于出站对话 body。
3. **`openai_gateway_forward.go:195` 签名补齐下沉后 `promptCacheKey` 的重新读取链路** —— 只确认 `:204` 紧随其后重读 `requestView`；下沉到 `:752` 之后需重做 `:549-590` 的 `clientPromptCacheKey` 逻辑，具体改法未验证。
4. **`gateway_handler.go:1012` 的 fallback 分组重试是否算“同一逻辑请求”** —— 从代码看它换 `apiKey` 后重入循环（`cloneAPIKeyWithGroup`，`:1030`），提示词策略身份是否继承需产品决策；本次只定位，不做判定。
5. **`internal/repository` 中是否已有 prompt template 仓储实现** —— `ls internal/repository | grep -i prompt` 无输出；但未用 `grep` 搜全部文件名（例如可能命名为 `prompt_template_repo.go` 之外的形式）。已确认 `internal/service/prompts/` 只含两个 `.txt` 资源（`codex_opencode_bridge.txt`、`tool_remap_message.txt`），与本功能无关。
6. **`selectAccountWithScheduler`（`openai_account_scheduler.go:2163`）内部是否已按“能力”过滤可注入账号** —— 只确认了能力参数 `requiredCapability OpenAIEndpointCapability` 的透传路径，未逐行核对过滤实现。
7. **前端（Vue）侧约定** —— 本次未审计 `frontend/`（任务范围仅后端路径核实）。
8. **`applyOpenCodeFreeTierGateBody` / `ensureDeepSeekChatReasoningPlaceholders` 是否会在极端输入下重建整个 messages 数组** —— 从 `openai_opencode_freetier.go:369-416` 看只做 `sjson.SetBytes` 局部写（`stream`/`tools`/`tool_choice`），`ensureDeepSeekChatReasoningPlaceholders` 未逐行读完（`openai_gateway_responses_chat_fallback.go:489` 起）。建议实施时把 Chat 注入放在 `cc_pipeline.go:189` 之前以彻底规避。
