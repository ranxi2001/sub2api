-- Site-wide recharge coupons. Issued coupons retain immutable rule snapshots.
CREATE TABLE IF NOT EXISTS recharge_coupon_templates (
    id BIGSERIAL PRIMARY KEY,
    name VARCHAR(100) NOT NULL,
    kind VARCHAR(20) NOT NULL CHECK (kind IN ('fixed','percent','bonus')),
    currency VARCHAR(3) NOT NULL,
    min_amount NUMERIC(20,2) NOT NULL CHECK (min_amount >= 0),
    value NUMERIC(20,2) NOT NULL CHECK (value > 0),
    max_discount NUMERIC(20,2) NOT NULL DEFAULT 0 CHECK (max_discount >= 0),
    valid_days INTEGER NOT NULL CHECK (valid_days BETWEEN 1 AND 3650),
    stackable BOOLEAN NOT NULL DEFAULT FALSE,
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    max_issues INTEGER NOT NULL DEFAULT 0 CHECK (max_issues >= 0),
    per_user_limit INTEGER NOT NULL DEFAULT 0 CHECK (per_user_limit >= 0),
    issued_count INTEGER NOT NULL DEFAULT 0 CHECK (issued_count >= 0),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CHECK (kind <> 'percent' OR value < 100)
);
CREATE TABLE IF NOT EXISTS user_recharge_coupons (
    id BIGSERIAL PRIMARY KEY,
    template_id BIGINT NOT NULL REFERENCES recharge_coupon_templates(id),
    user_id BIGINT NOT NULL REFERENCES users(id),
    snapshot JSONB NOT NULL,
    source VARCHAR(40) NOT NULL,
    source_key VARCHAR(250) NOT NULL UNIQUE,
    status VARCHAR(20) NOT NULL DEFAULT 'available' CHECK (status IN ('available','reserved','used','revoked')),
    order_id BIGINT REFERENCES payment_orders(id),
    expires_at TIMESTAMPTZ NOT NULL,
    used_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CHECK ((status IN ('reserved','used') AND order_id IS NOT NULL) OR status IN ('available','revoked'))
);
CREATE INDEX IF NOT EXISTS user_recharge_coupons_user ON user_recharge_coupons(user_id,id DESC);
CREATE INDEX IF NOT EXISTS user_recharge_coupons_template ON user_recharge_coupons(template_id,user_id);
CREATE TABLE IF NOT EXISTS recharge_coupon_orders (
    order_id BIGINT PRIMARY KEY REFERENCES payment_orders(id),
    coupon_id BIGINT NOT NULL REFERENCES user_recharge_coupons(id),
    user_id BIGINT NOT NULL REFERENCES users(id),
    original_amount NUMERIC(20,8) NOT NULL,
    discount_amount NUMERIC(20,8) NOT NULL DEFAULT 0,
    bonus_amount NUMERIC(20,8) NOT NULL DEFAULT 0,
    currency VARCHAR(3) NOT NULL,
    status VARCHAR(20) NOT NULL DEFAULT 'reserved' CHECK (status IN ('reserved','used','released','conflict','honored')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    used_at TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS recharge_coupon_orders_coupon ON recharge_coupon_orders(coupon_id,order_id DESC);
ALTER TABLE qq_bot_reward_codes ADD COLUMN IF NOT EXISTS coupon_template_id BIGINT REFERENCES recharge_coupon_templates(id);
ALTER TABLE qq_bot_reward_codes DROP CONSTRAINT IF EXISTS qq_bot_reward_codes_reward_type_check;
ALTER TABLE qq_bot_reward_codes ADD CONSTRAINT qq_bot_reward_codes_reward_type_check CHECK (reward_type IN ('balance','chances','physical','other','coupon'));

CREATE TABLE IF NOT EXISTS recharge_coupon_issuances (
 request_id VARCHAR(80) PRIMARY KEY,
 request_hash VARCHAR(64) NOT NULL,
 coupon_ids JSONB,
 created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

ALTER TABLE qq_bot_reward_codes DROP CONSTRAINT IF EXISTS qq_bot_reward_codes_source_check;
ALTER TABLE qq_bot_reward_codes ADD CONSTRAINT qq_bot_reward_codes_source_check CHECK (
 (source_kind='code' AND source_key IS NULL AND code_hash IS NOT NULL)
 OR (source_kind IN ('growth','streak','ranking','checkin','invite','lottery') AND source_key IS NOT NULL AND code_hash IS NULL AND status IN ('pending','fulfilled'))
);
