-- Persist the revocation generation used by JWT and refresh-token validation.
-- Existing sessions are intentionally invalidated at rollout because the
-- previous generation was derived in process and could not be durably bumped.
ALTER TABLE users
    ADD COLUMN IF NOT EXISTS token_version bigint NOT NULL DEFAULT 0;

ALTER TABLE users
    DROP CONSTRAINT IF EXISTS users_token_version_nonnegative;

ALTER TABLE users
    ADD CONSTRAINT users_token_version_nonnegative
    CHECK (token_version >= 0);

COMMENT ON COLUMN users.token_version IS
    'Durable generation incremented to revoke all access and refresh tokens';
