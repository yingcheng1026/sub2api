-- Evidence-based closeout for admissions whose upstream delivery outcome or
-- durable billing replay needs operator review. This migration adds no
-- automatic release path: every state-changing decision is explicit,
-- idempotent and append-only audited.

-- Keep the database's persistent financial-scope guard in sync with the new
-- admin write before the endpoint can create a reclaimable key.
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
        'admin.redeem_codes.create_and_redeem',
        'admin.usage.billing_reconciliation.resolve'
    );
$$;

UPDATE idempotency_records
SET is_reclaimable = FALSE
WHERE hfc_idempotency_scope_is_persistent(scope)
  AND is_reclaimable IS DISTINCT FROM FALSE;

CREATE TABLE IF NOT EXISTS usage_billing_reconciliation_audits (
    id BIGSERIAL PRIMARY KEY,
    request_id VARCHAR(255) NOT NULL,
    api_key_id BIGINT NOT NULL,
    action VARCHAR(32) NOT NULL,
    operator_id BIGINT NOT NULL,
    evidence_ref VARCHAR(500) NOT NULL,
    attempt_id VARCHAR(64),
    actual_cost_usd NUMERIC(20, 10),
    from_state VARCHAR(24) NOT NULL,
    to_state VARCHAR(24) NOT NULL,
    previous_error_code VARCHAR(64),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT usage_billing_reconciliation_audits_admission_fk
        FOREIGN KEY (request_id, api_key_id)
        REFERENCES usage_billing_admissions (request_id, api_key_id)
        ON UPDATE RESTRICT ON DELETE RESTRICT,
    CONSTRAINT usage_billing_reconciliation_audits_identity_check CHECK (
        api_key_id > 0 AND operator_id > 0
        AND evidence_ref ~ '^[A-Za-z0-9][A-Za-z0-9._:/#-]{7,499}$'
    ),
    CONSTRAINT usage_billing_reconciliation_audits_action_check CHECK (
        action IN ('retry_dead_letter', 'release_undelivered', 'settle_delivered')
    ),
    CONSTRAINT usage_billing_reconciliation_audits_resolution_shape_check CHECK (
        (
            action = 'retry_dead_letter'
            AND attempt_id IS NULL
            AND actual_cost_usd IS NULL
            AND from_state = 'reconcile'
            AND to_state = 'outbox_pending'
        )
        OR (
            action = 'release_undelivered'
            AND attempt_id IS NULL
            AND actual_cost_usd IS NULL
            AND from_state IN ('orphaned', 'reconcile')
            AND to_state = 'abandoned'
        )
        OR (
            action = 'settle_delivered'
            AND attempt_id IS NOT NULL
            AND attempt_id ~ '^[0-9a-f]{16,64}$'
            AND actual_cost_usd IS NOT NULL
            AND actual_cost_usd > 0
            AND actual_cost_usd NOT IN ('NaN'::numeric,'Infinity'::numeric,'-Infinity'::numeric)
            AND from_state IN ('orphaned', 'reconcile')
            AND to_state = 'outbox_pending'
        )
    )
);

CREATE INDEX IF NOT EXISTS idx_usage_billing_reconciliation_audits_case
    ON usage_billing_reconciliation_audits (request_id, api_key_id, created_at DESC, id DESC);

CREATE INDEX IF NOT EXISTS idx_usage_billing_reconciliation_audits_operator
    ON usage_billing_reconciliation_audits (operator_id, created_at DESC, id DESC);

CREATE UNIQUE INDEX IF NOT EXISTS idx_usage_billing_reconciliation_terminal_once
    ON usage_billing_reconciliation_audits (request_id, api_key_id)
    WHERE action IN ('release_undelivered', 'settle_delivered');

CREATE OR REPLACE FUNCTION hfc_reject_usage_billing_reconciliation_audit_mutation()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
BEGIN
    RAISE EXCEPTION 'usage billing reconciliation audits are append-only'
        USING ERRCODE = '23514';
END;
$$;

DROP TRIGGER IF EXISTS trg_hfc_reject_usage_billing_reconciliation_audit_mutation
    ON usage_billing_reconciliation_audits;
CREATE TRIGGER trg_hfc_reject_usage_billing_reconciliation_audit_mutation
    BEFORE UPDATE OR DELETE ON usage_billing_reconciliation_audits
    FOR EACH ROW
    EXECUTE FUNCTION hfc_reject_usage_billing_reconciliation_audit_mutation();

COMMENT ON TABLE usage_billing_reconciliation_audits IS
    'Append-only operator evidence for retrying, releasing or settling usage billing admissions';
