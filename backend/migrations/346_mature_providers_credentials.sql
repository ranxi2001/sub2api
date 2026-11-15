-- Credentials are encrypted with the existing AES-GCM encryptor. Public
-- projections expose only presence, never ciphertext or plaintext.
ALTER TABLE mature_providers ADD COLUMN IF NOT EXISTS auth_mode TEXT NOT NULL DEFAULT 'token' CHECK (auth_mode IN ('token','password'));
ALTER TABLE mature_providers ADD COLUMN IF NOT EXISTS account TEXT NOT NULL DEFAULT '';
ALTER TABLE mature_providers ADD COLUMN IF NOT EXISTS user_id TEXT NOT NULL DEFAULT '';
ALTER TABLE mature_providers ADD COLUMN IF NOT EXISTS credential_cipher TEXT NOT NULL DEFAULT '';
ALTER TABLE mature_providers ADD COLUMN IF NOT EXISTS last_error TEXT NOT NULL DEFAULT '';
