-- Build wallet integrity indexes without blocking live ledger writes.
-- The migration runner removes only invalid artifacts left by a failed prior
-- attempt. A valid index is never dropped during retry, so uniqueness
-- protection stays continuous if only migration-record persistence failed.

CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS idx_wallet_ledger_one_activation
    ON subscription_wallet_ledger(subscription_id)
    WHERE reason = 'activation';

CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS idx_user_subscriptions_one_active_credits_wallet
    ON user_subscriptions(user_id)
    WHERE wallet_balance_usd IS NOT NULL
      AND status = 'active'
      AND deleted_at IS NULL
      AND expires_at >= TIMESTAMPTZ '2099-12-30 23:59:59+00';
