-- 251_codex_ticket_attempts.sql
-- Codex 打票流水（attempt）基础表。
--
-- 背景：官方 Codex 的 x-codex-turn-state 票据由上游在响应头中铸造，本表只记录
-- 「谁在什么时候用哪个代理、针对哪个 (账号, 模型) 打了一发、结果如何」的元数据。
-- 票据本体（state blob）、OAuth 凭证、代理 URL 一律不落这张表：流水可导出、
-- 可长期保留，而票据是账号级机密（见 service/codex_ticket_service.go 的存储约定）。
--
-- 纯加性变更；表名/列名与参考实现（sub4api）对齐，便于后续迁移与对账。

CREATE TABLE IF NOT EXISTS codex_ticket_attempts (
    id BIGSERIAL PRIMARY KEY,
    account_id BIGINT NOT NULL,
    model TEXT NOT NULL,
    occurred_at TIMESTAMPTZ NOT NULL,
    -- success：拿到符合账号长度规则且带 gAAAAA 前缀的票据并落库；
    -- miss：上游响应但没有可用票据（状态码不符、票据形状不符、流内无输出等）；
    -- error：网络/代理/凭据/模板等链路错误，未得到可判定的上游响应。
    outcome TEXT NOT NULL CHECK (outcome IN ('success', 'miss', 'error')),
    -- 触发来源：automatic=后台扫描，manual=管理端手动打票。
    -- 列名用 source 而不是 trigger：trigger 在部分 PostgreSQL 版本里是保留字。
    source TEXT NOT NULL CHECK (source IN ('automatic', 'manual')),
    http_status INTEGER,
    ticket_length INTEGER,
    duration_ms INTEGER NOT NULL DEFAULT 0,
    reason_code TEXT,
    proxy_id BIGINT,
    proxy_name TEXT
);

-- 账号 + 模型维度的时间倒序翻页；id 参与排序保证同一毫秒内的稳定顺序。
CREATE INDEX IF NOT EXISTS codex_ticket_attempts_account_model_time
    ON codex_ticket_attempts (account_id, model, occurred_at DESC, id DESC);
-- 账号维度的最近流水（账号列表/详情页）。
CREATE INDEX IF NOT EXISTS codex_ticket_attempts_account_time
    ON codex_ticket_attempts (account_id, occurred_at DESC, id DESC);
-- 定期清理（保留窗口由 repository.Cleanup 决定，默认 90 天）。
CREATE INDEX IF NOT EXISTS codex_ticket_attempts_cleanup
    ON codex_ticket_attempts (occurred_at, id);
