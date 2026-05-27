-- Usage monitor page acceleration.
-- Query pattern: time range + top users, then selected users by time bucket/model.
CREATE INDEX IF NOT EXISTS idx_usage_logs_created_user_model_actual_cost
    ON usage_logs (created_at, user_id, model)
    INCLUDE (actual_cost);
