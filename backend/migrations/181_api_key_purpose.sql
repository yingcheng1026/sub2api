-- Give the wallet universal API key an immutable database identity.
--
-- Historical rows did not have a purpose column. Backfill only the exact,
-- unambiguous system-key shape owned by a user with historical credits-wallet
-- evidence. Finite legacy wallets are valid evidence too; requiring a
-- permanent wallet here would block databases created by the old wallet flow.
-- Every other legacy key remains standard and is
-- rejected by the NULL-group auth boundary.

LOCK TABLE user_subscriptions, api_keys
    IN SHARE ROW EXCLUSIVE MODE;

ALTER TABLE api_keys
    ADD COLUMN IF NOT EXISTS purpose VARCHAR(32);

UPDATE api_keys
SET purpose = 'standard'
WHERE purpose IS NULL;

ALTER TABLE api_keys
    ALTER COLUMN purpose SET DEFAULT 'standard',
    ALTER COLUMN purpose SET NOT NULL;

DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM api_keys
        WHERE purpose NOT IN ('standard', 'wallet_universal')
    ) THEN
        RAISE EXCEPTION 'api key purpose migration blocked: unknown purpose value requires review';
    END IF;

    IF EXISTS (
        SELECT 1
        FROM api_keys
        WHERE deleted_at IS NULL
          AND name = '钱包通用 key（自动路由）'
          AND group_id IS NOT NULL
    ) THEN
        RAISE EXCEPTION 'api key purpose migration blocked: reserved wallet key has a non-null group';
    END IF;

    IF EXISTS (
        SELECT 1
        FROM api_keys
        WHERE deleted_at IS NULL
          AND name = '钱包通用 key（自动路由）'
          AND group_id IS NULL
        GROUP BY user_id
        HAVING COUNT(*) > 1
    ) THEN
        RAISE EXCEPTION 'api key purpose migration blocked: multiple live wallet universal key candidates for one user';
    END IF;

    IF EXISTS (
        SELECT 1
        FROM api_keys ak
        WHERE ak.deleted_at IS NULL
          AND ak.name = '钱包通用 key（自动路由）'
          AND ak.group_id IS NULL
          AND ak.purpose = 'standard'
		  AND NOT EXISTS (
			  SELECT 1
			  FROM user_subscriptions us
			  WHERE us.user_id = ak.user_id
				AND us.group_id IS NULL
				AND us.wallet_balance_usd IS NOT NULL
				AND us.deleted_at IS NULL
		  )
    ) THEN
		RAISE EXCEPTION 'api key purpose migration blocked: reserved wallet key has no historical credits wallet evidence';
    END IF;

    IF EXISTS (
        SELECT 1
        FROM api_keys
        WHERE purpose = 'wallet_universal'
          AND (group_id IS NOT NULL OR name <> '钱包通用 key（自动路由）')
    ) THEN
        RAISE EXCEPTION 'api key purpose migration blocked: wallet_universal purpose has an invalid name/group shape';
    END IF;
END $$;

UPDATE api_keys ak
SET purpose = 'wallet_universal'
WHERE ak.deleted_at IS NULL
  AND ak.purpose = 'standard'
  AND ak.name = '钱包通用 key（自动路由）'
  AND ak.group_id IS NULL
  AND EXISTS (
	  SELECT 1
	  FROM user_subscriptions us
	  WHERE us.user_id = ak.user_id
		AND us.group_id IS NULL
		AND us.wallet_balance_usd IS NOT NULL
		AND us.deleted_at IS NULL
  );

DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM api_keys
        WHERE deleted_at IS NULL
          AND name = '钱包通用 key（自动路由）'
          AND purpose <> 'wallet_universal'
    ) THEN
        RAISE EXCEPTION 'api key purpose migration blocked: live reserved wallet key was not safely classified';
    END IF;

    IF EXISTS (
        SELECT 1
        FROM api_keys
        WHERE deleted_at IS NULL
          AND purpose = 'wallet_universal'
        GROUP BY user_id
        HAVING COUNT(*) > 1
    ) THEN
        RAISE EXCEPTION 'api key purpose migration blocked: duplicate live wallet_universal purpose rows';
    END IF;
END $$;

ALTER TABLE api_keys
    DROP CONSTRAINT IF EXISTS chk_api_keys_purpose_known;
ALTER TABLE api_keys
    ADD CONSTRAINT chk_api_keys_purpose_known CHECK (
        purpose IN ('standard', 'wallet_universal')
    );

ALTER TABLE api_keys
    DROP CONSTRAINT IF EXISTS chk_api_keys_wallet_universal_shape;
ALTER TABLE api_keys
    ADD CONSTRAINT chk_api_keys_wallet_universal_shape CHECK (
        purpose <> 'wallet_universal'
        OR (group_id IS NULL AND name = '钱包通用 key（自动路由）')
    );

ALTER TABLE api_keys
    DROP CONSTRAINT IF EXISTS chk_api_keys_reserved_wallet_name;
ALTER TABLE api_keys
    ADD CONSTRAINT chk_api_keys_reserved_wallet_name CHECK (
        deleted_at IS NOT NULL
        OR name <> '钱包通用 key（自动路由）'
        OR purpose = 'wallet_universal'
    );

DROP INDEX IF EXISTS apikey_user_id_purpose;
CREATE UNIQUE INDEX apikey_user_id_purpose
    ON api_keys (user_id, purpose)
    WHERE deleted_at IS NULL AND purpose = 'wallet_universal';

CREATE OR REPLACE FUNCTION hfc_reject_api_key_purpose_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF NEW.purpose IS DISTINCT FROM OLD.purpose THEN
        RAISE EXCEPTION 'api key purpose is immutable; api_key_id=%', OLD.id
            USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END $$;

DROP TRIGGER IF EXISTS trg_hfc_reject_api_key_purpose_mutation ON api_keys;
CREATE TRIGGER trg_hfc_reject_api_key_purpose_mutation
BEFORE UPDATE OF purpose
ON api_keys
FOR EACH ROW
EXECUTE FUNCTION hfc_reject_api_key_purpose_mutation();

COMMENT ON COLUMN api_keys.purpose IS
    'Immutable security identity: standard or wallet_universal';
