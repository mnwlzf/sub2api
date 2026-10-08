# 文本提示词第一阶段：端到端验证记录

日期：2026-10-07
环境：本地 PostgreSQL 15.13 + Redis；独立库 `sub2api_prompt_e2e`，服务端口 8090。
结论：**15 项端到端检查全部通过**，其中包含此前唯一无法靠单元测试覆盖的风险点
（Ent 写入与不可变 trigger 的交互）。验证完成后服务已停止、临时库与二进制已删除。

## 1. 服务启动与迁移运行器

用 `DATABASE_DBNAME=sub2api_prompt_e2e` 启动 `cmd/server`，服务正常监听 `0.0.0.0:8090`。

应用启动时的迁移运行器（`applyMigrationsFS`）自动应用了本功能的迁移：

```
schema_migrations 中的记录：
  242_prompt_templates.sql
  243_prompt_request_events.sql
```

生成的表与 trigger 均已存在（`prompt_templates`、`prompt_template_drafts`、
`prompt_template_versions`、`group_prompt_bindings`、`account_group_prompt_overrides`、
`prompt_admin_events`、`prompt_request_events`、`prompt_template_versions_immutable`）。

这回答了此前「运行器路径与手动重放是否一致」的疑问：一致。

## 2. 真实服务路由

| 请求 | 结果 | 含义 |
|---|---|---|
| `GET /health` | 200 | 服务正常 |
| `GET /api/v1/admin/prompt-templates` | 401 | 路由已注册且受管理员鉴权保护 |
| `GET /api/v1/admin/prompt-request-events` | 401 | 同上 |

401 而不是 404，说明新端点在真实进程中确实注册成功。

## 3. 数据层端到端（`cmd/prompt-e2e-check`）

用真实仓储（`repository.NewPromptTemplateRepository`）对真实 PostgreSQL 执行完整配置流程：

| # | 检查项 | 结果 |
|---|---|---|
| 1 | 创建模板 | PASS |
| 2 | 更新草稿（乐观锁递增） | PASS |
| 3 | **发布版本（Ent 写入 vs 不可变 trigger）** | PASS |
| 4 | 正文 SHA-256 与字节数 | PASS |
| 5 | manifest 摘要非空 | PASS |
| 6 | 发布幂等重放返回同一版本 | PASS |
| 7 | 过期 `draft_revision` 被拒绝 | PASS |
| 8 | 分组绑定到固定版本 | PASS |
| 9 | 策略解析出启用状态 | PASS |
| 10 | 策略带出正文与 manifest | PASS |
| 11 | 模型范围外不注入（`skipped_model_scope`） | PASS |
| 12 | 运行事件可写入并查回 | PASS |
| 13 | 已发布版本不可修改（trigger 兜底） | PASS |
| 14 | 模板维度管理审计 | PASS |
| 15 | 分组维度管理审计（绑定事件） | PASS |

第 3 与第 13 项共同确认：**Ent 的插入不会被 trigger 误伤，而绕过服务层的直接修改会被数据库拒绝**。
这是启动前最担心的风险，现已排除。

工具可重复运行（幂等键与 `request_id` 每次唯一），适合后续 schema 变更后回归：

```bash
export PROMPT_E2E_DSN="host=127.0.0.1 port=5432 user=postgres password=postgres dbname=<临时库> sslmode=disable"
go run ./cmd/prompt-e2e-check
```

## 4. 验证过程中发现并修正的问题

1. **校验程序漏导入 `ent/runtime`** 导致 `Save` 时空指针 panic。
   这是校验程序自身的缺陷，不是生产代码问题 —— `cmd/server/main.go` 已导入该包。
   已在该程序内加注释说明原因，避免后人重犯。

2. **断言写错**：绑定事件的 `template_id` 为空（按 `group_id` 归属），
   按模板查询只应看到 3 条事件。已拆成「模板维度」与「分组维度」两个断言。

3. **校验程序不可重跑**：幂等键与 `request_id` 写死，第二次运行会命中上一轮记录并产生假失败。已改为每次唯一。

以上三项都是验证工具的问题，**没有发现生产代码缺陷**。

## 5. 仍未验证

- **带管理员凭据的真实 HTTP 调用**：新端点在真实服务中只验证到 401，未用管理员
  Token 走通「建模板 → 发布 → 绑定」的完整 HTTP 链路。
- **注入在真实网关请求中生效**：出站注入已由单元测试在最终字节层面覆盖，但未在
  真实服务里配合真实分组与账号跑一次完整请求。
- **前端**：未开始，因此后台尚无界面可操作。
