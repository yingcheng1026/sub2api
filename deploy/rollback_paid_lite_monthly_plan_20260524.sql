-- Roll back the prepared paid-lite monthly plan.
-- If the plan has already been referenced by orders/subscriptions, keep history intact
-- and only hide it from sale.

BEGIN;

DO $$
DECLARE
    v_plan_id BIGINT;
    v_ref_count INT;
BEGIN
    SELECT id
      INTO v_plan_id
      FROM subscription_plans
     WHERE name = 'paid-lite-v3-30d';

    IF v_plan_id IS NULL THEN
        RAISE NOTICE 'paid-lite-v3-30d not found; nothing to roll back';
        RETURN;
    END IF;

    SELECT COUNT(*)
      INTO v_ref_count
      FROM payment_orders
     WHERE plan_id = v_plan_id;

    DELETE FROM subscription_plan_groups
     WHERE plan_id = v_plan_id;

    IF v_ref_count = 0 THEN
        DELETE FROM subscription_plans
         WHERE id = v_plan_id;
    ELSE
        UPDATE subscription_plans
           SET for_sale = FALSE,
               updated_at = NOW()
         WHERE id = v_plan_id;
        RAISE NOTICE 'paid-lite-v3-30d has % references; kept row and set for_sale=false', v_ref_count;
    END IF;
END $$;

COMMIT;
