-- 142_add_admin_scheduled_jobs.sql
-- Generic admin-only scheduled jobs center

CREATE TABLE IF NOT EXISTS admin_scheduled_jobs (
    id                BIGSERIAL PRIMARY KEY,
    name              VARCHAR(120) NOT NULL,
    job_type          VARCHAR(64) NOT NULL,
    cron_expression   VARCHAR(100) NOT NULL DEFAULT '0 * * * *',
    enabled           BOOLEAN NOT NULL DEFAULT true,
    payload_json      TEXT NOT NULL DEFAULT '{}',
    retention_limit   INT NOT NULL DEFAULT 100,
    last_run_at       TIMESTAMPTZ,
    next_run_at       TIMESTAMPTZ,
    last_status       VARCHAR(20) NOT NULL DEFAULT '',
    last_message      TEXT NOT NULL DEFAULT '',
    created_by        BIGINT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_admin_scheduled_jobs_enabled_next_run
    ON admin_scheduled_jobs(enabled, next_run_at)
    WHERE enabled = true;

CREATE TABLE IF NOT EXISTS admin_scheduled_job_runs (
    id                 BIGSERIAL PRIMARY KEY,
    job_id             BIGINT NOT NULL REFERENCES admin_scheduled_jobs(id) ON DELETE CASCADE,
    trigger_type       VARCHAR(20) NOT NULL DEFAULT 'manual',
    status             VARCHAR(20) NOT NULL DEFAULT 'running',
    message            TEXT NOT NULL DEFAULT '',
    result_json        TEXT NOT NULL DEFAULT '{}',
    started_at         TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    finished_at        TIMESTAMPTZ,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    triggered_by_user  BIGINT REFERENCES users(id) ON DELETE SET NULL
);

CREATE INDEX IF NOT EXISTS idx_admin_scheduled_job_runs_job_created
    ON admin_scheduled_job_runs(job_id, created_at DESC);
