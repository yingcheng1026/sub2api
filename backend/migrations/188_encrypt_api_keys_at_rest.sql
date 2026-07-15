-- Widen the legacy plaintext column for the versioned AES-GCM envelope.
-- Application startup performs the bounded, compare-and-swap data rewrite
-- before opening the HTTP listener.
ALTER TABLE api_keys
    ALTER COLUMN key TYPE varchar(512);

DROP INDEX IF EXISTS idx_api_keys_key_trgm;

COMMENT ON COLUMN api_keys.key IS
    'Versioned AES-GCM ciphertext for customer API keys; plaintext is forbidden after startup migration';
COMMENT ON COLUMN api_keys.key_hash IS
    'HMAC-SHA-256 lookup locator derived from the independent API-key protection key';

-- Existing rows are rewritten by the pre-worker application migration. NOT
-- VALID permits that one-time legacy state, while PostgreSQL immediately
-- rejects every new/updated plaintext live row from an old binary or direct SQL.
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'chk_api_keys_encrypted_storage'
          AND conrelid = 'api_keys'::regclass
    ) THEN
        ALTER TABLE api_keys
            ADD CONSTRAINT chk_api_keys_encrypted_storage
            CHECK (
                (deleted_at IS NULL AND key LIKE 'enc:v1:%')
                OR
                (deleted_at IS NOT NULL AND (key LIKE 'enc:v1:%' OR key LIKE '__deleted__%'))
            ) NOT VALID;
    END IF;
END $$;

-- Remove historical credential copies from idempotency replay bodies. New
-- writes persist a masked response through IdempotencySensitiveResponse, but
-- old rows may contain top-level or nested wallet key fields indefinitely.
UPDATE idempotency_records
SET response_body = regexp_replace(
    response_body,
    '("key"[[:space:]]*:[[:space:]]*)"[^"]*"',
    E'\\1"***"',
    'g'
)
WHERE scope IN (
    'user.api_keys.create',
    'admin.subscriptions.assign',
    'admin.subscriptions.bulk_assign'
)
  AND response_body IS NOT NULL
  AND response_body LIKE '%"key"%';
