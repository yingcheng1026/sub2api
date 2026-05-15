-- 154_extend_wallet_ledger_reason_topup.sql
-- 钱包模式 v4 — Phase B2.4
--
-- 额度卡 (plan_type='credits') 叠加场景：当用户已有 active 钱包订阅时，再买一张
-- 额度卡不新建 user_subscriptions 行，而是把 quota 合并到现有钱包：
--   UPDATE wallet_balance_usd += quota
--   UPDATE wallet_initial_usd += quota
--   INSERT subscription_wallet_ledger (reason='topup', delta=+quota)
--
-- 152 落地时 CHECK 约束只接受 5 个 reason，这里追加 'topup'。
--
-- 详细设计: ai-relay-infra/docs/plans/2026-05-13-wallet-multikey-credits-design.md §2.3

ALTER TABLE subscription_wallet_ledger
    DROP CONSTRAINT IF EXISTS chk_wallet_ledger_reason;

ALTER TABLE subscription_wallet_ledger
    ADD CONSTRAINT chk_wallet_ledger_reason CHECK (
        reason IN ('activation', 'usage', 'refund', 'adjustment', 'expiration', 'topup')
    );

COMMENT ON COLUMN subscription_wallet_ledger.reason IS
    'activation 激活 / usage 调用扣费 / refund 退款 / adjustment 客服调账 / expiration 过期清零 / topup 额度卡叠加充值';
