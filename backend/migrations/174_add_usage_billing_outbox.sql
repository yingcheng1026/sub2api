-- Durable, replay-safe usage billing events.
--
-- The table intentionally has no business foreign keys: deletion of a user,
-- API key, account or subscription must not erase or block an accounting event.

CREATE TABLE IF NOT EXISTS usage_billing_outbox (
    id BIGSERIAL PRIMARY KEY,
    request_id VARCHAR(255) NOT NULL,
    api_key_id BIGINT NOT NULL,
    request_fingerprint VARCHAR(64) NOT NULL,
    envelope_version SMALLINT NOT NULL DEFAULT 1,
    envelope JSONB NOT NULL,

    status VARCHAR(16) NOT NULL DEFAULT 'pending',
    attempt_count SMALLINT NOT NULL DEFAULT 0,
    max_attempts SMALLINT NOT NULL DEFAULT 8,
    available_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    locked_at TIMESTAMPTZ,
    locked_by VARCHAR(128),
    lease_token VARCHAR(64),
    last_attempt_at TIMESTAMPTZ,

    completed_at TIMESTAMPTZ,
    dead_lettered_at TIMESTAMPTZ,
    result_code VARCHAR(64),
    last_error_code VARCHAR(64),
    last_error VARCHAR(1000),

    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT usage_billing_outbox_request_api_key_key UNIQUE (request_id, api_key_id),
    CONSTRAINT usage_billing_outbox_envelope_object CHECK (jsonb_typeof(envelope) = 'object'),
    CONSTRAINT usage_billing_outbox_envelope_size_check CHECK (octet_length(envelope::text) <= 16384),
    CONSTRAINT usage_billing_outbox_outer_identity_check CHECK (
        request_id = envelope->>'request_id'
        AND api_key_id = (envelope->>'api_key_id')::BIGINT
        AND request_fingerprint = envelope->>'request_fingerprint'
        AND envelope_version = (envelope->>'version')::SMALLINT
    ),
    CONSTRAINT usage_billing_outbox_status_check CHECK (status IN ('pending', 'retry', 'completed', 'dead_letter')),
    CONSTRAINT usage_billing_outbox_attempt_count_check CHECK (attempt_count >= 0),
    CONSTRAINT usage_billing_outbox_max_attempts_check CHECK (max_attempts BETWEEN 1 AND 32),
    CONSTRAINT usage_billing_outbox_fingerprint_check CHECK (request_fingerprint ~ '^[0-9a-f]{64}$')
);

CREATE INDEX IF NOT EXISTS idx_usage_billing_outbox_claim
    ON usage_billing_outbox (available_at, id)
    WHERE status IN ('pending', 'retry');

CREATE INDEX IF NOT EXISTS idx_usage_billing_outbox_dead_letter
    ON usage_billing_outbox (dead_lettered_at, id)
    WHERE status = 'dead_letter';
