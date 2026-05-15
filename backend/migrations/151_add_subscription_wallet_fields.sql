-- 151_add_subscription_wallet_fields.sql
-- 钱包模式 v4 — Phase A schema #2
--
-- 给 subscription_plans 加 wallet_quota_usd, 给 user_subscriptions 加余额字段,
-- 放宽 group_id NOT NULL 约束（钱包模式 subscription 不绑单一 group）。
--
-- 模式判别:
--   subscription_plans.wallet_quota_usd IS NOT NULL  → 钱包模式 (v4)
--   user_subscriptions.wallet_balance_usd IS NOT NULL → 钱包模式 (v4)
--   两者 NULL 时走老的单 group 模式 (v3 不动)
--
-- 详细设计: ai-relay-infra/docs/plans/2026-05-10-wallet-mode-design.md §1.3

-- ============================================================================
-- 1. subscription_plans: 加 wallet_quota_usd, 放开 group_id NOT NULL
-- ============================================================================

ALTER TABLE subscription_plans
    ALTER COLUMN group_id DROP NOT NULL;

ALTER TABLE subscription_plans
    ADD COLUMN IF NOT EXISTS wallet_quota_usd DECIMAL(20, 8);

COMMENT ON COLUMN subscription_plans.wallet_quota_usd IS
    '钱包模式月度总额度（USD）。NULL = 走老的单 group 订阅模式（v3）。NOT NULL 时 group_id 应为 NULL，关联 group 走 subscription_plan_groups 表。';

-- 互斥保证: wallet_quota_usd 与 group_id 二选一
ALTER TABLE subscription_plans
    DROP CONSTRAINT IF EXISTS chk_subscription_plans_mode;
ALTER TABLE subscription_plans
    ADD CONSTRAINT chk_subscription_plans_mode CHECK (
        (wallet_quota_usd IS NULL AND group_id IS NOT NULL)        -- v3 老模式
        OR
        (wallet_quota_usd IS NOT NULL AND group_id IS NULL)        -- v4 钱包模式
    );

-- ============================================================================
-- 2. user_subscriptions: 加 wallet_balance_usd / wallet_initial_usd, 放开 group_id NOT NULL
-- ============================================================================

ALTER TABLE user_subscriptions
    ALTER COLUMN group_id DROP NOT NULL;

ALTER TABLE user_subscriptions
    ADD COLUMN IF NOT EXISTS wallet_balance_usd DECIMAL(20, 10);

ALTER TABLE user_subscriptions
    ADD COLUMN IF NOT EXISTS wallet_initial_usd DECIMAL(20, 10);

COMMENT ON COLUMN user_subscriptions.wallet_balance_usd IS
    '钱包模式当前余额（USD，含倍率扣减）。NULL = 走老的 daily/weekly/monthly_usage_usd 模式。';
COMMENT ON COLUMN user_subscriptions.wallet_initial_usd IS
    '钱包模式激活时的总额度（用于 UI 进度条显示 "已用 X%"）。';

-- 模式互斥
ALTER TABLE user_subscriptions
    DROP CONSTRAINT IF EXISTS chk_user_subscriptions_mode;
ALTER TABLE user_subscriptions
    ADD CONSTRAINT chk_user_subscriptions_mode CHECK (
        (wallet_balance_usd IS NULL AND group_id IS NOT NULL)      -- v3 老模式
        OR
        (wallet_balance_usd IS NOT NULL AND group_id IS NULL)      -- v4 钱包模式
    );

-- 余额非负兜底（race condition 三层防御之一）
ALTER TABLE user_subscriptions
    DROP CONSTRAINT IF EXISTS chk_user_subscriptions_wallet_balance_nonneg;
ALTER TABLE user_subscriptions
    ADD CONSTRAINT chk_user_subscriptions_wallet_balance_nonneg CHECK (
        wallet_balance_usd IS NULL OR wallet_balance_usd >= -0.01
    );

-- 同 user 同一时间只能有 1 条 active 钱包订阅（与老 group 订阅可并存）
-- 老的 user_subscriptions_user_group_unique_active 索引（migration 016 建的）
-- 由于 group_id 为 NULL 时 PG 不视为重复, 不会冲突, 保留即可。
CREATE UNIQUE INDEX IF NOT EXISTS user_subscriptions_one_active_wallet
    ON user_subscriptions(user_id)
    WHERE wallet_balance_usd IS NOT NULL
      AND status = 'active'
      AND deleted_at IS NULL;
