CREATE OR REPLACE FUNCTION hfc_enforce_paid_trial_redeem_once()
RETURNS trigger AS $$
DECLARE
    existing_id BIGINT;
BEGIN
    IF NEW.type = 'subscription'
       AND NEW.group_id = 13
       AND NEW.status = 'used'
       AND NEW.used_by IS NOT NULL THEN
        PERFORM pg_advisory_xact_lock(hashtextextended('hfc_paid_trial_redeem_once:' || NEW.used_by::text, 0));

        SELECT id INTO existing_id
        FROM redeem_codes
        WHERE type = 'subscription'
          AND group_id = 13
          AND status = 'used'
          AND used_by = NEW.used_by
          AND id <> NEW.id
        LIMIT 1;

        IF existing_id IS NOT NULL THEN
            RAISE EXCEPTION USING
                ERRCODE = '23505',
                MESSAGE = 'paid trial redeem code can only be used once per user';
        END IF;
    END IF;

    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_hfc_paid_trial_redeem_once ON redeem_codes;

CREATE TRIGGER trg_hfc_paid_trial_redeem_once
BEFORE INSERT OR UPDATE OF type, group_id, status, used_by
ON redeem_codes
FOR EACH ROW
EXECUTE FUNCTION hfc_enforce_paid_trial_redeem_once();
