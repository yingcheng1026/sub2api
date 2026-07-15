-- A wallet debit is allowed to settle against its frozen authorization after
-- upstream delivery. Soft-deleting that wallet first would make the
-- append-only ledger trigger reject the debit and strand the reservation.
-- The wallet row is already locked by UPDATE; admission creation takes the
-- same wallet-row lock before inserting its hold, so this guard closes both
-- check/delete and admit/delete races.

CREATE OR REPLACE FUNCTION hfc_guard_wallet_revoke_with_open_admission()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM usage_billing_admissions
        WHERE wallet_subscription_id = OLD.id
          AND wallet_consumed_at IS NULL
          AND wallet_released_at IS NULL
    ) THEN
        RAISE EXCEPTION 'wallet subscription has open usage billing admissions; subscription_id=%', OLD.id
            USING ERRCODE = '23514',
                  CONSTRAINT = 'hfc_wallet_open_admission_revoke';
    END IF;
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS trg_hfc_guard_wallet_revoke_with_open_admission ON user_subscriptions;
CREATE TRIGGER trg_hfc_guard_wallet_revoke_with_open_admission
    BEFORE UPDATE OF deleted_at ON user_subscriptions
    FOR EACH ROW
    WHEN (
        OLD.wallet_balance_usd IS NOT NULL
        AND OLD.deleted_at IS NULL
        AND NEW.deleted_at IS NOT NULL
    )
    EXECUTE FUNCTION hfc_guard_wallet_revoke_with_open_admission();

COMMENT ON FUNCTION hfc_guard_wallet_revoke_with_open_admission() IS
    'Prevents wallet soft-delete from stranding a frozen usage debit or reservation';
