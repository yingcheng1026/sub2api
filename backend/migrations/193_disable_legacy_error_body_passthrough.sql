-- Security migration: intentionally forward-only. Re-enabling raw upstream body
-- reflection would restore a cross-boundary information-disclosure vulnerability.
UPDATE error_passthrough_rules
SET passthrough_body = FALSE,
    custom_message = CASE
        WHEN custom_message IS NULL OR BTRIM(custom_message) = '' THEN 'Upstream request failed'
        ELSE LEFT(BTRIM(custom_message), 512)
    END,
    updated_at = NOW()
WHERE passthrough_body = TRUE;
