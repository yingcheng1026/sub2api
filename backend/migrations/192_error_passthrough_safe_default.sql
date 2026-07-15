-- Raw upstream errors may contain credentials, internal addresses, or provider diagnostics.
-- New rules must default to the operator-authored message path.
ALTER TABLE error_passthrough_rules
    ALTER COLUMN passthrough_body SET DEFAULT FALSE;
