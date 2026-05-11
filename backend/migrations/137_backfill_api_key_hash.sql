-- Backfill hash and display prefix for existing API keys. This keeps the legacy
-- plaintext column in place for compatibility until the follow-up cleanup phase.

UPDATE api_keys
SET
    key_hash = encode(digest(key, 'sha256'), 'hex'),
    key_prefix = left(key, 12)
WHERE key IS NOT NULL
  AND key <> ''
  AND (key_hash IS NULL OR key_prefix = '');
