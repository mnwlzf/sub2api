-- 提示词正文上限从 32 KiB 扩容到 64 KiB，与 service.PromptBodyMaxBytes 保持一致。
--
-- 背景：支持“海鸥基础人格 + 单个完整 Skill”的静态组合模板（约 38.5 KiB）。
-- 现有行均小于 32 KiB，新约束可直接生效，无需回填。

ALTER TABLE prompt_template_drafts
    DROP CONSTRAINT IF EXISTS prompt_template_drafts_body_size;
ALTER TABLE prompt_template_drafts
    ADD CONSTRAINT prompt_template_drafts_body_size CHECK (octet_length(body) <= 65536);

ALTER TABLE prompt_template_versions
    DROP CONSTRAINT IF EXISTS prompt_template_versions_body_size;
ALTER TABLE prompt_template_versions
    ADD CONSTRAINT prompt_template_versions_body_size CHECK (octet_length(body) <= 65536);
