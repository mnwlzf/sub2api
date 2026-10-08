-- 文本提示词管理：中央模板 + 不可变版本 + 分组绑定 + 分组内账号覆盖 + 管理审计。
--
-- 设计要点（详见 docs/plans/2026-10-07-text-prompt-management-design.md）：
--   1. 正文分两处保存：可编辑草稿在 prompt_template_drafts，发布后的不可变
--      快照在 prompt_template_versions。绑定永远指向一个版本 ID，因此
--      “修改草稿”不会影响任何已经在运行的请求。
--   2. 缺少绑定行等价于 disabled；复制出来的分组不会意外启用候选。
--   3. 账号覆盖的作用域是 (account_id, group_id) 关系，不是账号全局字段。
--   4. 已发布版本内容不可变，由 trigger 强制。
--
-- 迁移遵循 forward-only 与幂等约定：全部使用 IF NOT EXISTS / IF EXISTS。

CREATE TABLE IF NOT EXISTS prompt_templates (
    id BIGSERIAL PRIMARY KEY,
    name VARCHAR(100) NOT NULL,
    description TEXT NULL,
    source_url VARCHAR(500) NULL,
    source_note TEXT NULL,
    archived_at TIMESTAMPTZ NULL,
    revision INTEGER NOT NULL DEFAULT 1,
    created_by BIGINT NULL,
    updated_by BIGINT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- 活动模板内名称唯一；归档后名称可被新模板复用。
CREATE UNIQUE INDEX IF NOT EXISTS prompt_templates_active_name_key
    ON prompt_templates (name)
    WHERE archived_at IS NULL;

CREATE INDEX IF NOT EXISTS prompt_templates_archived_at_idx
    ON prompt_templates (archived_at);

CREATE TABLE IF NOT EXISTS prompt_template_drafts (
    id BIGSERIAL PRIMARY KEY,
    template_id BIGINT NOT NULL REFERENCES prompt_templates(id) ON DELETE CASCADE,
    body TEXT NOT NULL,
    client_models JSONB NOT NULL DEFAULT '[]'::jsonb,
    upstream_models JSONB NOT NULL DEFAULT '[]'::jsonb,
    supported_profiles JSONB NOT NULL DEFAULT '[]'::jsonb,
    revision INTEGER NOT NULL DEFAULT 1,
    updated_by BIGINT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT prompt_template_drafts_body_not_blank CHECK (length(body) > 0),
    CONSTRAINT prompt_template_drafts_body_size CHECK (octet_length(body) <= 32768)
);

CREATE UNIQUE INDEX IF NOT EXISTS prompt_template_drafts_template_id_key
    ON prompt_template_drafts (template_id);

CREATE TABLE IF NOT EXISTS prompt_template_versions (
    id BIGSERIAL PRIMARY KEY,
    template_id BIGINT NOT NULL REFERENCES prompt_templates(id) ON DELETE RESTRICT,
    version_no INTEGER NOT NULL,
    body TEXT NOT NULL,
    body_sha256 VARCHAR(64) NOT NULL,
    body_bytes INTEGER NOT NULL,
    client_models JSONB NOT NULL DEFAULT '[]'::jsonb,
    upstream_models JSONB NOT NULL DEFAULT '[]'::jsonb,
    supported_profiles JSONB NOT NULL DEFAULT '[]'::jsonb,
    manifest_sha256 VARCHAR(64) NOT NULL,
    change_note TEXT NULL,
    idempotency_key VARCHAR(64) NULL,
    published_by BIGINT NULL,
    published_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT prompt_template_versions_body_not_blank CHECK (length(body) > 0),
    CONSTRAINT prompt_template_versions_body_size CHECK (octet_length(body) <= 32768),
    CONSTRAINT prompt_template_versions_body_bytes_match CHECK (body_bytes = octet_length(body)),
    CONSTRAINT prompt_template_versions_version_no_positive CHECK (version_no >= 1)
);

CREATE UNIQUE INDEX IF NOT EXISTS prompt_template_versions_template_version_key
    ON prompt_template_versions (template_id, version_no);

CREATE INDEX IF NOT EXISTS prompt_template_versions_template_id_idx
    ON prompt_template_versions (template_id);

CREATE INDEX IF NOT EXISTS prompt_template_versions_body_sha256_idx
    ON prompt_template_versions (body_sha256);

-- 发布幂等：同一个 idempotency_key 只会产生一个版本（重放发布请求时返回已存在版本）。
CREATE UNIQUE INDEX IF NOT EXISTS prompt_template_versions_idempotency_key
    ON prompt_template_versions (idempotency_key)
    WHERE idempotency_key IS NOT NULL;

-- 已发布版本内容不可变：任何 UPDATE / DELETE 都被拒绝。
-- 需要清理测试数据时使用 TRUNCATE（不触发行级 trigger）或 DROP TABLE。
CREATE OR REPLACE FUNCTION prompt_template_versions_reject_mutation()
RETURNS trigger AS $$
BEGIN
    RAISE EXCEPTION 'prompt_template_versions is append-only; publish a new version instead of modifying version %', COALESCE(OLD.id, 0)
        USING ERRCODE = 'restrict_violation';
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS prompt_template_versions_immutable ON prompt_template_versions;
CREATE TRIGGER prompt_template_versions_immutable
    BEFORE UPDATE OR DELETE ON prompt_template_versions
    FOR EACH ROW EXECUTE FUNCTION prompt_template_versions_reject_mutation();

CREATE TABLE IF NOT EXISTS group_prompt_bindings (
    id BIGSERIAL PRIMARY KEY,
    group_id BIGINT NOT NULL REFERENCES groups(id) ON DELETE CASCADE,
    mode VARCHAR(20) NOT NULL DEFAULT 'disabled',
    version_id BIGINT NULL REFERENCES prompt_template_versions(id) ON DELETE RESTRICT,
    revision INTEGER NOT NULL DEFAULT 1,
    updated_by BIGINT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT group_prompt_bindings_mode_valid CHECK (mode IN ('disabled', 'version')),
    -- mode 与 version_id 必须配套，避免出现“启用了但没有版本”的静默空转状态。
    CONSTRAINT group_prompt_bindings_mode_version_pair CHECK (
        (mode = 'version' AND version_id IS NOT NULL)
        OR (mode = 'disabled' AND version_id IS NULL)
    )
);

CREATE UNIQUE INDEX IF NOT EXISTS group_prompt_bindings_group_id_key
    ON group_prompt_bindings (group_id);

CREATE INDEX IF NOT EXISTS group_prompt_bindings_version_id_idx
    ON group_prompt_bindings (version_id);

CREATE TABLE IF NOT EXISTS account_group_prompt_overrides (
    id BIGSERIAL PRIMARY KEY,
    account_id BIGINT NOT NULL,
    group_id BIGINT NOT NULL,
    mode VARCHAR(20) NOT NULL DEFAULT 'inherit',
    version_id BIGINT NULL REFERENCES prompt_template_versions(id) ON DELETE RESTRICT,
    revision INTEGER NOT NULL DEFAULT 1,
    updated_by BIGINT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT account_group_prompt_overrides_mode_valid CHECK (mode IN ('inherit', 'disabled', 'version')),
    CONSTRAINT account_group_prompt_overrides_mode_version_pair CHECK (
        (mode = 'version' AND version_id IS NOT NULL)
        OR (mode <> 'version' AND version_id IS NULL)
    ),
    -- 覆盖只能存在于真实的分组成员关系上；移除分组成员时随之清理。
    CONSTRAINT account_group_prompt_overrides_membership_fk
        FOREIGN KEY (account_id, group_id)
        REFERENCES account_groups (account_id, group_id)
        ON DELETE CASCADE
);

CREATE UNIQUE INDEX IF NOT EXISTS account_group_prompt_overrides_membership_key
    ON account_group_prompt_overrides (account_id, group_id);

CREATE INDEX IF NOT EXISTS account_group_prompt_overrides_group_id_idx
    ON account_group_prompt_overrides (group_id);

CREATE INDEX IF NOT EXISTS account_group_prompt_overrides_version_id_idx
    ON account_group_prompt_overrides (version_id);

CREATE TABLE IF NOT EXISTS prompt_admin_events (
    id BIGSERIAL PRIMARY KEY,
    action VARCHAR(40) NOT NULL,
    scope VARCHAR(20) NOT NULL,
    template_id BIGINT NULL,
    version_id BIGINT NULL,
    group_id BIGINT NULL,
    account_id BIGINT NULL,
    actor_id BIGINT NULL,
    actor_name VARCHAR(100) NULL,
    before_state JSONB NOT NULL DEFAULT '{}'::jsonb,
    after_state JSONB NOT NULL DEFAULT '{}'::jsonb,
    request_id VARCHAR(64) NULL,
    note TEXT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT prompt_admin_events_scope_valid CHECK (scope IN ('template', 'version', 'binding', 'account_override'))
);

CREATE INDEX IF NOT EXISTS prompt_admin_events_created_at_idx
    ON prompt_admin_events (created_at DESC);

CREATE INDEX IF NOT EXISTS prompt_admin_events_template_id_idx
    ON prompt_admin_events (template_id);

CREATE INDEX IF NOT EXISTS prompt_admin_events_group_id_idx
    ON prompt_admin_events (group_id);

CREATE INDEX IF NOT EXISTS prompt_admin_events_account_id_idx
    ON prompt_admin_events (account_id);
