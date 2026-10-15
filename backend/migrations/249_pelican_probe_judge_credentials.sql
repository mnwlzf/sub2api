-- 249_pelican_probe_judge_credentials.sql
-- 糖果题 LLM 裁判的独立凭据：裁判 Key（加密存储）与裁判网关地址。
--
-- 背景：此前裁判复用探测 Key 打同一个网关，而裁判模型往往由另一个分组
-- （另一个平台）的账号提供。生产实证：分组 12（openai）的探测计划用
-- deepseek-v4.1-flash 当裁判，请求走分组 12 直接 404
-- "Model not supported by any configured account in this group"，裁判永远失败、
-- verdict 永远 pending。裁判必须有「去哪取这个模型」的信息。
--
-- judge_api_key_encrypted：裁判专用 API Key 密文（空 = 沿用探测 Key）。
-- judge_base_url：裁判专用网关地址（空 = 沿用探测网关）。
-- 纯加性变更，回滚安全（删列需手动）。

ALTER TABLE pelican_probe_plans ADD COLUMN IF NOT EXISTS judge_api_key_encrypted TEXT NOT NULL DEFAULT '';
ALTER TABLE pelican_probe_plans ADD COLUMN IF NOT EXISTS judge_base_url VARCHAR(500) NOT NULL DEFAULT '';
