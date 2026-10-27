## 1. 存储与模型

- [x] 1.1 `groups` 新增 `forced_service_tier varchar(16) NOT NULL DEFAULT ''`（迁移 256，纯加性）
- [x] 1.2 ent schema 增加字段并重新生成 ent 代码
- [x] 1.3 `service.Group` 增加 `ForcedServiceTier`
- [x] 1.4 `groupEntityToService` 映射该字段（所有读取路径共用）
- [x] 1.5 认证快照结构体透传该字段（L1/L2 两处映射）

## 2. 业务逻辑

- [x] 2.1 `normalizeForcedServiceTier`：`fast` 归一为 `priority`，未知值回落为空
- [x] 2.2 `openAIGroupForcedServiceTier` 优先读分组配置；为空时回落到 `force_openai_fast`
- [x] 2.3 目标 `ultrafast` 时无条件强制；目标 `priority` 时仍不降级客户端的 `ultrafast`
- [x] 2.4 平台不支持 OpenAI Fast 时，`sanitizeGroupOpenAIFast` 一并清空该字段
- [x] 2.5 分组复制（duplicate）复制该字段
- [x] 2.6 移除已无调用的 `openAIGroupForcesFast`

## 3. 接口与管理端

- [x] 3.1 创建/更新分组请求体增加 `forced_service_tier`
- [x] 3.2 管理端分组 DTO 暴露该字段
- [x] 3.3 `GroupsView` 在「强制 Fast」下方新增档位下拉（不指定 / Fast / UltraFast）
- [x] 3.4 表单默认值、编辑回填、创建/更新 payload 全部接入
- [x] 3.5 中英 i18n 文案

## 4. 测试

- [x] 4.1 `explicit ultrafast target wins over the caller tier`：6 种入参均输出 ultrafast
- [x] 4.2 `explicit priority target matches the legacy switch`：且不降级客户端 ultrafast
- [x] 4.3 `unknown forced tier falls back to no-op`
- [x] 4.4 既有 4 个用例保持通过

## 5. 验证

- [x] 5.1 `go build ./...` 通过
- [x] 5.2 `go test -tags=unit ./internal/service/ ./internal/handler/ ./internal/repository/ -run 'Group|Fast|Tier'` 通过
- [x] 5.3 `golangci-lint run` 0 issues
- [x] 5.4 `pnpm run typecheck` 通过
- [x] 5.5 `check:i18n` 通过
- [ ] 5.6 部署后：把 UltraFast 分组设为 `ultrafast`，请求后 `usage_logs.service_tier` = `ultrafast`，且 `total_cost` 与 fast 请求一致
