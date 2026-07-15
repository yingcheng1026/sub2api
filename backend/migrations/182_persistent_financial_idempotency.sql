-- Financial idempotency keys must never become executable again after their
-- ordinary response-replay TTL. Successful records are retained for replay;
-- incomplete records require manual recovery.

ALTER TABLE idempotency_records
    ADD COLUMN IF NOT EXISTS is_reclaimable BOOLEAN NOT NULL DEFAULT TRUE;

-- Freeze new claims while the historical scope backfill and guards are
-- installed. Otherwise an old application process could insert a financial
-- row with the temporary TRUE default between those operations.
LOCK TABLE idempotency_records IN SHARE ROW EXCLUSIVE MODE;

CREATE OR REPLACE FUNCTION hfc_idempotency_scope_is_persistent(candidate_scope TEXT)
RETURNS BOOLEAN
LANGUAGE SQL
IMMUTABLE
STRICT
PARALLEL SAFE
AS $$
    SELECT candidate_scope IN (
        'admin.subscriptions.assign',
        'admin.subscriptions.bulk_assign',
        'admin.subscriptions.extend',
        'admin.users.balance.update',
        'admin.redeem_codes.generate',
        'admin.redeem_codes.create_and_redeem'
    );
$$;

-- Rows created before this migration did not carry a persistence bit. Preserve
-- every known financial key as replay/manual-recovery-only before cleanup can
-- observe it.
UPDATE idempotency_records
SET is_reclaimable = FALSE
WHERE hfc_idempotency_scope_is_persistent(scope)
  AND is_reclaimable IS DISTINCT FROM FALSE;

ALTER TABLE idempotency_records
    DROP CONSTRAINT IF EXISTS chk_idempotency_records_persistent_scope;

ALTER TABLE idempotency_records
    ADD CONSTRAINT chk_idempotency_records_persistent_scope
    CHECK (
        NOT hfc_idempotency_scope_is_persistent(scope)
        OR is_reclaimable = FALSE
    );

CREATE OR REPLACE FUNCTION hfc_guard_persistent_financial_idempotency()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
BEGIN
    IF hfc_idempotency_scope_is_persistent(NEW.scope)
       AND NEW.is_reclaimable IS DISTINCT FROM FALSE THEN
        RAISE EXCEPTION 'persistent financial idempotency scope % cannot be reclaimable', NEW.scope
            USING ERRCODE = '23514';
    END IF;

    -- The identity and fingerprint define what was executed. Allowing any of
    -- them to change would turn a historical claim into a different operation.
    IF TG_OP = 'UPDATE' AND (
        NEW.scope IS DISTINCT FROM OLD.scope
        OR NEW.idempotency_key_hash IS DISTINCT FROM OLD.idempotency_key_hash
        OR NEW.request_fingerprint IS DISTINCT FROM OLD.request_fingerprint
    ) THEN
        RAISE EXCEPTION 'idempotency record identity is immutable'
            USING ERRCODE = '23514';
    END IF;

    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS trg_guard_persistent_financial_idempotency ON idempotency_records;
CREATE TRIGGER trg_guard_persistent_financial_idempotency
    BEFORE INSERT OR UPDATE ON idempotency_records
    FOR EACH ROW
    EXECUTE FUNCTION hfc_guard_persistent_financial_idempotency();

CREATE INDEX IF NOT EXISTS idx_idempotency_records_reclaimable_expires_at
    ON idempotency_records (expires_at)
    WHERE is_reclaimable = TRUE;

COMMENT ON COLUMN idempotency_records.is_reclaimable IS
    'false for persistent financial writes; expired rows are replay/manual-only and never automatically reclaimed or deleted';

COMMENT ON FUNCTION hfc_idempotency_scope_is_persistent(TEXT) IS
    'Exact financial idempotency scopes whose keys are permanently replay/manual-recovery-only';
