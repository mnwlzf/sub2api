# 验收计划：OpenAI BPS 平台

> 本文件定义「什么算做完」。实现过程中每完成一项在此记录命令、结果与证据路径。

## 1. 自动化验证

### 1.1 后端

```bash
cd backend
go build ./...
go vet ./internal/service/ ./internal/handler/... ./internal/repository/ ./cmd/server/
go test -p 2 ./internal/service/ -run 'BPS' -count=1
go test -p 2 ./internal/handler/... -run 'BPS' -count=1
go test -p 2 ./internal/repository/ -run 'BPS' -count=1
go test -p 2 ./migrations/ -run 'BPS' -count=1
go test -p 2 ./internal/service/ -count=1        # 既有平台全量回归
```

### 1.2 前端

```bash
cd frontend
node node_modules/vue-tsc/bin/vue-tsc.js --noEmit
node node_modules/vitest/vitest.mjs run src/components/account/__tests__/OpenAIBPSAccountFields.spec.ts \
  src/components/account/__tests__/CreateAccountModal.spec.ts \
  src/components/account/__tests__/EditAccountModal.spec.ts
```

## 2. 必测矩阵（自动化）

| 能力 | 用例 |
|---|---|
| 平台/账号 | 平台常量、仅 oauth、AT 必填、账号 ID 覆盖、JWT 到期提取、脱敏、状态保留、替换 token 不解除停用 |
| 网关 | JSON、SSE、错误透传、客户端取消、超时、`store=false`、客户端认证头不转发、固定端点不可覆盖 |
| 会话 | session_id/prompt_cache_key 派生、首条消息派生、API Key 隔离、Redis 绑定原子性、换号边界（未绑定/已绑定）、`bps_account_unavailable`、`bps_context_expired` |
| 工具 | 目录生成、重复合并、冲突报错、function/custom 信封、`tool/args` 兼容、未知工具拒绝、schema 校验、外部 `$ref`、1 MiB 上限、单工具交付、连续往返、加密 reasoning 重建、`update_plan` 回执 |
| 压缩 | 真实摘要、引用版本、引用不外发、轮次保持、迭代递增、连续压缩、旧摘要兼容、跨实例恢复、失败不替换历史 |
| 结构化 | json_object、json_schema、strict=false 仍校验、无效 schema 400、外部引用拒绝、大整数、16 MiB 上限、SSE `response.failed`、usage 保留 |
| 计费/错误 | usage 记录（含失败）、401 停调度、过期排除、模型权限冷却 30 分钟、403 不整号、400 不重试、429 Retry-After、迟到响应不覆盖新凭证 |
| 目录 | BPS 目录、别名、白名单、Composite 精确/前缀路由、空候选、普通 `gpt-*` 仍走 OpenAI |
| 迁移 | 平台枚举扩展幂等、回滚为空操作、既有平台数据不受影响 |

## 3. 真实账号验收（需 BPS token）

- [ ] 文本：JSON 与 SSE 各一轮，usage 字段与计费一致
- [ ] 工具：function（含 `additional_tools`）、custom、`update_plan` 各一轮；多轮工具结果续接
- [ ] 压缩：`/responses/compact` → 新一轮对话 → 工具调用，轮次与迭代符合预期
- [ ] 结构化：json_object 与 json_schema（含嵌套对象/数组/大整数）各一轮
- [ ] 错误：无效 token（401）、过期 token、模型权限错误、429（如可构造）
- [ ] 多实例：两个后端实例共享 Redis，压缩/工具状态跨实例恢复
- [ ] 客户端：Codex CLI `wire_api="responses"`、`supports_websockets=false` 正常对话与工具
- [ ] 计费：用量日志、分组倍率、用户平台配额扣减核对

## 4. 上线检查

- [ ] 迁移已在目标环境执行（真实 PostgreSQL，非仅 sqlmock）
- [ ] Redis 可用性与容量评估（状态 7 天 TTL 的峰值估算）
- [ ] 平台默认关闭 → 灰度开关流程与回滚演练完成
- [ ] 封禁风险提示已在前端与文档中可见
- [ ] 监控：BPS 请求量/失败率/工具成功率/压缩成功率/Redis 命中率看板
