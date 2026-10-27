## Why

「强制 fast」只是布尔开关，只能把出站 `service_tier` 提升到 `priority`(Fast)。但上游实际服务档位可能是 `ultrafast`（第三方中转普遍恒回 ultrafast，且实测不区分请求档位、按固定价收费）。

结果是后台账目与现实脱节：

```
openai routing decision  account_id=31611  final_service_tier=priority   ← 恒为 priority
usage_logs.service_tier = priority → 前端渲染 "Fast"
上游响应 service_tier   = ultrafast
```

分组叫 UltraFast、客户买的是 UltraFast、上游给的是 ultrafast，只有我们自己的用量记录写 Fast。运维据此对账时无法区分两档，也无法解释“为什么显示 Fast”。

`ultrafast` 价格已由 `ultrafast-tier-price-floor` 保证「不低于同渠道 fast 倍率」，因此把某个分组真正切到 ultrafast **在财务上是零变化的**——只差一个能让分组声明目标档位的开关。

## What Changes

- `groups` 新增 `forced_service_tier varchar(16) NOT NULL DEFAULT ''`：空＝不强制（沿用 `force_openai_fast` 的 priority），`priority`＝强制 Fast，`ultrafast`＝强制 UltraFast。
- `openAIGroupForcedServiceTier` 改为读该字段；目标为 `ultrafast` 时无视客户端档位直接强制，目标为 `priority` 时保持既有「不降级客户端 ultrafast」的行为。
- 该字段随分组复制（duplicate）一起复制，平台不支持 OpenAI Fast 时随 `force_openai_fast` 一起清空。
- 认证快照（api_key auth cache）透传该字段，保证网关上下文里能读到。
- 管理端分组表单在「强制 Fast」下方新增「强制档位」下拉（不指定 / Fast / UltraFast），中英文案齐备。
- 纯新增列、默认空串，存量分组行为完全不变。

## Capabilities

### New Capabilities

- `group-forced-service-tier`：分组可以显式声明出站服务档位，使后台用量记录的 `service_tier` 与上游实际服务档位一致。

## Non-goals

- 不改变 `ultrafast` 的价格倍率取值（由 `ultrafast-tier-price-floor` 负责）。
- 不改变上游请求体的其他字段。
- 不为 `flex` 增加强制档位。
