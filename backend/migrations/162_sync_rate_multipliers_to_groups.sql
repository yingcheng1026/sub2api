-- 162_sync_rate_multipliers_to_groups.sql
--
-- HandsFreeClub production decision (2026-05-22):
-- groups.rate_multiplier is the single source of truth for customer-facing
-- display and billing. Historical locked_rates are retained only as a synced
-- compatibility cache for existing subscription response shapes.

COMMENT ON COLUMN user_subscriptions.locked_rates IS
    '历史兼容倍率缓存：group_id 字符串 -> rate_multiplier；必须与 groups.rate_multiplier 同步，不再作为独立锁价来源。';

CREATE OR REPLACE FUNCTION sync_subscription_locked_rates_on_group_rate_update()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
  IF NEW.rate_multiplier IS DISTINCT FROM OLD.rate_multiplier THEN
    UPDATE user_subscriptions
    SET locked_rates = jsonb_set(locked_rates, ARRAY[NEW.id::text], to_jsonb(NEW.rate_multiplier), true),
        updated_at = NOW()
    WHERE deleted_at IS NULL
      AND locked_rates IS NOT NULL
      AND locked_rates ? NEW.id::text;
  END IF;
  RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS trg_sync_locked_rates_on_group_rate_update ON groups;
CREATE TRIGGER trg_sync_locked_rates_on_group_rate_update
AFTER UPDATE OF rate_multiplier ON groups
FOR EACH ROW
EXECUTE FUNCTION sync_subscription_locked_rates_on_group_rate_update();

CREATE OR REPLACE FUNCTION normalize_subscription_locked_rates_to_groups()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
  synced_rates jsonb;
BEGIN
  IF NEW.locked_rates IS NULL THEN
    RETURN NEW;
  END IF;

  SELECT jsonb_object_agg(lr.group_id_text, to_jsonb(g.rate_multiplier))
  INTO synced_rates
  FROM jsonb_each_text(NEW.locked_rates) AS lr(group_id_text, rate_text)
  JOIN groups g ON lr.group_id_text ~ '^[0-9]+$' AND g.id = lr.group_id_text::bigint;

  IF synced_rates IS NOT NULL THEN
    NEW.locked_rates := NEW.locked_rates || synced_rates;
  END IF;

  RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS trg_normalize_subscription_locked_rates ON user_subscriptions;
CREATE TRIGGER trg_normalize_subscription_locked_rates
BEFORE INSERT OR UPDATE OF locked_rates ON user_subscriptions
FOR EACH ROW
WHEN (NEW.locked_rates IS NOT NULL)
EXECUTE FUNCTION normalize_subscription_locked_rates_to_groups();

WITH current_rates AS (
  SELECT us.id AS subscription_id,
         jsonb_object_agg(lr.group_id_text, to_jsonb(g.rate_multiplier)) AS synced_rates
  FROM user_subscriptions us
  CROSS JOIN LATERAL jsonb_each_text(us.locked_rates) AS lr(group_id_text, rate_text)
  JOIN groups g ON lr.group_id_text ~ '^[0-9]+$' AND g.id = lr.group_id_text::bigint
  WHERE us.deleted_at IS NULL
    AND us.locked_rates IS NOT NULL
  GROUP BY us.id
)
UPDATE user_subscriptions us
SET locked_rates = us.locked_rates || current_rates.synced_rates,
    updated_at = NOW()
FROM current_rates
WHERE us.id = current_rates.subscription_id
  AND us.locked_rates IS DISTINCT FROM (us.locked_rates || current_rates.synced_rates);

UPDATE user_group_rate_multipliers
SET rate_multiplier = NULL,
    updated_at = NOW()
WHERE rate_multiplier IS NOT NULL;

DELETE FROM user_group_rate_multipliers
WHERE rate_multiplier IS NULL
  AND rpm_override IS NULL;

CREATE OR REPLACE FUNCTION reject_user_group_rate_multiplier_override()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
  IF NEW.rate_multiplier IS NOT NULL THEN
    RAISE EXCEPTION 'user_group_rate_multipliers.rate_multiplier is disabled; use groups.rate_multiplier as the single billing multiplier source';
  END IF;
  RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS trg_reject_user_group_rate_multiplier_override ON user_group_rate_multipliers;
CREATE TRIGGER trg_reject_user_group_rate_multiplier_override
BEFORE INSERT OR UPDATE OF rate_multiplier ON user_group_rate_multipliers
FOR EACH ROW
WHEN (NEW.rate_multiplier IS NOT NULL)
EXECUTE FUNCTION reject_user_group_rate_multiplier_override();
