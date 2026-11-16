-- Binding metadata is encrypted together with the secret in existing ciphertext
-- columns. This flag records a fresh verification for this claimed task only.
ALTER TABLE openai_totp_rotations
    ADD COLUMN IF NOT EXISTS binding_verified BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN IF NOT EXISTS login_email TEXT NOT NULL DEFAULT '';
UPDATE openai_totp_rotations r SET login_email=c.login_email
FROM openai_oauth_reauth_configs c WHERE c.account_id=r.account_id AND r.login_email='';
