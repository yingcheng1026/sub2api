-- PostgreSQL's regular-expression engine rejects counted repetitions above
-- 255. Migration 185 used a larger bound, so every reconciliation audit insert
-- failed before the evidence value could be checked. Keep the same 8..500
-- contract by separating length validation from the character whitelist.

ALTER TABLE usage_billing_reconciliation_audits
    DROP CONSTRAINT IF EXISTS usage_billing_reconciliation_audits_identity_check;

ALTER TABLE usage_billing_reconciliation_audits
    ADD CONSTRAINT usage_billing_reconciliation_audits_identity_check CHECK (
        api_key_id > 0
        AND operator_id > 0
        AND char_length(evidence_ref) BETWEEN 8 AND 500
        AND evidence_ref ~ '^[A-Za-z0-9][A-Za-z0-9._:/#-]*$'
    );
