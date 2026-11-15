-- The pending record commits BEFORE the remote request. A crash, timeout or
-- uncertain response must never turn an upstream key creation into a retry.
CREATE TABLE IF NOT EXISTS mature_provider_key_operations (
    provider_id BIGINT NOT NULL REFERENCES mature_providers(id) ON DELETE CASCADE,
    request_key_hash TEXT NOT NULL,
    input_hash TEXT NOT NULL,
    http_status INTEGER,
    result JSONB,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    completed_at TIMESTAMPTZ,
    PRIMARY KEY(provider_id,request_key_hash),
    CHECK ((http_status IS NULL AND result IS NULL) OR (http_status IS NOT NULL AND result IS NOT NULL))
);
