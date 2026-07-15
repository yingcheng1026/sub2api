-- Forward-only hardening for installations where migration 179 may already be
-- recorded. Settlement must never skip the pre-transport dispatch fence, and
-- lifecycle timestamps must agree with terminal financial states.

CREATE OR REPLACE FUNCTION hfc_guard_usage_billing_admission_immutable()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'usage billing admissions are append-only'
            USING ERRCODE = '23514';
    END IF;

    IF NEW.request_id IS DISTINCT FROM OLD.request_id
       OR NEW.api_key_id IS DISTINCT FROM OLD.api_key_id
       OR NEW.binding_fingerprint IS DISTINCT FROM OLD.binding_fingerprint
       OR NEW.owner_token IS DISTINCT FROM OLD.owner_token
       OR NEW.request_payload_hash IS DISTINCT FROM OLD.request_payload_hash
       OR NEW.auth_cache_locator IS DISTINCT FROM OLD.auth_cache_locator
       OR NEW.user_id IS DISTINCT FROM OLD.user_id
       OR NEW.subscription_id IS DISTINCT FROM OLD.subscription_id
       OR NEW.group_id IS DISTINCT FROM OLD.group_id
       OR NEW.effective_billing_group_id IS DISTINCT FROM OLD.effective_billing_group_id
       OR NEW.billing_type IS DISTINCT FROM OLD.billing_type
       OR NEW.billing_model IS DISTINCT FROM OLD.billing_model
       OR NEW.pricing_source IS DISTINCT FROM OLD.pricing_source
       OR NEW.pricing_revision IS DISTINCT FROM OLD.pricing_revision
       OR NEW.pricing_hash IS DISTINCT FROM OLD.pricing_hash
       OR NEW.rate_multiplier IS DISTINCT FROM OLD.rate_multiplier
       OR NEW.worst_case_cost_usd IS DISTINCT FROM OLD.worst_case_cost_usd
       OR NEW.alternate_billing_model IS DISTINCT FROM OLD.alternate_billing_model
       OR NEW.alternate_pricing_source IS DISTINCT FROM OLD.alternate_pricing_source
       OR NEW.alternate_pricing_revision IS DISTINCT FROM OLD.alternate_pricing_revision
       OR NEW.alternate_pricing_hash IS DISTINCT FROM OLD.alternate_pricing_hash
       OR NEW.alternate_rate_multiplier IS DISTINCT FROM OLD.alternate_rate_multiplier
       OR NEW.wallet_subscription_id IS DISTINCT FROM OLD.wallet_subscription_id
       OR NEW.wallet_reserved_usd IS DISTINCT FROM OLD.wallet_reserved_usd
       OR NEW.prepared_at IS DISTINCT FROM OLD.prepared_at
       OR NEW.created_at IS DISTINCT FROM OLD.created_at THEN
        RAISE EXCEPTION 'usage billing admission authorization facts are immutable'
            USING ERRCODE = '23514';
    END IF;

    IF NEW.state IS DISTINCT FROM OLD.state AND NOT (
        (OLD.state = 'prepared' AND NEW.state IN ('dispatched','orphaned','abandoned'))
        OR (OLD.state = 'dispatched' AND NEW.state IN ('outbox_pending','orphaned','reconcile','abandoned'))
        OR (OLD.state = 'outbox_pending' AND NEW.state IN ('settled','orphaned','reconcile'))
        OR (OLD.state = 'orphaned' AND NEW.state = 'reconcile')
        OR (OLD.state = 'reconcile' AND NEW.state IN ('outbox_pending','settled','abandoned'))
    ) THEN
        RAISE EXCEPTION 'invalid usage billing admission state transition: % -> %', OLD.state, NEW.state
            USING ERRCODE = '23514';
    END IF;

    IF NEW.wallet_subscription_id IS NOT NULL THEN
        IF NEW.state = 'settled' AND NEW.wallet_consumed_at IS NULL THEN
            RAISE EXCEPTION 'settled wallet admission requires wallet_consumed_at'
                USING ERRCODE = '23514';
        END IF;
        IF NEW.state <> 'settled' AND NEW.wallet_consumed_at IS NOT NULL THEN
            RAISE EXCEPTION 'wallet_consumed_at requires settled state'
                USING ERRCODE = '23514';
        END IF;
        IF NEW.state = 'abandoned' AND NEW.wallet_released_at IS NULL THEN
            RAISE EXCEPTION 'abandoned wallet admission requires wallet_released_at'
                USING ERRCODE = '23514';
        END IF;
        IF NEW.state <> 'abandoned' AND NEW.wallet_released_at IS NOT NULL THEN
            RAISE EXCEPTION 'wallet_released_at requires abandoned state'
                USING ERRCODE = '23514';
        END IF;
    END IF;

    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS trg_guard_usage_billing_admission_immutable ON usage_billing_admissions;
CREATE TRIGGER trg_guard_usage_billing_admission_immutable
    BEFORE UPDATE OR DELETE ON usage_billing_admissions
    FOR EACH ROW
    EXECUTE FUNCTION hfc_guard_usage_billing_admission_immutable();

CREATE OR REPLACE FUNCTION hfc_guard_usage_billing_admission_attempt_immutable()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'usage billing admission attempts are append-only'
            USING ERRCODE = '23514';
    END IF;

    IF NEW.request_id IS DISTINCT FROM OLD.request_id
       OR NEW.api_key_id IS DISTINCT FROM OLD.api_key_id
       OR NEW.attempt_id IS DISTINCT FROM OLD.attempt_id
       OR NEW.owner_token IS DISTINCT FROM OLD.owner_token
       OR NEW.attempt_fingerprint IS DISTINCT FROM OLD.attempt_fingerprint
       OR NEW.account_id IS DISTINCT FROM OLD.account_id
       OR NEW.account_type IS DISTINCT FROM OLD.account_type
       OR NEW.prepared_at IS DISTINCT FROM OLD.prepared_at
       OR NEW.created_at IS DISTINCT FROM OLD.created_at THEN
        RAISE EXCEPTION 'usage billing admission attempt identity is immutable'
            USING ERRCODE = '23514';
    END IF;

    IF NEW.state IS DISTINCT FROM OLD.state AND NOT (
        (OLD.state = 'prepared' AND NEW.state IN ('dispatched','failed'))
        OR (OLD.state = 'dispatched' AND NEW.state IN ('failed','finalized'))
    ) THEN
        RAISE EXCEPTION 'invalid usage billing admission attempt state transition: % -> %', OLD.state, NEW.state
            USING ERRCODE = '23514';
    END IF;

    IF NEW.state = 'prepared' AND (
        NEW.dispatched_at IS NOT NULL OR NEW.failed_at IS NOT NULL OR NEW.finalized_at IS NOT NULL
    ) THEN
        RAISE EXCEPTION 'prepared attempt cannot have terminal timestamps'
            USING ERRCODE = '23514';
    END IF;
    IF NEW.state = 'dispatched' AND (
        NEW.dispatched_at IS NULL OR NEW.failed_at IS NOT NULL OR NEW.finalized_at IS NOT NULL
    ) THEN
        RAISE EXCEPTION 'dispatched attempt timestamp mismatch'
            USING ERRCODE = '23514';
    END IF;
    IF NEW.state = 'failed' AND (NEW.failed_at IS NULL OR NEW.finalized_at IS NOT NULL) THEN
        RAISE EXCEPTION 'failed attempt timestamp mismatch'
            USING ERRCODE = '23514';
    END IF;
    IF NEW.state = 'finalized' AND (
        NEW.dispatched_at IS NULL OR NEW.finalized_at IS NULL OR NEW.failed_at IS NOT NULL
    ) THEN
        RAISE EXCEPTION 'finalized attempt timestamp mismatch'
            USING ERRCODE = '23514';
    END IF;

    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS trg_guard_usage_billing_admission_attempt_immutable ON usage_billing_admission_attempts;
CREATE TRIGGER trg_guard_usage_billing_admission_attempt_immutable
    BEFORE UPDATE OR DELETE ON usage_billing_admission_attempts
    FOR EACH ROW
    EXECUTE FUNCTION hfc_guard_usage_billing_admission_attempt_immutable();

CREATE INDEX IF NOT EXISTS idx_usage_billing_admissions_prepared_reconcile
    ON usage_billing_admissions (updated_at, wallet_lease_expires_at)
    WHERE state = 'prepared';
