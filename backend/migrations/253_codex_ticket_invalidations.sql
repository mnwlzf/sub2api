-- 253_codex_ticket_invalidations.sql
-- Codex 票据失效流水（invalidation）。
--
-- 背景：服务端注入打票所得的 x-codex-turn-state 后，若上游在响应里返回了**不同**
-- 的 turn-state 且同时换了 __oailb 路由 Cookie，说明这一代票据已被上游拒绝/轮换。
-- 观测路径（CodexTicketService.Observe）据此记一条失效流水，并原子地清掉账号
-- extra 中当前这一代票据，触发后台重新打票。
--
-- ticket_generation_id 唯一：同一代票据的失效事件只会落一条（观测可能被多条并发
-- 请求重复触发，去重必须由数据库保证，不能依赖进程内状态）。
-- 票据本体只在管理端诊断详情中可见（且已脱敏控制），列表接口只返回 *_present 布尔。

CREATE TABLE IF NOT EXISTS codex_ticket_invalidations (
    id BIGSERIAL PRIMARY KEY,
    account_id BIGINT NOT NULL,
    model TEXT NOT NULL,
    ticket_generation_id UUID NOT NULL UNIQUE,
    occurred_at TIMESTAMPTZ NOT NULL,
    recorded_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    -- upstream_turn_state_and_oailb_changed：注入的票据被上游换掉（turn-state 与
    -- __oailb 同时变化，真实客户端不会产生的矛盾信号）。
    reason_code TEXT NOT NULL CHECK (reason_code IN ('upstream_turn_state_and_oailb_changed')),
    response_http_status INTEGER,
    request_kind TEXT NOT NULL CHECK (request_kind IN ('user_request', 'diagnostic')),
    request_route TEXT NOT NULL,
    invalidated_current BOOLEAN NOT NULL DEFAULT FALSE,
    original_ticket TEXT,
    original_cookie TEXT,
    returned_ticket TEXT NOT NULL,
    returned_cookie TEXT,
    returned_set_cookies JSONB,
    CONSTRAINT codex_ticket_invalidations_cookies_array_check
        CHECK (returned_set_cookies IS NULL OR jsonb_typeof(returned_set_cookies) = 'array')
);

CREATE INDEX IF NOT EXISTS codex_ticket_invalidations_account_time
    ON codex_ticket_invalidations (account_id, occurred_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS codex_ticket_invalidations_account_model_time
    ON codex_ticket_invalidations (account_id, model, occurred_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS codex_ticket_invalidations_cleanup
    ON codex_ticket_invalidations (occurred_at, id);
