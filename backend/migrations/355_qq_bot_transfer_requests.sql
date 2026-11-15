CREATE TABLE qq_bot_transfer_requests (
    token_hash     text             NOT NULL PRIMARY KEY,
    app_id         text             NOT NULL,
    from_member_id text             NOT NULL,
    from_user_id   bigint           NOT NULL,
    from_email     text             NOT NULL,
    to_user_id     bigint           NOT NULL,
    to_email       text             NOT NULL,
    amount         numeric(18,8)    NOT NULL,
    fee            numeric(18,8)    NOT NULL,
    net_amount     numeric(18,8)    NOT NULL,
    expires_at     timestamptz      NOT NULL,
    created_at     timestamptz      NOT NULL DEFAULT NOW(),
    UNIQUE (app_id, from_member_id)
);
