-- 153_add_plan_type.sql
-- 钱包模式 v4 — Phase B schema #1
--
-- 给 subscription_plans 加 plan_type，区分月卡（subscription）/ 额度卡（credits）。
-- 额度卡 = 永久有效（validity_days 设 36500 ≈ 100 年），余额烧完为止。
--
-- 详细设计: ai-relay-infra/docs/plans/2026-05-13-wallet-multikey-credits-design.md §2.1

ALTER TABLE subscription_plans
    ADD COLUMN IF NOT EXISTS plan_type VARCHAR(16) NOT NULL DEFAULT 'subscription';

COMMENT ON COLUMN subscription_plans.plan_type IS
    'subscription = 月卡（validity_days 控时长，到期冻结）；credits = 额度卡（永久有效，validity_days 设 36500，余额烧完为止）';

-- 取值约束
ALTER TABLE subscription_plans
    DROP CONSTRAINT IF EXISTS chk_subscription_plans_plan_type;
ALTER TABLE subscription_plans
    ADD CONSTRAINT chk_subscription_plans_plan_type CHECK (
        plan_type IN ('subscription', 'credits')
    );

-- 额度卡必须是钱包模式（wallet_quota_usd 非空 + group_id 空）
ALTER TABLE subscription_plans
    DROP CONSTRAINT IF EXISTS chk_subscription_plans_credits_must_be_wallet;
ALTER TABLE subscription_plans
    ADD CONSTRAINT chk_subscription_plans_credits_must_be_wallet CHECK (
        plan_type <> 'credits'
        OR (wallet_quota_usd IS NOT NULL AND group_id IS NULL)
    );
