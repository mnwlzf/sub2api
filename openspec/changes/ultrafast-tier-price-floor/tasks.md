## 1. 价格地板

- [x] 1.1 `configuredServiceTierMultiplier` 对 `ultrafast` 先取 `UltrafastMultiplier`，再与 `FastMultiplier` 取较大值
- [x] 1.2 两者都为 0 时仍回退 `serviceTierCostMultiplier`（2.0），未配 fast 的渠道行为不变
- [x] 1.3 `priority` / `fast` / `flex` 分支逻辑保持原样

## 2. 测试

- [x] 2.1 `ultrafast inherits fast multiplier`：fast=2.5、无专属值 → 2.5
- [x] 2.2 `ultrafast inherits a large fast multiplier`：fast=8、无专属值 → 8（覆盖线上渠道 23）
- [x] 2.3 `ultrafast keeps a higher dedicated multiplier`：专属 6、fast 3 → 6（astra 行为不回退）
- [x] 2.4 `ultrafast never drops below fast`：专属 6、fast 8 → 8
- [x] 2.5 `ultrafast default`：两者皆无 → 2（原有断言保留）
- [x] 2.6 `TestAstraUltrafastPricingUsesSixTimesStandard` 与 `gpt-6-sol` 的 2.0 断言均保持不变且通过

## 3. 验证

- [x] 3.1 `go build ./...` 通过
- [x] 3.2 `go test -tags=unit ./internal/service/ -run 'Tier|Multiplier|Billing'` 通过
- [x] 3.3 `golangci-lint run ./internal/service/...` 0 issues
- [ ] 3.4 部署后：客户请求体带 `service_tier: "ultrafast"` 经渠道 23，`usage_logs.total_cost` 与 fast 请求一致（不再降到 1/4）
- [ ] 3.5 部署后：不带 `service_tier` 的常规请求计费不变（回归）

## 4. 遗留（不在本次范围）

- [ ] 4.1 上游声明档位落库（`usage_logs.upstream_service_tier`）：需要给 8 处 INSERT 语句同时追加列与 `$N` 占位符，风险较高，独立变更处理
- [ ] 4.2 分组强制档位可配置（fast / ultrafast），使后台「服务档位」直接显示 Ultrafast
