-- 254_codex_degradation_probe_results.sql
-- Codex 降智检测（ModelTrace 指纹）的探测结果历史。
--
-- 每次账号原生探测（手动诊断或自动检测）落一行：请求模型、四态判定
-- （normal/degraded/uncertain/failed）、指纹分类预测与概率、解析数量、
-- 上游 HTTP 状态与来源（manual/auto）。纯新增表，回滚安全。
--
-- 账号删除时级联删除探测历史；自动检测的调度状态仍保存在
-- accounts.extra.codex_degradation_check，不在此表。

CREATE TABLE IF NOT EXISTS codex_degradation_probe_results (
    id              BIGSERIAL PRIMARY KEY,
    account_id      BIGINT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    model           VARCHAR(200) NOT NULL,
    status          VARCHAR(20) NOT NULL,
    reason          TEXT,
    predicted_model VARCHAR(200),
    probability     DOUBLE PRECISION NOT NULL DEFAULT 0,
    parsed_count    INT NOT NULL DEFAULT 0,
    http_status     INT NOT NULL DEFAULT 0,
    source          VARCHAR(20) NOT NULL DEFAULT 'manual',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_codex_degradation_probe_results_account_created
    ON codex_degradation_probe_results(account_id, created_at DESC, id DESC);
