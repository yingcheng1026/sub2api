\set ON_ERROR_STOP on

BEGIN;

WITH restored AS (
  UPDATE usage_logs ul
  SET model = b.old_model
  FROM usage_logs_model_backfill_gpt_compat_20260609 b
  WHERE ul.id = b.id
    AND ul.model = TRIM(b.upstream_model)
  RETURNING ul.id
)
SELECT COUNT(*) AS restored_rows FROM restored;

\echo 'Rollback verification: backed-up rows still showing upstream model'
SELECT COUNT(*) AS rows_still_showing_upstream_model
FROM usage_logs ul
JOIN usage_logs_model_backfill_gpt_compat_20260609 b ON b.id = ul.id
WHERE ul.model = TRIM(b.upstream_model);

\echo 'Rollback verification by group'
SELECT
  g.name AS group_name,
  COUNT(*) AS backed_up_rows,
  COUNT(*) FILTER (WHERE ul.model = b.old_model) AS rows_restored_to_old_model
FROM usage_logs_model_backfill_gpt_compat_20260609 b
JOIN usage_logs ul ON ul.id = b.id
JOIN groups g ON g.id = b.group_id
GROUP BY g.name
ORDER BY backed_up_rows DESC, g.name;

COMMIT;
