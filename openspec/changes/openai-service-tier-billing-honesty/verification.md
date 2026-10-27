# Verification

## 现场证据（改动依据）

线上 `sub2api` 容器日志与 `usage_logs`，2026-10-27：

| 分组 | 绑定账号 | 上游 | 路由决策 | 上游声明 | 计费档位 | 前端显示 |
| --- | --- | --- | --- | --- | --- | --- |
| 65 UltraFast分组 | 31611 | `wanjckn.xyz` | `priority` | `ultrafast` | `priority` | Fast |
| 66 普通Fast分组 | 31612 | `starbridgex.cc` | `priority` | `default` | `default` | Standard |

两个分组配置完全相同（`force_openai_fast=true`、`rate_multiplier=0.25`、同一份 `model_allowlist`），差异只在绑定的上游账号。

直连上游复现（对两个账号各自发送 `service_tier: "priority"`，`/v1/responses`，`max_output_tokens=16`）：

```
31612 starbridgex.cc  http=200  service_tier=default    status=completed
31611 wanjckn.xyz     http=200  service_tier=ultrafast  status=completed
```

结论：31612 的上游不因请求档位而改变声明，恒回 `default`；而 group 65 的每一条 `final_service_tier` 都是 `priority`，说明「强制 fast」把客户端可能发出的 `ultrafast` 覆写掉了。

## 改动前期望失败 / 改动后通过

单测覆盖两条边界：

- `TestOpenAIGroupForcedServiceTier`（新增）
  - `force never downgrades an explicit ultrafast request`：未修复前该子用例失败（返回 `priority, true`）。
  - `force raises missing and cheaper tiers to priority`：保证修复没有削弱强制语义。
- `TestApplyServiceTierBillingResolutionOnlyRewritesDowngrades`
  - `relay flagged non-authoritative keeps outbound priority despite default echo`：未修复前失败（降档到 `default`）。
  - `relay flag still accepts an explicit flex downgrade`：防止开关把真实降级也一并吞掉。
  - `relay flag default-off keeps the generic contract`：默认路径行为不变。
  - `relay flag never promotes an untiered request`：无档请求不因上游回声而被提升。

## 自动化命令

```bash
cd backend
go build ./internal/service/
go test -tags=unit ./internal/service/ -run 'TestOpenAIGroupForcedServiceTier|TestApplyServiceTierBillingResolutionOnlyRewritesDowngrades' -count=1
go test -tags=unit ./internal/service/ ./internal/handler/ -run 'Fast|ServiceTier|TierBilling' -count=1

cd ../frontend
pnpm run typecheck
npx vitest run src/i18n/__tests__/localeKeyCompleteness.spec.ts
```

## 上线后需要复验的线上事实

1. **group 66**：真实请求后 `usage_logs.service_tier` 由 `default` 变为 `priority`；`billing.service_tier_downgraded` 不再为 31612 产生记录。
2. **group 65**：客户端显式 `service_tier=ultrafast` 时，`openai routing decision` 的 `final_service_tier` 为 `ultrafast`（当前恒为 `priority`）。
3. **倍率回归**：两组的 `rate_multiplier=0.25` 不应受档位改动影响；`default`→`priority` 会让成本倍率由 1.0 变为 2.0，属于预期内的账目变化。

## 风险与回滚

- 存量账号默认不带 `response_tier_non_authoritative`，计费行为与改动前一致；只有显式开启的账号才按请求档位结算。
- ultrafast 修复会让 group 65 的真实出站档位变为 `ultrafast`，成本倍率 2.0（与 `priority` 相同），账目金额不变，但 `usage_logs.service_tier` 的取值会变化——依赖该字段做统计的看板需要知悉。
- 回滚方式：回退代码即可；凭据字段留在账号上不影响旧版行为（旧版完全忽略它）。
