## 1. 强制 fast 不得降低更高档位

- [x] 1.1 新增 `openAIGroupForcedServiceTier(ctx, account, current) (string, bool)`：未开启 `ForceOpenAIFast` 时返回 `("", false)`
- [x] 1.2 当前档位归一化后为 `ultrafast` 时返回 `("", false)`（不覆写）
- [x] 1.3 其余情况（空 / default / auto / scale / flex / priority / 别名 fast / 大小写与空格变体）返回 `(priority, true)`
- [x] 1.4 `applyOpenAIFastPolicyToBody` 改用该函数（`/v1/responses`、`/chat/completions`、`/messages`、passthrough 入口）
- [x] 1.5 `applyOpenAIFastPolicyToWSResponseCreate` 改用该函数（Realtime WS 帧）
- [x] 1.6 `openai_gateway_forward.go` 转发主路径改用该函数，`markPatchSet` 只在真正改写时触发

## 2. 上游档位声明可选地不参与降档

- [x] 2.1 `Account.OpenAIResponseTierIsNonAuthoritative()`：读 `credentials.response_tier_non_authoritative`，同时接受 JSON bool 与字符串 `true`（`GetCredential` 对 bool 返回空，故直接读 map）
- [x] 2.2 `codexOAuthResponseTierIsNonAuthoritative` 更名 `openAIResponseBaseTierIsNonAuthoritative`，判定扩展为 `default` + `standard`
- [x] 2.3 `ResolveOpenAIServiceTierBilling`：`IsOpenAIOAuthLike() || OpenAIResponseTierIsNonAuthoritative()` 时保留请求档位
- [x] 2.4 更便宜的 `flex` 声明仍降档（不因开关掩盖真实降级）
- [x] 2.5 开关默认关闭时行为与改动前逐条一致

## 3. 管理端开关

- [x] 3.1 `EditAccountModal.vue` 新增 Toggle，仅在 `type=apikey && platform=openai` 显示
- [x] 3.2 载入时读 `credentials.response_tier_non_authoritative === true`
- [x] 3.3 保存时写入 `true`，关闭时 `delete` 该字段（不残留 false）
- [x] 3.4 中英 i18n 文案 `admin.accounts.upstreamBilling.responseTierNonAuthoritative{,Hint}`
- [ ] 3.5 后续：`CreateAccountModal.vue` 的 6 条 apikey 建号链路同样暴露该开关（本次先在创建后通过编辑弹窗开启，避免一次性改动 6 处建号分支）

## 4. 测试

- [x] 4.1 `TestOpenAIGroupForcedServiceTier`：不强制 / 提升 cheaper 档 / **不降级 ultrafast** / 非 openai 账号 / nil 账号与 ctx
- [x] 4.2 `TestApplyServiceTierBillingResolutionOnlyRewritesDowngrades` 新增：中转开关打开时 default 回声不降档（bool 与字符串两种存法）、仍接受 flex 降级、默认关闭保持旧契约、不会把无档请求提升
- [x] 4.3 `codex exception covers the whole base tier`：`default` / `standard` / 大小写空格为真，`flex` / `priority` / 空为假

## 5. 验证

- [x] 5.1 `go build ./internal/service/` 通过
- [x] 5.2 `go test ./internal/service/ -run 'TestOpenAIGroupForcedServiceTier|TestApplyServiceTierBillingResolutionOnlyRewritesDowngrades'` 通过
- [x] 5.3 `pnpm run typecheck` 通过
- [x] 5.4 `check:i18n` 通过
- [x] 5.5 部署后：对 group 66 真实请求，`usage_logs.service_tier` 由 `default` 变为 `priority`（21:07 实测 4 条），`billing.service_tier_downgraded` 不再出现
- [x] 5.6 部署后：客户端显式 `service_tier=ultrafast` 经 group 65 请求，`final_service_tier` 与 `usage_logs.service_tier` 均为 `ultrafast`
- [x] 5.7 单笔成本核对：同一请求 group 66 由 `default` 的 0.000716 变为 `priority` 的 0.001431，正是 2.0× 倍率
- [x] 5.8 注意：账号凭据改动需等进程内账号缓存刷新（实测约 6 分钟）才对新请求生效，改完即刻验证会看到旧行为
