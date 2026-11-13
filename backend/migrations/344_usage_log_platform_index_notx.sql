CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_usage_logs_platform_created
    ON usage_logs (platform, created_at DESC, id DESC);
