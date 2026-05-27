-- HFC risk-control Telegram alert settings.
-- Defaults are intentionally disabled so the migration has no production side effects.
INSERT INTO settings (key, value, updated_at)
VALUES
    ('hfc_telegram_risk_alert_enabled', 'false', NOW()),
    ('hfc_telegram_bot_token', '', NOW()),
    ('hfc_telegram_chat_id', '', NOW()),
    ('hfc_telegram_min_severity', 'high', NOW())
ON CONFLICT (key) DO NOTHING;
