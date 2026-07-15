-- Widen the historical plaintext password column for the domain-bound
-- AES-GCM envelope. Startup rewrites legacy rows before workers/listeners.
ALTER TABLE proxies
    ALTER COLUMN password TYPE text;

COMMENT ON COLUMN proxies.password IS
    'Domain-bound AES-GCM ciphertext for proxy authentication; plaintext is forbidden after startup migration';

-- Existing rows may still be plaintext when this DDL first lands, so the
-- constraint is NOT VALID. PostgreSQL nevertheless enforces it for every new
-- or updated row, preventing old binaries/direct SQL from reintroducing
-- plaintext while the startup transaction rewrites legacy data.
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'chk_proxies_password_encrypted_storage'
          AND conrelid = 'proxies'::regclass
    ) THEN
        ALTER TABLE proxies
            ADD CONSTRAINT chk_proxies_password_encrypted_storage
            CHECK (password IS NULL OR password LIKE 'sd:v3:%') NOT VALID;
    END IF;
END $$;
