-- Expand API key storage so authentication can move from plaintext lookup to hash lookup.

ALTER TABLE api_keys
    ADD COLUMN IF NOT EXISTS key_hash varchar(64),
    ADD COLUMN IF NOT EXISTS key_prefix varchar(16) NOT NULL DEFAULT '';

CREATE EXTENSION IF NOT EXISTS pgcrypto;
