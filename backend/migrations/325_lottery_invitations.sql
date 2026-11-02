ALTER TABLE lottery_pools DROP CONSTRAINT IF EXISTS lottery_pools_mode_check;
ALTER TABLE lottery_pools ADD CONSTRAINT lottery_pools_mode_check
    CHECK (mode IN ('single', 'cumulative', 'invite_visit', 'invite_signup', 'invite_recharge'));
ALTER TABLE lottery_pools ADD COLUMN IF NOT EXISTS invite_config JSONB;

CREATE TABLE IF NOT EXISTS lottery_invite_links (
    id BIGSERIAL PRIMARY KEY,
    pool_id BIGINT NOT NULL REFERENCES lottery_pools(id),
    user_id BIGINT NOT NULL REFERENCES users(id),
    code VARCHAR(64) NOT NULL UNIQUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE UNIQUE INDEX IF NOT EXISTS lotteryinvitelink_pool_id_user_id ON lottery_invite_links(pool_id, user_id);
CREATE TABLE IF NOT EXISTS lottery_invitations (
    id BIGSERIAL PRIMARY KEY,
    pool_id BIGINT NOT NULL REFERENCES lottery_pools(id),
    inviter_id BIGINT NOT NULL REFERENCES users(id),
    invitee_id BIGINT REFERENCES users(id),
    visitor_key VARCHAR(64) NOT NULL,
    ip_address VARCHAR(45) NOT NULL,
    country VARCHAR(2) NOT NULL DEFAULT '',
    net_units BIGINT NOT NULL DEFAULT 0 CHECK (net_units >= 0),
    qualified BOOLEAN NOT NULL DEFAULT FALSE,
    inviter_awarded BOOLEAN NOT NULL DEFAULT FALSE,
    invitee_awarded BOOLEAN NOT NULL DEFAULT FALSE,
    registered_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CHECK (invitee_id IS NULL OR invitee_id <> inviter_id)
);
CREATE UNIQUE INDEX IF NOT EXISTS lotteryinvitation_pool_id_visitor_key ON lottery_invitations(pool_id, visitor_key);
CREATE UNIQUE INDEX IF NOT EXISTS lotteryinvitation_pool_id_invitee_id ON lottery_invitations(pool_id, invitee_id);
CREATE INDEX IF NOT EXISTS lotteryinvitation_inviter_id_pool_id ON lottery_invitations(inviter_id, pool_id);
CREATE INDEX IF NOT EXISTS lotteryinvitation_invitee_id ON lottery_invitations(invitee_id);
CREATE INDEX IF NOT EXISTS lotteryinvitation_pool_id_ip_address_created_at ON lottery_invitations(pool_id, ip_address, created_at);
