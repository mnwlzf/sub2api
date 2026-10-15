-- 246_pelican_probe.sql
-- 鹈鹕智商探针（分组级模型质量/降智监控，MVP：人工评审）。
-- 设计说明：
--   - 按 group_id + model_id 定时探测，走用户面 /v1/chat/completions 自调用，
--     测的是分组真实调度链路（路由/账号池/限流），而非单账号连通性。
--   - 结果存模型原始输出 raw_html，前端沙箱 iframe 渲染，人眼评审通过/降智。

CREATE TABLE IF NOT EXISTS pelican_probe_plans (
    id              BIGSERIAL PRIMARY KEY,
    name            VARCHAR(100) NOT NULL DEFAULT '',
    group_id        BIGINT NOT NULL REFERENCES groups(id) ON DELETE CASCADE,
    model_id        VARCHAR(200) NOT NULL,
    base_url        VARCHAR(500) NOT NULL DEFAULT '',
    api_key_encrypted TEXT NOT NULL DEFAULT '',
    prompt          TEXT NOT NULL DEFAULT '',
    cron_expression VARCHAR(100) NOT NULL DEFAULT '0 */4 * * *',
    enabled         BOOLEAN NOT NULL DEFAULT true,
    max_results     INT NOT NULL DEFAULT 50,
    last_run_at     TIMESTAMPTZ,
    next_run_at     TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_ppp_group_id ON pelican_probe_plans(group_id);
CREATE INDEX IF NOT EXISTS idx_ppp_enabled_next_run ON pelican_probe_plans(enabled, next_run_at) WHERE enabled = true;

CREATE TABLE IF NOT EXISTS pelican_probe_results (
    id              BIGSERIAL PRIMARY KEY,
    plan_id         BIGINT NOT NULL REFERENCES pelican_probe_plans(id) ON DELETE CASCADE,
    status          VARCHAR(20) NOT NULL DEFAULT 'success',
    raw_html        TEXT NOT NULL DEFAULT '',
    verdict         VARCHAR(20) NOT NULL DEFAULT 'pending',
    note            TEXT NOT NULL DEFAULT '',
    reviewed_by     VARCHAR(255) NOT NULL DEFAULT '',
    reviewed_at     TIMESTAMPTZ,
    error_message   TEXT NOT NULL DEFAULT '',
    latency_ms      BIGINT NOT NULL DEFAULT 0,
    prompt_tokens   INT NOT NULL DEFAULT 0,
    completion_tokens INT NOT NULL DEFAULT 0,
    started_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    finished_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_ppr_plan_created ON pelican_probe_results(plan_id, created_at DESC);
