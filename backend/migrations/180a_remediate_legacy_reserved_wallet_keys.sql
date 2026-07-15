-- Before API-key purpose becomes an immutable security identity, normalize
-- historical rows that reused the reserved wallet-universal display name.
--
-- A group-bound key remains a valid fixed-group credential, so preserve its
-- secret and routing while assigning a non-reserved, deterministic name.
-- A NULL-group key without any undeleted wallet evidence has no safe billing
-- identity and already fails the current auth boundary; revoke and tombstone
-- it instead of silently blessing it as wallet_universal.

LOCK TABLE api_keys, user_subscriptions IN SHARE ROW EXCLUSIVE MODE;

UPDATE api_keys
SET name = '固定分组 Key（历史钱包迁移 #' || id::text || '）',
    updated_at = NOW()
WHERE deleted_at IS NULL
  AND name = '钱包通用 key（自动路由）'
  AND group_id IS NOT NULL;

UPDATE api_keys ak
SET status = 'revoked',
    key = '__deleted__hfc_orphan_wallet_key_' || ak.id::text,
    deleted_at = NOW(),
    updated_at = NOW()
WHERE ak.deleted_at IS NULL
  AND ak.name = '钱包通用 key（自动路由）'
  AND ak.group_id IS NULL
  AND NOT EXISTS (
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
          AND group_id IS NOT NULL
    ) THEN
        RAISE EXCEPTION 'legacy wallet key remediation blocked: group-bound reserved names remain'
            USING ERRCODE = '23514';
    END IF;

    IF EXISTS (
        SELECT 1
        FROM api_keys ak
        WHERE ak.deleted_at IS NULL
          AND ak.name = '钱包通用 key（自动路由）'
          AND ak.group_id IS NULL
          AND NOT EXISTS (
              SELECT 1
              FROM user_subscriptions us
              WHERE us.user_id = ak.user_id
                AND us.group_id IS NULL
                AND us.wallet_balance_usd IS NOT NULL
                AND us.deleted_at IS NULL
          )
    ) THEN
        RAISE EXCEPTION 'legacy wallet key remediation blocked: reserved NULL-group key lacks wallet evidence'
            USING ERRCODE = '23514';
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
        RAISE EXCEPTION 'legacy wallet key remediation blocked: duplicate evidenced wallet-universal candidates remain'
            USING ERRCODE = '23514';
    END IF;
END $$;
