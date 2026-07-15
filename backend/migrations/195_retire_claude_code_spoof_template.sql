-- Migration: 195_retire_claude_code_spoof_template
--
-- A shared monitor cannot authenticate that it is Anthropic's official CLI.
-- Retire the historical seed and clear every plaintext snapshot that asserts
-- that identity. Header keys in JSONB are matched case-insensitively because
-- legacy rows predate canonical header normalization. The referenced snapshots
-- must be cleared before deleting the template because the foreign key uses
-- ON DELETE SET NULL.

WITH unsafe_templates AS (
    SELECT id
    FROM channel_monitor_request_templates
    WHERE provider = 'anthropic'
      AND (
          name = 'Claude Code 伪装'
          OR EXISTS (
              SELECT 1
              FROM jsonb_each_text(
                  CASE
                      WHEN jsonb_typeof(extra_headers) = 'object' THEN extra_headers
                      ELSE '{}'::jsonb
                  END
              ) AS header(key, value)
              WHERE (lower(header.key) = 'user-agent' AND lower(header.value) LIKE 'claude-cli/%')
                 OR (lower(header.key) = 'x-app' AND lower(header.value) = 'cli')
                 OR (lower(header.key) = 'anthropic-beta' AND (
                        lower(header.value) LIKE '%claude-code-%'
                        OR lower(header.value) LIKE '%oauth-%'
                    ))
                 OR (lower(header.key) = 'anthropic-dangerous-direct-browser-access'
                     AND lower(header.value) = 'true')
          )
          OR lower(COALESCE(body_override::text, '')) LIKE '%anthropic''s official cli for claude%'
          OR lower(COALESCE(body_override::text, '')) LIKE '%"cc_entrypoint"%'
          OR lower(COALESCE(body_override::text, '')) LIKE '%<billing_attribution>%'
      )
)
UPDATE channel_monitors
SET template_id = NULL,
    extra_headers = '{}'::jsonb,
    body_override_mode = 'off',
    body_override = NULL,
    updated_at = NOW()
WHERE template_id IN (SELECT id FROM unsafe_templates);

-- Also remove legacy plaintext snapshots whose template association was
-- already deleted under the historical snapshot-retention semantics.
UPDATE channel_monitors
SET template_id = NULL,
    extra_headers = '{}'::jsonb,
    body_override_mode = 'off',
    body_override = NULL,
    updated_at = NOW()
WHERE provider = 'anthropic'
  AND (
      EXISTS (
          SELECT 1
          FROM jsonb_each_text(
              CASE
                  WHEN jsonb_typeof(extra_headers) = 'object' THEN extra_headers
                  ELSE '{}'::jsonb
              END
          ) AS header(key, value)
          WHERE (lower(header.key) = 'user-agent' AND lower(header.value) LIKE 'claude-cli/%')
             OR (lower(header.key) = 'x-app' AND lower(header.value) = 'cli')
             OR (lower(header.key) = 'anthropic-beta' AND (
                    lower(header.value) LIKE '%claude-code-%'
                    OR lower(header.value) LIKE '%oauth-%'
                ))
             OR (lower(header.key) = 'anthropic-dangerous-direct-browser-access'
                 AND lower(header.value) = 'true')
      )
      OR lower(COALESCE(body_override::text, '')) LIKE '%anthropic''s official cli for claude%'
      OR lower(COALESCE(body_override::text, '')) LIKE '%"cc_entrypoint"%'
      OR lower(COALESCE(body_override::text, '')) LIKE '%<billing_attribution>%'
  );

DELETE FROM channel_monitor_request_templates
WHERE provider = 'anthropic'
  AND (
      name = 'Claude Code 伪装'
      OR EXISTS (
          SELECT 1
          FROM jsonb_each_text(
              CASE
                  WHEN jsonb_typeof(extra_headers) = 'object' THEN extra_headers
                  ELSE '{}'::jsonb
              END
          ) AS header(key, value)
          WHERE (lower(header.key) = 'user-agent' AND lower(header.value) LIKE 'claude-cli/%')
             OR (lower(header.key) = 'x-app' AND lower(header.value) = 'cli')
             OR (lower(header.key) = 'anthropic-beta' AND (
                    lower(header.value) LIKE '%claude-code-%'
                    OR lower(header.value) LIKE '%oauth-%'
                ))
             OR (lower(header.key) = 'anthropic-dangerous-direct-browser-access'
                 AND lower(header.value) = 'true')
      )
      OR lower(COALESCE(body_override::text, '')) LIKE '%anthropic''s official cli for claude%'
      OR lower(COALESCE(body_override::text, '')) LIKE '%"cc_entrypoint"%'
      OR lower(COALESCE(body_override::text, '')) LIKE '%<billing_attribution>%'
  );
