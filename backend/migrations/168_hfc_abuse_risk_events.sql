-- HFC abuse-risk review events.
-- This table is a manual review and evidence layer only. It does not enable
-- automatic account/key blocking by itself.

CREATE TABLE IF NOT EXISTS hfc_abuse_risk_events (
    id                              BIGSERIAL PRIMARY KEY,
    source                          VARCHAR(64) NOT NULL DEFAULT '',
    severity                        VARCHAR(16) NOT NULL DEFAULT 'high',
    status                          VARCHAR(24) NOT NULL DEFAULT 'open',
    user_id                         BIGINT REFERENCES users(id) ON DELETE SET NULL,
    user_email                      VARCHAR(255) NOT NULL DEFAULT '',
    api_key_id                      BIGINT REFERENCES api_keys(id) ON DELETE SET NULL,
    api_key_name                    VARCHAR(100) NOT NULL DEFAULT '',
    group_id                        BIGINT REFERENCES groups(id) ON DELETE SET NULL,
    group_name                      VARCHAR(255) NOT NULL DEFAULT '',
    risk_score                      INT NOT NULL DEFAULT 0,
    signup_ip_prefix                VARCHAR(64) NOT NULL DEFAULT '',
    device_fingerprint_hash         VARCHAR(64) NOT NULL DEFAULT '',
    signup_user_agent_hash          VARCHAR(64) NOT NULL DEFAULT '',
    payment_order_id                BIGINT REFERENCES payment_orders(id) ON DELETE SET NULL,
    redeem_code_id                  BIGINT REFERENCES redeem_codes(id) ON DELETE SET NULL,
    referral_user_id                BIGINT REFERENCES users(id) ON DELETE SET NULL,
    content_moderation_log_id       BIGINT REFERENCES content_moderation_logs(id) ON DELETE SET NULL,
    summary                         TEXT NOT NULL DEFAULT '',
    evidence                        JSONB NOT NULL DEFAULT '[]'::jsonb,
    action_taken                    VARCHAR(64) NOT NULL DEFAULT '',
    action_note                     TEXT NOT NULL DEFAULT '',
    reviewed_by                     BIGINT REFERENCES users(id) ON DELETE SET NULL,
    reviewed_at                     TIMESTAMPTZ,
    telegram_sent                   BOOLEAN NOT NULL DEFAULT FALSE,
    telegram_sent_at                TIMESTAMPTZ,
    telegram_error                  TEXT NOT NULL DEFAULT '',
    created_at                      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at                      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT chk_hfc_abuse_risk_events_severity CHECK (severity IN ('low', 'medium', 'high', 'critical')),
    CONSTRAINT chk_hfc_abuse_risk_events_status CHECK (status IN ('open', 'reviewing', 'resolved', 'false_positive'))
);

CREATE INDEX IF NOT EXISTS idx_hfc_abuse_risk_events_created_at ON hfc_abuse_risk_events(created_at DESC);
CREATE INDEX IF NOT EXISTS idx_hfc_abuse_risk_events_status_created_at ON hfc_abuse_risk_events(status, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_hfc_abuse_risk_events_severity_created_at ON hfc_abuse_risk_events(severity, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_hfc_abuse_risk_events_source_created_at ON hfc_abuse_risk_events(source, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_hfc_abuse_risk_events_user_created_at ON hfc_abuse_risk_events(user_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_hfc_abuse_risk_events_api_key_created_at ON hfc_abuse_risk_events(api_key_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_hfc_abuse_risk_events_signup_ip_prefix ON hfc_abuse_risk_events(signup_ip_prefix);
CREATE INDEX IF NOT EXISTS idx_hfc_abuse_risk_events_device_fingerprint_hash ON hfc_abuse_risk_events(device_fingerprint_hash);
