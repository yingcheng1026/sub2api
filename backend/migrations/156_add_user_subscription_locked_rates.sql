-- 156_add_user_subscription_locked_rates.sql
-- 月卡 F+ / 老用户永久旧价：给 user_subscriptions 增加订阅级锁定倍率。
--
-- 优先级：
--   user_subscriptions.locked_rates -> user_group_rate_multipliers -> groups.rate_multiplier -> default
--
-- 交接决策（2026-05-17）：
--   新月卡 F+：GPT/Kiro 1.0x, Antigravity 3.5x, Claude 8.5x。
--   老订阅 sub 16/29/40/45 + bonus user 21：继续 0.3x，不受新 F+ 倍率影响。

ALTER TABLE user_subscriptions
    ADD COLUMN IF NOT EXISTS locked_rates JSONB;

COMMENT ON COLUMN user_subscriptions.locked_rates IS
    '订阅级锁定倍率：group_id 字符串 -> rate_multiplier；存在时优先于用户专属倍率和 group 默认倍率。';

ALTER TABLE user_subscriptions
    DROP CONSTRAINT IF EXISTS chk_user_subscriptions_locked_rates_object;
ALTER TABLE user_subscriptions
    ADD CONSTRAINT chk_user_subscriptions_locked_rates_object CHECK (
        locked_rates IS NULL OR jsonb_typeof(locked_rates) = 'object'
    );

UPDATE user_subscriptions
SET
    locked_rates = COALESCE(user_subscriptions.locked_rates, '{}'::jsonb) || legacy_rates.rates,
    updated_at = NOW()
FROM (
    SELECT jsonb_object_agg(id::text, 0.3) AS rates
    FROM groups
    WHERE deleted_at IS NULL
      AND status = 'active'
      AND (
          LOWER(COALESCE(platform, '')) IN ('openai', 'anthropic', 'antigravity')
          OR LOWER(name) LIKE '%openai%'
          OR LOWER(name) LIKE '%gpt%'
          OR LOWER(name) LIKE '%kiro%'
          OR LOWER(name) LIKE '%cc-default%'
          OR LOWER(name) LIKE '%claude%'
          OR LOWER(name) LIKE '%antigravity%'
      )
) AS legacy_rates
WHERE deleted_at IS NULL
  AND (id IN (16, 29, 40, 45) OR user_id = 21)
  AND legacy_rates.rates IS NOT NULL;
