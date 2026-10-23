-- 252_codex_ticket_attempts_lifecycle.sql
-- Codex 打票流水的票据生命周期元数据。
--
-- 251 只记录「打了一发、结果如何」。本迁移补上票据代数（generation）、验真方式、
-- 票据有效期与响应头里是否真的带了 turn-state / Cookie，用于：
--   1. 管理端按票据代数对账（同一代票据只应有一条 success 流水）；
--   2. 观测路径发现上游换了票据时，定位被替换的是哪一代（见 253 的失效表）。
-- 仍然只存元数据，不存票据本体。

ALTER TABLE codex_ticket_attempts
    ADD COLUMN IF NOT EXISTS expires_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS ticket_generation_id UUID,
    ADD COLUMN IF NOT EXISTS verification_method TEXT,
    ADD COLUMN IF NOT EXISTS turn_state_present BOOLEAN,
    ADD COLUMN IF NOT EXISTS cookie_present BOOLEAN,
    ADD COLUMN IF NOT EXISTS challenge_expected_count INTEGER,
    ADD COLUMN IF NOT EXISTS parsed_number_count INTEGER;

-- 同一代票据只允许一条 success 流水：并发探测/重试可能重复写入同一代票据，
-- 唯一索引把幂等性下沉到数据库，管理端不会看到重复的「成功打票」。
CREATE UNIQUE INDEX IF NOT EXISTS codex_ticket_attempts_success_generation
    ON codex_ticket_attempts (ticket_generation_id)
    WHERE outcome = 'success' AND ticket_generation_id IS NOT NULL;
