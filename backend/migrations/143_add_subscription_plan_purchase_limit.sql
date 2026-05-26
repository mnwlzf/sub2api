ALTER TABLE subscription_plans
ADD COLUMN IF NOT EXISTS purchase_limit integer NOT NULL DEFAULT 0;

ALTER TABLE subscription_plans
DROP CONSTRAINT IF EXISTS subscription_plans_purchase_limit_check;

ALTER TABLE subscription_plans
ADD CONSTRAINT subscription_plans_purchase_limit_check
CHECK (purchase_limit >= 0);
