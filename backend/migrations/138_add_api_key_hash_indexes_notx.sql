CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS apikey_key_hash
    ON api_keys (key_hash)
    WHERE deleted_at IS NULL AND key_hash IS NOT NULL;
