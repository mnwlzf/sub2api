-- 247_pelican_probe_public.sql
-- 鹈鹕探针对用户公开：计划级 is_public 开关。
-- 全局总开关走 settings KV（pelican_probe_enabled，缺省 true），无需 DDL。

ALTER TABLE pelican_probe_plans ADD COLUMN IF NOT EXISTS is_public BOOLEAN NOT NULL DEFAULT false;
CREATE INDEX IF NOT EXISTS idx_ppp_public ON pelican_probe_plans(is_public) WHERE is_public = true;
