-- Observation begins here; no historical billing reconstruction or money changes.
CREATE TABLE balance_forecast_coverage (
 id boolean PRIMARY KEY DEFAULT true CHECK(id),
 started_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
INSERT INTO balance_forecast_coverage(id) VALUES(true);
-- Expression matches the immutable normalized billing command's user identity.
CREATE INDEX balance_forecast_billing_user_time ON usage_billing_events
 (((payload->'command'->>'UserID')::bigint),applied_at)
 WHERE state IN ('applied','complete');
CREATE INDEX balance_forecast_capture_user ON batch_image_money_holds(user_id)
 WHERE state='captured';
CREATE INDEX balance_forecast_primary_user ON regional_primary_requests(user_id)
 WHERE state='completed' AND balance_funded;
CREATE INDEX balance_forecast_regional_time ON regional_settled_events(lease_id,settled_at);
CREATE INDEX balance_forecast_users ON users(id)
 WHERE deleted_at IS NULL AND status='active' AND balance_notify_enabled AND balance>0;
