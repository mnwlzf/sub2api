## Why

生产里两个 OpenAI 分组都开启了 `force_openai_fast`，但计费档位与预期不符，且两个问题根因不同：

**① 普通Fast分组（group 66）日志显示 Standard**

线上审计日志逐条确认：

```
openai routing decision  account_id=31612  final_service_tier=priority   ← 强制 fast 已生效
billing.service_tier_downgraded  account_id=31612
  requested_tier=priority → response_tier=default → billed_tier=default
```

直接对该账号的上游（第三方中转 `starbridgex.cc`）复现：显式发送 `service_tier: "priority"`，上游仍回 `service_tier: "default"`；同样请求打到会声明档位的上游（`wanjckn.xyz`，group 65 绑定的账号）则回 `ultrafast`。

即：**中转不会如实声明档位**，而 `ResolveBillingServiceTier` 的契约是「上游声明只许降不许升」，于是每笔用量都按 `default`（1.0×）结算，而不是 `priority`（2.0×）。现有豁免只覆盖 OpenAI OAuth / SetupToken（Codex 私有端点的同类行为），apikey 类型的中转账号不在其中，且没有任何运营侧开关可以声明「这个上游的档位声明不可信」。

**② UltraFast分组（group 65）日志显示 Fast**

`openAIGroupForcesFast` 在三个出站收口点**无条件**把 `service_tier` 覆写为 `priority`：

- `applyOpenAIFastPolicyToBody`（`openai_gateway_request_body.go`）
- `applyOpenAIFastPolicyToWSResponseCreate`（同文件）
- `OpenAIGatewayService` 转发主路径（`openai_gateway_forward.go`）

线上 group 65 的每一条路由决策都是 `final_service_tier=priority`，从未出现过 `ultrafast`：即使客户端显式请求 `ultrafast`，也会被降写成 `priority`，随后计费记 `priority`，而前端把 `priority` 渲染成 **Fast**。分组名叫 UltraFast 却永远卖 Fast，且这种降级完全静默。

## What Changes

- 新增 `openAIGroupForcedServiceTier(ctx, account, current) (string, bool)`：「强制 fast」语义改为**至少 fast**。当前档位已是 `ultrafast`（成本高于 priority）时不覆写，其余情况（缺省 / flex / default / auto / scale / priority / 别名 fast）仍提升为 `priority`。三处调用点全部改用它，行为一致。
- 新增账号级凭据开关 `credentials.response_tier_non_authoritative`：声明该账号上游响应的档位不可信。开启后 `ResolveOpenAIServiceTierBilling` 不再允许上游声明的**基础档位**（`default` / `standard`）降低计费档位，改为按请求实际发送的档位结算；更便宜的 `flex` 声明仍然生效，以免掩盖真实降级。
- `codexOAuthResponseTierIsNonAuthoritative` 更名为 `openAIResponseBaseTierIsNonAuthoritative`，并把判定从仅 `default` 扩展到 `default` + `standard`（OpenAI 侧同时出现两种写法）。Codex OAuth/SetupToken 的既有豁免行为不变。
- 管理端「编辑账号」弹窗为 `platform=openai` 且 `type=apikey` 的账号新增开关「上游响应档位不可信」，默认关闭＝现有行为不变。
- 新增中英 i18n 文案。

## Capabilities

### New Capabilities

- `openai-service-tier-billing-honesty`：OpenAI 网关的计费档位在两种边界下保持与真实服务一致——分组「强制 fast」不得把更高的 ultrafast 静默降级；无法如实声明档位的上游（OAuth/SetupToken 与显式标注的中转账号）不得用 `default` 回声把账单压低。

## Non-goals

- 不改变 `priority` / `ultrafast` / `flex` / `default` 之间的成本倍率数值。
- 不探测、不推断上游真实服务档位；开关只是声明「声明不可信」，不做自动识别。
- 不改 `openai_fast_policy` 全局策略（pass / filter / block / force_priority）的既有语义。
- 不为「强制 fast」新增 ultrafast 强制档位选项；分组仍只能强制到 priority。
