-- 请求级提示词策略记录：一次“逻辑请求 × 上游尝试”一行。
--
-- 目的：让后台能回答“这个请求注入了没有、为什么没注入、重试时是否换了版本”。
-- 只记录策略身份与结果，不记录提示词正文、不记录客户请求体。
--
-- 写入为尽力而为：记录失败不得影响已经成功的上游请求，也不得触发重试。

CREATE TABLE IF NOT EXISTS prompt_request_events (
    id BIGSERIAL PRIMARY KEY,
    request_id VARCHAR(64) NULL,
    attempt_no INTEGER NOT NULL DEFAULT 1,
    group_id BIGINT NULL,
    account_id BIGINT NULL,
    client_model VARCHAR(120) NULL,
    upstream_model VARCHAR(120) NULL,
    outbound_profile VARCHAR(40) NULL,
    binding_source VARCHAR(20) NULL,
    version_id BIGINT NULL,
    manifest_sha256 VARCHAR(64) NULL,
    applied BOOLEAN NOT NULL DEFAULT FALSE,
    reason VARCHAR(40) NOT NULL,
    added_bytes INTEGER NOT NULL DEFAULT 0,
    apply_duration_ms INTEGER NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT prompt_request_events_attempt_positive CHECK (attempt_no >= 1),
    CONSTRAINT prompt_request_events_added_bytes_non_negative CHECK (added_bytes >= 0)
);

CREATE INDEX IF NOT EXISTS prompt_request_events_created_at_idx
    ON prompt_request_events (created_at DESC);

CREATE INDEX IF NOT EXISTS prompt_request_events_request_id_idx
    ON prompt_request_events (request_id);

CREATE INDEX IF NOT EXISTS prompt_request_events_group_id_idx
    ON prompt_request_events (group_id);

CREATE INDEX IF NOT EXISTS prompt_request_events_version_id_idx
    ON prompt_request_events (version_id);

CREATE INDEX IF NOT EXISTS prompt_request_events_applied_idx
    ON prompt_request_events (applied);
