-- 248_pelican_probe_options.sql
-- 鹈鹕探针改版：计划加测试类型（鹈鹕 SVG / 糖果题）、reasoning_effort 选项，
-- 以及糖果题的 LLM 裁判模型（judge_model_id）。
-- 纯加性变更，回滚安全（删列需手动）。

ALTER TABLE pelican_probe_plans ADD COLUMN IF NOT EXISTS test_type VARCHAR(20) NOT NULL DEFAULT 'pelican';
ALTER TABLE pelican_probe_plans ADD COLUMN IF NOT EXISTS reasoning_effort VARCHAR(20) NOT NULL DEFAULT '';
ALTER TABLE pelican_probe_plans ADD COLUMN IF NOT EXISTS judge_model_id VARCHAR(200) NOT NULL DEFAULT '';
