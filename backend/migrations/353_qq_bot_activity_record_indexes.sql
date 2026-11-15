-- Support paginated participation lists and invitation reward lookup without
-- scanning all activity history for each relationship on a page.
CREATE INDEX IF NOT EXISTS qq_bot_rewards_app_kind_time
    ON qq_bot_rewards(app_id, kind, created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS qq_bot_rewards_invite_source_member
    ON qq_bot_rewards(app_id, source_member_id, id) WHERE kind='invite';
CREATE INDEX IF NOT EXISTS qq_bot_rewards_invite_source_user
    ON qq_bot_rewards(source_user_id, id) WHERE kind='invite';
CREATE INDEX IF NOT EXISTS qq_bot_lottery_draws_app_time
    ON qq_bot_lottery_draws(app_id, created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS qq_bot_members_invite_time
    ON qq_bot_members(app_id, (COALESCE(invited_at,first_seen_at)) DESC, group_id, member_id)
    WHERE inviter_id<>'';
