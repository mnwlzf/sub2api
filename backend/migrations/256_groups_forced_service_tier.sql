-- 256_groups_forced_service_tier.sql
-- 分组级「强制档位」配置。
--
-- 背景：「强制 fast」原本只是布尔开关，只能把出站 service_tier 提升到
-- priority(Fast)。但 upstream 实际可能是 ultrafast 档（第三方中转普遍恒回
-- ultrafast），后台用量记录只能记成 priority，前端渲染成 Fast，和真实服务档位
-- 不一致，运维无法据此对账。
--
-- 本列让分组可以显式声明强制档位：
--   ''          不强制（沿用 force_openai_fast 的 priority 行为）
--   'priority'  强制 Fast
--   'ultrafast' 强制 UltraFast
--
-- 纯新增列且默认空串，存量分组行为不变，回滚安全。

ALTER TABLE groups
    ADD COLUMN IF NOT EXISTS forced_service_tier varchar(16) NOT NULL DEFAULT '';
