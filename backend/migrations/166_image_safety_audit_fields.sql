ALTER TABLE content_moderation_logs ADD COLUMN IF NOT EXISTS stage VARCHAR(32) NOT NULL DEFAULT 'input';
ALTER TABLE content_moderation_logs ADD COLUMN IF NOT EXISTS input_hash VARCHAR(64) NOT NULL DEFAULT '';
ALTER TABLE content_moderation_logs ADD COLUMN IF NOT EXISTS output_hashes JSONB NOT NULL DEFAULT '[]'::jsonb;
ALTER TABLE content_moderation_logs ADD COLUMN IF NOT EXISTS category_flags JSONB NOT NULL DEFAULT '{}'::jsonb;
ALTER TABLE content_moderation_logs ADD COLUMN IF NOT EXISTS category_applied_input_types JSONB NOT NULL DEFAULT '{}'::jsonb;
ALTER TABLE content_moderation_logs ADD COLUMN IF NOT EXISTS policy_rule VARCHAR(128) NOT NULL DEFAULT '';
ALTER TABLE content_moderation_logs ADD COLUMN IF NOT EXISTS upstream_request_id VARCHAR(128) NOT NULL DEFAULT '';
ALTER TABLE content_moderation_logs ADD COLUMN IF NOT EXISTS safety_identifier VARCHAR(128) NOT NULL DEFAULT '';

CREATE INDEX IF NOT EXISTS idx_content_moderation_logs_stage_created_at
    ON content_moderation_logs(stage, created_at DESC);

CREATE INDEX IF NOT EXISTS idx_content_moderation_logs_upstream_request_id
    ON content_moderation_logs(upstream_request_id)
    WHERE upstream_request_id <> '';

CREATE INDEX IF NOT EXISTS idx_content_moderation_logs_input_hash
    ON content_moderation_logs(input_hash)
    WHERE input_hash <> '';
