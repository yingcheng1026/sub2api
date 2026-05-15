-- 155_add_redeem_code_plan_id.sql
-- 钱包模式 v4 — Phase B2.7
--
-- 给 redeem_codes 加 plan_id：让兑换码可以直接挂载 SubscriptionPlan，
-- 兑换时按 plan.WalletQuotaUsd 创建 wallet 订阅（plan_type='credits' → 永久）。
--
-- 用途：链动小铺额度卡 SKU（credits-100 / credits-500 / credits-1500）
-- 由 admin 批量生成「钱包 plan 兑换码」并粘贴入链动小铺商品 卡密管理。
--
-- 详细设计: ai-relay-infra/docs/plans/2026-05-13-wallet-multikey-credits-design.md §5 B2.7

ALTER TABLE redeem_codes
    ADD COLUMN IF NOT EXISTS plan_id BIGINT;

COMMENT ON COLUMN redeem_codes.plan_id IS
    '钱包模式额度卡：tied to subscription_plans.id；兑换时按 plan.wallet_quota_usd 建 wallet 订阅。type=wallet 时必填，type=subscription 时必须为 NULL。';

-- type=wallet 与 plan_id/group_id 的约束：
--   type='wallet'       → plan_id NOT NULL,  group_id NULL
--   type='subscription' → plan_id NULL,      group_id NOT NULL
--   其他 type           → 不约束（balance / concurrency / invitation / affiliate_balance）
ALTER TABLE redeem_codes
    DROP CONSTRAINT IF EXISTS chk_redeem_codes_wallet_shape;
ALTER TABLE redeem_codes
    ADD CONSTRAINT chk_redeem_codes_wallet_shape CHECK (
        (type <> 'wallet')
        OR (plan_id IS NOT NULL AND group_id IS NULL)
    );
