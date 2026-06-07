-- HFC signup risk fields were originally prototyped as migration 157 in a
-- side branch. Production already uses 157-165 for locked rates and paid trial
-- guards, so keep this as the next forward migration.
ALTER TABLE users
  ADD COLUMN IF NOT EXISTS signup_ip VARCHAR(64) NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS signup_ip_prefix VARCHAR(64) NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS signup_user_agent_hash VARCHAR(64) NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS signup_device_fingerprint_hash VARCHAR(64) NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS trial_bonus_eligible BOOLEAN NOT NULL DEFAULT true,
  ADD COLUMN IF NOT EXISTS trial_bonus_hold_reason VARCHAR(80) NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS trial_bonus_risk_score INTEGER NOT NULL DEFAULT 0;

CREATE INDEX IF NOT EXISTS idx_users_signup_ip_prefix_created_at
  ON users (signup_ip_prefix, created_at)
  WHERE deleted_at IS NULL AND signup_ip_prefix <> '';

CREATE INDEX IF NOT EXISTS idx_users_signup_device_fingerprint_created_at
  ON users (signup_device_fingerprint_hash, created_at)
  WHERE deleted_at IS NULL AND signup_device_fingerprint_hash <> '';

CREATE INDEX IF NOT EXISTS idx_users_trial_bonus_hold
  ON users (trial_bonus_eligible, trial_bonus_hold_reason, created_at)
  WHERE deleted_at IS NULL AND role = 'user';
