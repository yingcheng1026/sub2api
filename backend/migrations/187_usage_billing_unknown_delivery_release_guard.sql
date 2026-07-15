-- A local durable spool is intentionally node-local. In a multi-instance
-- deployment another node cannot prove that an admission has no pending
-- billable envelope merely because PostgreSQL has no outbox row. Once an
-- attempt crossed the dispatch fence, unknown delivery must therefore remain
-- held until it is settled through the durable billing path.

CREATE OR REPLACE FUNCTION hfc_block_unknown_delivery_wallet_release()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
BEGIN
    IF OLD.wallet_subscription_id IS NOT NULL
       AND OLD.state IN ('orphaned', 'reconcile')
       AND NEW.state = 'abandoned'
       AND OLD.wallet_consumed_at IS NULL
       AND OLD.wallet_released_at IS NULL
       AND (
           OLD.dispatched_at IS NOT NULL
           OR EXISTS (
               SELECT 1
               FROM usage_billing_admission_attempts AS attempt
               WHERE attempt.request_id = OLD.request_id
                 AND attempt.api_key_id = OLD.api_key_id
                 AND (
                     attempt.dispatched_at IS NOT NULL
                     OR attempt.state IN ('dispatched', 'finalized')
                 )
           )
       ) THEN
        RAISE EXCEPTION 'cannot release wallet hold after upstream dispatch; request_id=% api_key_id=%',
            OLD.request_id, OLD.api_key_id
            USING ERRCODE = '23514',
                  CONSTRAINT = 'hfc_wallet_unknown_delivery_release';
    END IF;
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS trg_hfc_block_unknown_delivery_wallet_release
    ON usage_billing_admissions;
CREATE TRIGGER trg_hfc_block_unknown_delivery_wallet_release
    BEFORE UPDATE OF state ON usage_billing_admissions
    FOR EACH ROW
    EXECUTE FUNCTION hfc_block_unknown_delivery_wallet_release();

COMMENT ON FUNCTION hfc_block_unknown_delivery_wallet_release() IS
    'Fail-closed guard: an orphaned/reconcile wallet hold is not releasable after any upstream dispatch';
