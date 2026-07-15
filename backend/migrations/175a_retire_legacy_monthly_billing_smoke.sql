-- HFC retired monthly-card billing on 2026-07-14 and replaced its old
-- monthly-billing smoke with the credits-wallet canary. One historical smoke
-- grant can otherwise survive as a perpetual subscription on the standard
-- `vip` group and block the monthly/group integrity migration that follows.
--
-- This remediation intentionally discovers the fixture by both dedicated key
-- names and then validates every entitlement attribute before changing data.
-- A missing fixture is a no-op; a partial or repurposed fixture fails closed.

LOCK TABLE api_keys, user_subscriptions, groups IN SHARE ROW EXCLUSIVE MODE;

DO $$
DECLARE
    canary_user_id bigint;
    canary_group_id bigint;
    vip_key_count integer;
    universal_key_count integer;
    grant_count integer;
    changed_count integer;
BEGIN
    SELECT COUNT(*), MIN(user_id), MIN(group_id)
    INTO vip_key_count, canary_user_id, canary_group_id
    FROM api_keys
    WHERE name = 'monthly-billing-smoke-vip-group22'
      AND deleted_at IS NULL;

    IF vip_key_count = 0 THEN
        RETURN;
    END IF;

    IF vip_key_count <> 1 OR canary_user_id IS NULL OR canary_group_id IS NULL THEN
        RAISE EXCEPTION 'legacy monthly smoke retirement blocked: VIP smoke key is ambiguous'
            USING ERRCODE = '23514';
    END IF;

    SELECT COUNT(*)
    INTO universal_key_count
    FROM api_keys
    WHERE user_id = canary_user_id
      AND name = 'monthly-billing-smoke-own-group'
      AND group_id IS NULL
      AND deleted_at IS NULL;

    IF universal_key_count <> 1 THEN
        RAISE EXCEPTION 'legacy monthly smoke retirement blocked: universal smoke key is missing or ambiguous for user_id=%',
            canary_user_id
            USING ERRCODE = '23514';
    END IF;

    IF NOT EXISTS (
        SELECT 1
        FROM groups
        WHERE id = canary_group_id
          AND name = 'vip'
          AND status = 'active'
          AND subscription_type = 'standard'
          AND deleted_at IS NULL
    ) THEN
        RAISE EXCEPTION 'legacy monthly smoke retirement blocked: VIP smoke group no longer matches the retired fixture; group_id=%',
            canary_group_id
            USING ERRCODE = '23514';
    END IF;

    SELECT COUNT(*)
    INTO grant_count
    FROM user_subscriptions
    WHERE user_id = canary_user_id
      AND group_id = canary_group_id
      AND status IN ('active', 'suspended')
      AND deleted_at IS NULL
      AND expires_at > NOW()
      AND wallet_balance_usd IS NULL
      AND wallet_initial_usd IS NULL
      AND notes ILIKE '%smoke%'
      AND notes ILIKE '%canary%'
      AND notes ILIKE '%monthly%';

    IF grant_count <> 1 THEN
        RAISE EXCEPTION 'legacy monthly smoke retirement blocked: expected one exact smoke grant, found % for user_id=% group_id=%',
            grant_count, canary_user_id, canary_group_id
            USING ERRCODE = '23514';
    END IF;

    UPDATE user_subscriptions
    SET status = 'expired',
        expires_at = LEAST(expires_at, NOW()),
        deleted_at = NOW(),
        updated_at = NOW(),
        notes = CONCAT_WS(
            E'\n',
            NULLIF(BTRIM(notes), ''),
            '[migration 175a] retired obsolete monthly billing smoke grant after credits-only cutover'
        )
    WHERE user_id = canary_user_id
      AND group_id = canary_group_id
      AND status IN ('active', 'suspended')
      AND deleted_at IS NULL
      AND expires_at > NOW()
      AND wallet_balance_usd IS NULL
      AND wallet_initial_usd IS NULL
      AND notes ILIKE '%smoke%'
      AND notes ILIKE '%canary%'
      AND notes ILIKE '%monthly%';

    GET DIAGNOSTICS changed_count = ROW_COUNT;
    IF changed_count <> 1 THEN
        RAISE EXCEPTION 'legacy monthly smoke retirement blocked: expected to retire one grant, changed %',
            changed_count
            USING ERRCODE = '23514';
    END IF;

    UPDATE api_keys
    SET status = 'revoked',
        key = '__deleted__hfc_monthly_smoke_' || id::text,
        deleted_at = NOW(),
        updated_at = NOW()
    WHERE user_id = canary_user_id
      AND name IN (
          'monthly-billing-smoke-own-group',
          'monthly-billing-smoke-vip-group22'
      )
      AND deleted_at IS NULL;

    GET DIAGNOSTICS changed_count = ROW_COUNT;
    IF changed_count <> 2 THEN
        RAISE EXCEPTION 'legacy monthly smoke retirement blocked: expected to revoke two keys, changed %',
            changed_count
            USING ERRCODE = '23514';
    END IF;
END $$;
