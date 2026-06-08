\set ON_ERROR_STOP on

BEGIN;

CREATE TABLE IF NOT EXISTS usage_logs_model_backfill_gpt_compat_20260609 (
  id bigint PRIMARY KEY,
  old_model text,
  requested_model text,
  upstream_model text,
  model_mapping_chain text,
  group_id bigint,
  created_at timestamptz,
  backed_up_at timestamptz NOT NULL DEFAULT now()
);

CREATE TEMP TABLE _gpt_compat_usage_model_candidates ON COMMIT DROP AS
SELECT
  ul.id,
  ul.model AS old_model,
  ul.requested_model,
  ul.upstream_model,
  ul.model_mapping_chain,
  ul.group_id,
  ul.created_at
FROM usage_logs ul
JOIN groups g ON g.id = ul.group_id
WHERE g.platform = 'openai'
  AND ul.inbound_endpoint = '/v1/messages'
  AND ul.model LIKE 'claude%'
  AND TRIM(ul.upstream_model) LIKE 'gpt%';

DO $$
DECLARE
  candidate_rows bigint;
BEGIN
  SELECT COUNT(*) INTO candidate_rows FROM _gpt_compat_usage_model_candidates;
  IF candidate_rows <> 121688 THEN
    RAISE WARNING 'GPT compat usage_logs candidate count is %, expected 121688 from 2026-06-09 dry-run; explain before committing if this is unexpected.', candidate_rows;
  ELSE
    RAISE NOTICE 'GPT compat usage_logs candidate count matches expected dry-run: %', candidate_rows;
  END IF;
END $$;

\echo 'Candidate range and distribution before update'
SELECT
  COUNT(*) AS candidate_rows,
  MIN(created_at AT TIME ZONE 'Asia/Shanghai') AS first_cst,
  MAX(created_at AT TIME ZONE 'Asia/Shanghai') AS last_cst
FROM _gpt_compat_usage_model_candidates;

SELECT
  g.name AS group_name,
  COUNT(*) AS candidate_rows
FROM _gpt_compat_usage_model_candidates c
JOIN groups g ON g.id = c.group_id
GROUP BY g.name
ORDER BY candidate_rows DESC, g.name;

SELECT
  TRIM(upstream_model) AS upstream_model,
  COUNT(*) AS candidate_rows
FROM _gpt_compat_usage_model_candidates
GROUP BY TRIM(upstream_model)
ORDER BY candidate_rows DESC, upstream_model;

WITH inserted AS (
  INSERT INTO usage_logs_model_backfill_gpt_compat_20260609 (
    id,
    old_model,
    requested_model,
    upstream_model,
    model_mapping_chain,
    group_id,
    created_at
  )
  SELECT
    id,
    old_model,
    requested_model,
    upstream_model,
    model_mapping_chain,
    group_id,
    created_at
  FROM _gpt_compat_usage_model_candidates
  ON CONFLICT (id) DO NOTHING
  RETURNING id
)
SELECT
  (SELECT COUNT(*) FROM _gpt_compat_usage_model_candidates) AS candidate_rows,
  COUNT(*) AS newly_backed_up_rows
FROM inserted;

DO $$
DECLARE
  candidate_rows bigint;
  backup_rows bigint;
BEGIN
  SELECT COUNT(*) INTO candidate_rows FROM _gpt_compat_usage_model_candidates;
  SELECT COUNT(*)
    INTO backup_rows
  FROM usage_logs_model_backfill_gpt_compat_20260609 b
  JOIN _gpt_compat_usage_model_candidates c ON c.id = b.id;

  IF backup_rows <> candidate_rows THEN
    RAISE EXCEPTION 'Backup row count % does not match candidate row count %', backup_rows, candidate_rows;
  END IF;
  RAISE NOTICE 'Backup row count matches candidate row count: %', backup_rows;
END $$;

CREATE TEMP TABLE _gpt_compat_usage_model_updated ON COMMIT DROP AS
WITH to_update AS (
  SELECT
    b.id,
    b.old_model,
    TRIM(b.upstream_model) AS new_model
  FROM usage_logs_model_backfill_gpt_compat_20260609 b
  JOIN _gpt_compat_usage_model_candidates c ON c.id = b.id
  JOIN usage_logs ul ON ul.id = b.id
  WHERE ul.model = b.old_model
    AND ul.model LIKE 'claude%'
    AND TRIM(b.upstream_model) LIKE 'gpt%'
),
updated AS (
  UPDATE usage_logs ul
  SET model = tu.new_model
  FROM to_update tu
  WHERE ul.id = tu.id
  RETURNING ul.id
)
SELECT id FROM updated;

SELECT COUNT(*) AS updated_rows FROM _gpt_compat_usage_model_updated;

DO $$
DECLARE
  candidate_rows bigint;
  updated_rows bigint;
BEGIN
  SELECT COUNT(*) INTO candidate_rows FROM _gpt_compat_usage_model_candidates;
  SELECT COUNT(*) INTO updated_rows FROM _gpt_compat_usage_model_updated;

  IF updated_rows <> candidate_rows THEN
    RAISE EXCEPTION 'Updated row count % does not match candidate/backup row count %', updated_rows, candidate_rows;
  END IF;
  RAISE NOTICE 'Updated row count matches candidate/backup row count: %', updated_rows;
END $$;

\echo 'Post-update remaining GPT compat rows with Claude visible model'
SELECT COUNT(*) AS remaining_rows
FROM usage_logs ul
JOIN groups g ON g.id = ul.group_id
WHERE g.platform = 'openai'
  AND ul.inbound_endpoint = '/v1/messages'
  AND ul.model LIKE 'claude%'
  AND TRIM(ul.upstream_model) LIKE 'gpt%';

DO $$
DECLARE
  remaining_rows bigint;
BEGIN
  SELECT COUNT(*)
    INTO remaining_rows
  FROM usage_logs ul
  JOIN groups g ON g.id = ul.group_id
  WHERE g.platform = 'openai'
    AND ul.inbound_endpoint = '/v1/messages'
    AND ul.model LIKE 'claude%'
    AND TRIM(ul.upstream_model) LIKE 'gpt%';

  IF remaining_rows <> 0 THEN
    RAISE EXCEPTION 'Remaining GPT compat rows with Claude visible model: %', remaining_rows;
  END IF;
END $$;

\echo 'Backfill verification by required group'
SELECT
  g.name AS group_name,
  COUNT(*) AS backed_up_rows,
  COUNT(*) FILTER (WHERE ul.model = TRIM(b.upstream_model)) AS rows_now_showing_upstream_model,
  COUNT(*) FILTER (WHERE ul.requested_model IS DISTINCT FROM b.requested_model) AS requested_model_changed_rows,
  COUNT(*) FILTER (WHERE ul.model_mapping_chain IS DISTINCT FROM b.model_mapping_chain) AS mapping_chain_changed_rows
FROM usage_logs_model_backfill_gpt_compat_20260609 b
JOIN usage_logs ul ON ul.id = b.id
JOIN groups g ON g.id = b.group_id
WHERE g.name IN (
  'openai-default',
  'paid-standard-v3',
  'paid-trial-bonus',
  'paid-pro-v3',
  'paid-trial-v3',
  'openai-claude-shell-jimi',
  'paid-flagship-v3',
  '大岳专属'
)
GROUP BY g.name
ORDER BY backed_up_rows DESC, g.name;

COMMIT;
