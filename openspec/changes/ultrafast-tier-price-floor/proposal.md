## Why

`ultrafast` 档位在系统里没有价格：`UltrafastMultiplier` 全库只对 `gpt-6-astra` 族硬编码为 6，其余模型一律落到 `serviceTierCostMultiplier` 的兜底 **2.0**；渠道定价界面也只有 Fast / Flex 两个倍率输入框（`PricingEntryCard.vue`），运营方没有任何手段给 ultrafast 定价。

这条兜底路径此前从未被触发，因为「强制 fast」会把所有请求的 `service_tier` 无条件改写成 `priority`。PR #45 把强制语义改成「至少 fast」之后，客户端显式请求的 `ultrafast` 会被保留，于是兜底 2.0 开始生效：

```
渠道 23（UltraFast 分组）fast_multiplier = 8
客户发 service_tier: "ultrafast"
  → 计费倍率 8 → 2，售价降到 1/4
  → 上游按固定价收费（实测不看该字段）
```

即：**修复“ultrafast 被静默降级”的同时，开出了一个 75% 的折扣口子。**

## What Changes

- `configuredServiceTierMultiplier` 对 `ultrafast` 增加价格地板：取「模型专属 `UltrafastMultiplier`」与「渠道 `FastMultiplier`」的**较大值**，两者都为 0 时才回退到 2.0。
- 语义：`ultrafast` 是 `fast` 的更高档，**任何情况下不得比 `fast` 便宜**。
- 未配置 fast 的渠道行为完全不变（仍回退 2.0）。
- 不新增数据库列、不改管理界面、不动任何现有渠道定价数字。

## Capabilities

### New Capabilities

- `ultrafast-tier-price-floor`：`ultrafast` 档位的计费倍率永远不低于同渠道的 fast 倍率，避免该档位因缺少独立定价而按 2.0 兜底被低价卖出。

## Non-goals

- 不为 `ultrafast` 引入独立定价字段（`channel_model_pricing.ultrafast_multiplier`）；本变更用继承规则替代。
- 不改变 `flex` / `priority` 的既有取值逻辑。
- 不改变上游请求体里的 `service_tier`（仍由分组强制策略决定）。
