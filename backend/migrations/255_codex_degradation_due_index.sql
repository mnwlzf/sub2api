-- 255_codex_degradation_due_index.sql
-- 自动降智检测的到期扫描索引。
--
-- 调度状态保存在 accounts.extra.codex_degradation_check.next_check_at（RFC3339
-- 文本）。运行器每分钟扫描一次"已到期或从未检查"的 OpenAI oauth/setup-token
-- 活跃账号；没有该索引时每轮都要全表扫描并解析 JSONB。部分索引把范围收窄到
-- 本功能候选账号，索引键就是到期时间文本，可直接支撑 ORDER BY。
--
-- 纯新增索引，回滚安全。

CREATE INDEX IF NOT EXISTS idx_accounts_codex_degradation_next_check
    ON accounts (((extra #>> '{codex_degradation_check,next_check_at}')))
    WHERE deleted_at IS NULL
        AND platform = 'openai'
        AND status = 'active'
        AND type IN ('oauth', 'setup-token');
