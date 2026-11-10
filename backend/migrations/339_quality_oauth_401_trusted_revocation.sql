-- Trust is internal and scoped by the row's captured credential identity.
ALTER TABLE quality_oauth_401_streaks
    ADD COLUMN IF NOT EXISTS own_revoked_401 BOOLEAN NOT NULL DEFAULT false;

-- Earlier binaries could count generic error text as revocation evidence.
-- Do not carry those unproven counts into the corrected classifier.
UPDATE quality_oauth_401_streaks SET failures = 0, last_round_until = NULL;
