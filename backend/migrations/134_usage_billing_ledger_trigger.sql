-- 134_usage_billing_ledger_trigger.sql
-- Keep an append-only audit ledger for every persisted billable usage log.
--
-- This trigger intentionally has no billing side effects: it does not deduct
-- balance, update API key quota, or mutate subscription counters. Those writes
-- remain owned by the application billing transaction in standard mode and by
-- the production simple-mode quota trigger where that mode is explicitly used.

CREATE OR REPLACE FUNCTION record_billing_usage_entry_from_usage_log()
RETURNS TRIGGER AS $$
BEGIN
  IF NEW.user_id IS NULL
     OR NEW.api_key_id IS NULL
     OR COALESCE(NEW.actual_cost, 0) <= 0 THEN
    RETURN NEW;
  END IF;

  INSERT INTO billing_usage_entries (
    usage_log_id,
    user_id,
    api_key_id,
    subscription_id,
    billing_type,
    applied,
    delta_usd,
    created_at
  )
  VALUES (
    NEW.id,
    NEW.user_id,
    NEW.api_key_id,
    NEW.subscription_id,
    COALESCE(NEW.billing_type, 0),
    TRUE,
    NEW.actual_cost,
    COALESCE(NEW.created_at, NOW())
  )
  ON CONFLICT (usage_log_id) DO NOTHING;

  RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_usage_logs_billing_ledger_entry ON usage_logs;

CREATE TRIGGER trg_usage_logs_billing_ledger_entry
AFTER INSERT ON usage_logs
FOR EACH ROW
EXECUTE FUNCTION record_billing_usage_entry_from_usage_log();

INSERT INTO billing_usage_entries (
  usage_log_id,
  user_id,
  api_key_id,
  subscription_id,
  billing_type,
  applied,
  delta_usd,
  created_at
)
SELECT
  ul.id,
  ul.user_id,
  ul.api_key_id,
  ul.subscription_id,
  COALESCE(ul.billing_type, 0),
  TRUE,
  ul.actual_cost,
  COALESCE(ul.created_at, NOW())
FROM usage_logs ul
LEFT JOIN billing_usage_entries b ON b.usage_log_id = ul.id
WHERE COALESCE(ul.actual_cost, 0) > 0
  AND ul.user_id IS NOT NULL
  AND ul.api_key_id IS NOT NULL
  AND b.id IS NULL
ON CONFLICT (usage_log_id) DO NOTHING;
