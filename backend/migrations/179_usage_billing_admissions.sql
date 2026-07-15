-- Immutable pre-upstream billing lifecycle. The base row freezes tenant,
-- pricing and affordability facts once. Candidate accounts live in an attempt
-- table so failover never rewrites the original authorization.
CREATE TABLE IF NOT EXISTS usage_billing_admissions (
    request_id VARCHAR(255) NOT NULL,
    api_key_id BIGINT NOT NULL,
    binding_fingerprint VARCHAR(64) NOT NULL,
    owner_token VARCHAR(64) NOT NULL,
    request_payload_hash VARCHAR(64) NOT NULL,
    auth_cache_locator VARCHAR(64),
    user_id BIGINT NOT NULL,
    subscription_id BIGINT,
    group_id BIGINT NOT NULL,
    effective_billing_group_id BIGINT NOT NULL,
    billing_type SMALLINT NOT NULL,

    billing_model VARCHAR(128) NOT NULL,
    pricing_source VARCHAR(64) NOT NULL,
    pricing_revision VARCHAR(128) NOT NULL,
    pricing_hash VARCHAR(64) NOT NULL,
    rate_multiplier NUMERIC(20, 10) NOT NULL,
    worst_case_cost_usd NUMERIC(20, 10) NOT NULL,

    -- Optional second frozen settlement schedule for result-polymorphic
    -- endpoints. All five columns are either present together or all NULL.
    alternate_billing_model VARCHAR(128),
    alternate_pricing_source VARCHAR(64),
    alternate_pricing_revision VARCHAR(128),
    alternate_pricing_hash VARCHAR(64),
    alternate_rate_multiplier NUMERIC(20, 10),

    state VARCHAR(24) NOT NULL DEFAULT 'prepared',
    prepared_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    dispatched_at TIMESTAMPTZ,
    outbox_pending_at TIMESTAMPTZ,
    settled_at TIMESTAMPTZ,
    orphaned_at TIMESTAMPTZ,
    reconcile_started_at TIMESTAMPTZ,
    abandoned_at TIMESTAMPTZ,

    -- Wallet reservations share this exact lifecycle. No FK is used: a
    -- post-auth delete/revoke cannot erase or block settlement.
    wallet_subscription_id BIGINT,
    wallet_reserved_usd NUMERIC(20, 10) NOT NULL DEFAULT 0,
    wallet_lease_expires_at TIMESTAMPTZ,
    wallet_consumed_at TIMESTAMPTZ,
    wallet_released_at TIMESTAMPTZ,

    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    PRIMARY KEY (request_id, api_key_id),
    CONSTRAINT usage_billing_admissions_fingerprint_check CHECK (binding_fingerprint ~ '^[0-9a-f]{64}$'),
    CONSTRAINT usage_billing_admissions_owner_check CHECK (owner_token ~ '^[0-9a-f]{16,64}$'),
    CONSTRAINT usage_billing_admissions_payload_hash_check CHECK (request_payload_hash ~ '^[0-9a-f]{64}$'),
    CONSTRAINT usage_billing_admissions_quote_text_check CHECK (
        billing_model IS NOT NULL AND BTRIM(billing_model) <> ''
        AND pricing_source IS NOT NULL AND BTRIM(pricing_source) <> ''
        AND pricing_revision IS NOT NULL AND BTRIM(pricing_revision) <> ''
        AND pricing_hash IS NOT NULL AND pricing_hash ~ '^[0-9a-f]{64}$'
    ),
    CONSTRAINT usage_billing_admissions_state_check CHECK (
        state IN ('prepared','dispatched','outbox_pending','settled','orphaned','reconcile','abandoned')
    ),
    CONSTRAINT usage_billing_admissions_billing_type_check CHECK (billing_type IN (0, 1)),
    CONSTRAINT usage_billing_admissions_cost_check CHECK (
        rate_multiplier > 0 AND worst_case_cost_usd > 0
        AND rate_multiplier NOT IN ('NaN'::numeric,'Infinity'::numeric,'-Infinity'::numeric)
        AND worst_case_cost_usd NOT IN ('NaN'::numeric,'Infinity'::numeric,'-Infinity'::numeric)
    ),
    CONSTRAINT usage_billing_admissions_alternate_quote_check CHECK (
        (alternate_billing_model IS NULL
            AND alternate_pricing_source IS NULL
            AND alternate_pricing_revision IS NULL
            AND alternate_pricing_hash IS NULL
            AND alternate_rate_multiplier IS NULL)
        OR
        (alternate_billing_model IS NOT NULL AND BTRIM(alternate_billing_model) <> ''
            AND alternate_pricing_source IS NOT NULL AND BTRIM(alternate_pricing_source) <> ''
            AND alternate_pricing_revision IS NOT NULL AND BTRIM(alternate_pricing_revision) <> ''
            AND alternate_pricing_hash IS NOT NULL AND alternate_pricing_hash ~ '^[0-9a-f]{64}$'
            AND alternate_rate_multiplier IS NOT NULL AND alternate_rate_multiplier > 0
            AND alternate_rate_multiplier NOT IN ('NaN'::numeric,'Infinity'::numeric,'-Infinity'::numeric))
    ),
    CONSTRAINT usage_billing_admissions_wallet_reserved_check CHECK (
        wallet_reserved_usd >= 0
        AND wallet_reserved_usd NOT IN ('NaN'::numeric,'Infinity'::numeric,'-Infinity'::numeric)
    ),
    CONSTRAINT usage_billing_admissions_wallet_terminal_check CHECK (
        wallet_consumed_at IS NULL OR wallet_released_at IS NULL
    ),
    CONSTRAINT usage_billing_admissions_wallet_shape_check CHECK (
        (wallet_subscription_id IS NULL
            AND wallet_reserved_usd = 0
            AND wallet_lease_expires_at IS NULL
            AND wallet_consumed_at IS NULL
            AND wallet_released_at IS NULL)
        OR
        (wallet_subscription_id IS NOT NULL
            AND subscription_id IS NOT NULL
            AND subscription_id = wallet_subscription_id
            AND billing_type = 1
            AND wallet_lease_expires_at IS NOT NULL
            AND (wallet_reserved_usd > 0 OR wallet_consumed_at IS NOT NULL OR wallet_released_at IS NOT NULL)
            AND (wallet_consumed_at IS NULL OR state IN ('outbox_pending','settled'))
            AND (wallet_released_at IS NULL OR state = 'abandoned'))
    )
);

-- A partially executed draft of migration 179 may already have created the
-- base table. Add the alternate quote columns and constraint idempotently.
ALTER TABLE usage_billing_admissions ADD COLUMN IF NOT EXISTS alternate_billing_model VARCHAR(128);
ALTER TABLE usage_billing_admissions ADD COLUMN IF NOT EXISTS alternate_pricing_source VARCHAR(64);
ALTER TABLE usage_billing_admissions ADD COLUMN IF NOT EXISTS alternate_pricing_revision VARCHAR(128);
ALTER TABLE usage_billing_admissions ADD COLUMN IF NOT EXISTS alternate_pricing_hash VARCHAR(64);
ALTER TABLE usage_billing_admissions ADD COLUMN IF NOT EXISTS alternate_rate_multiplier NUMERIC(20, 10);

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'usage_billing_admissions_alternate_quote_check'
          AND conrelid = 'usage_billing_admissions'::regclass
    ) THEN
        ALTER TABLE usage_billing_admissions
            ADD CONSTRAINT usage_billing_admissions_alternate_quote_check CHECK (
                (alternate_billing_model IS NULL
                    AND alternate_pricing_source IS NULL
                    AND alternate_pricing_revision IS NULL
                    AND alternate_pricing_hash IS NULL
                    AND alternate_rate_multiplier IS NULL)
                OR
                (alternate_billing_model IS NOT NULL AND BTRIM(alternate_billing_model) <> ''
                    AND alternate_pricing_source IS NOT NULL AND BTRIM(alternate_pricing_source) <> ''
                    AND alternate_pricing_revision IS NOT NULL AND BTRIM(alternate_pricing_revision) <> ''
                    AND alternate_pricing_hash IS NOT NULL AND alternate_pricing_hash ~ '^[0-9a-f]{64}$'
                    AND alternate_rate_multiplier IS NOT NULL AND alternate_rate_multiplier > 0
                    AND alternate_rate_multiplier NOT IN ('NaN'::numeric,'Infinity'::numeric,'-Infinity'::numeric)));
    END IF;
END;
$$;

CREATE TABLE IF NOT EXISTS usage_billing_admission_attempts (
    request_id VARCHAR(255) NOT NULL,
    api_key_id BIGINT NOT NULL,
    attempt_id VARCHAR(64) NOT NULL,
    owner_token VARCHAR(64) NOT NULL,
    attempt_fingerprint VARCHAR(64) NOT NULL,
    account_id BIGINT NOT NULL,
    account_type VARCHAR(32) NOT NULL,
    state VARCHAR(16) NOT NULL DEFAULT 'prepared',
    prepared_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    dispatched_at TIMESTAMPTZ,
    failed_at TIMESTAMPTZ,
    finalized_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (request_id, api_key_id, attempt_id),
    CONSTRAINT usage_billing_admission_attempts_admission_fk
        FOREIGN KEY (request_id, api_key_id)
        REFERENCES usage_billing_admissions (request_id, api_key_id)
        ON UPDATE RESTRICT ON DELETE RESTRICT,
    CONSTRAINT usage_billing_admission_attempts_fingerprint_check CHECK (attempt_fingerprint ~ '^[0-9a-f]{64}$'),
    CONSTRAINT usage_billing_admission_attempts_owner_check CHECK (owner_token ~ '^[0-9a-f]{16,64}$'),
    CONSTRAINT usage_billing_admission_attempts_state_check CHECK (state IN ('prepared','dispatched','failed','finalized'))
);

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

    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS trg_guard_usage_billing_admission_attempt_immutable ON usage_billing_admission_attempts;
CREATE TRIGGER trg_guard_usage_billing_admission_attempt_immutable
    BEFORE UPDATE OR DELETE ON usage_billing_admission_attempts
    FOR EACH ROW
    EXECUTE FUNCTION hfc_guard_usage_billing_admission_attempt_immutable();

-- Remove the earlier non-unique draft name if migration 179 partially ran.
DROP INDEX IF EXISTS idx_usage_billing_admissions_wallet_active;
DROP INDEX IF EXISTS idx_usage_billing_admissions_one_open_wallet;

-- The wallet row is locked before reservations are summed, so multiple
-- affordable requests may proceed without over-reserving the same balance.
-- Lease expiry is deliberately not a release condition.
CREATE INDEX IF NOT EXISTS idx_usage_billing_admissions_open_wallet
    ON usage_billing_admissions (wallet_subscription_id)
    WHERE wallet_subscription_id IS NOT NULL
      AND wallet_consumed_at IS NULL
      AND wallet_released_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_usage_billing_admissions_reconcile
    ON usage_billing_admissions (state, updated_at)
    WHERE state IN ('dispatched','outbox_pending','orphaned','reconcile');

CREATE INDEX IF NOT EXISTS idx_usage_billing_admissions_prepared_reconcile
    ON usage_billing_admissions (updated_at, wallet_lease_expires_at)
    WHERE state = 'prepared';

CREATE INDEX IF NOT EXISTS idx_usage_billing_admission_attempts_account
    ON usage_billing_admission_attempts (request_id, api_key_id, account_id, state);
