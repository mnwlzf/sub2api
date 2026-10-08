# 迁移 242 数据库层验证记录

日期：2026-10-07
环境：本地 PostgreSQL 15.13（`D:\Develop\DevelopEnv\PostgreSQL\15`），独立临时库 `sub2api_prompt_verify`
结论：迁移 242 的**全部结构性约束在真实 PostgreSQL 上按设计生效**。验证完成后临时库已删除。

## 方法

在全新空库中按字典序重放 `backend/migrations/*.sql`，随后用直接 SQL 触发每条约束，
观察是被拒绝还是被接受。选择临时库而不是既有 `sub2api` / `sub2api_test`，避免影响现有数据。

## 迁移重放结果

- 成功：**292 / 293**
- 失败：`123_fix_legacy_auth_source_grant_on_signup_defaults.sql`
  - 原因：该迁移读取 `schema_migrations` 表，该表由应用的迁移运行器创建，不在 SQL 文件里。
  - 判定：脱离运行器直接重放时的预期现象，与本功能无关。
- 迁移 242 应用成功。

## 验证项与结果

| # | 验证内容 | 预期 | 实测 |
|---|---|---|---|
| 1 | 六张表创建 | 存在 | 全部存在 |
| 2 | 不可变 trigger | 存在 | `prompt_template_versions_immutable` |
| 3 | 已发布版本 UPDATE | 拒绝 | 被拒绝：`is append-only` |
| 4 | 已发布版本 DELETE | 拒绝 | 被拒绝：`is append-only` |
| 5 | `mode=version` 但 `version_id` 为空 | 拒绝 | 违反 `group_prompt_bindings_mode_version_pair` |
| 6 | `mode=disabled` 但带 `version_id` | 拒绝 | 违反同一 CHECK |
| 7 | 合法的 `mode=version` 绑定 | 接受 | 接受 |
| 8 | 活动模板重名 | 拒绝 | 违反 `prompt_templates_active_name_key` |
| 9 | 归档后复用同名 | 接受 | 接受 |
| 10 | 非分组成员的账号覆盖 | 拒绝 | 违反 `account_group_prompt_overrides_membership_fk` |
| 11 | 真实分组成员的覆盖 | 接受 | 接受 |
| 12 | 移除分组成员关系 | 级联删除覆盖 | 覆盖计数 1 → 0 |

第 10–12 项确认了设计中的关键取舍：账号覆盖通过复合外键绑定到 `account_groups`，
因此**覆盖只能存在于真实的分组成员关系上，移除成员时会自动清理**，不会残留孤儿配置。

## 尚未验证

- 应用启动时的迁移运行器（`applyMigrationsFS`）是否与手动重放行为一致，未实测。
- 迁移的校验和记录、以及重复执行的幂等性，未在运行器路径下实测。
- 服务层与数据库的真实交互（Ent 写入与 trigger 的交互）未实测 —— 端到端请求验证尚未进行。
