-- Make a non-null usage_log_id the immutable idempotency key for wallet
-- settlement. The table lock closes the scan-to-index race with older writers.
LOCK TABLE subscription_wallet_ledger IN SHARE ROW EXCLUSIVE MODE;

DO $$
BEGIN
    IF EXISTS (
        SELECT usage_log_id
        FROM subscription_wallet_ledger
        WHERE usage_log_id IS NOT NULL
          AND reason = 'usage'
        GROUP BY usage_log_id
        HAVING COUNT(*) > 1
    ) THEN
        RAISE EXCEPTION 'duplicate wallet usage settlements must be reconciled before migration 191';
    END IF;
END
$$;

CREATE UNIQUE INDEX IF NOT EXISTS idx_wallet_ledger_one_usage_settlement
    ON subscription_wallet_ledger(usage_log_id)
    WHERE usage_log_id IS NOT NULL
      AND reason = 'usage';
