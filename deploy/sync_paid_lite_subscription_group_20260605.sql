-- Sync the HandsFreeClub paid-lite monthly subscription group.
--
-- Purpose:
--   Keep the admin "assign subscription -> group" dropdown aligned with the
--   99 yuan / 400 USD paid-lite monthly plan.
--
-- Production run:
--   docker exec -i sub2api-postgres psql -U sub2api -d sub2api -v ON_ERROR_STOP=1 \
--     < deploy/sync_paid_lite_subscription_group_20260605.sql

BEGIN;

DO $$
DECLARE
    v_lite_group_id BIGINT;
    v_plan_id BIGINT;
BEGIN
    SELECT id
      INTO v_plan_id
      FROM subscription_plans
     WHERE name = 'paid-lite-v3-30d';

    IF v_plan_id IS NULL THEN
        RAISE EXCEPTION 'paid-lite-v3-30d plan not found; aborting';
    END IF;

    UPDATE groups
       SET sort_order = CASE name
           WHEN 'paid-trial-v3' THEN 101
           WHEN 'paid-standard-v3' THEN 103
           WHEN 'paid-pro-v3' THEN 104
           WHEN 'paid-flagship-v3' THEN 105
           ELSE sort_order
       END,
       updated_at = NOW()
     WHERE name IN ('paid-trial-v3','paid-standard-v3','paid-pro-v3','paid-flagship-v3');

    SELECT id
      INTO v_lite_group_id
      FROM groups
     WHERE name = 'paid-lite-v3'
     LIMIT 1;

    IF v_lite_group_id IS NULL THEN
        INSERT INTO groups (
            name,
            description,
            rate_multiplier,
            is_exclusive,
            status,
            platform,
            subscription_type,
            daily_limit_usd,
            weekly_limit_usd,
            monthly_limit_usd,
            default_validity_days,
            sort_order,
            created_at,
            updated_at
        ) VALUES (
            'paid-lite-v3',
            '轻量正式版 - 30 天 $400 月额度 / $50 日 cap (早鸟 beta)',
            1.0000,
            FALSE,
            'active',
            'openai',
            'subscription',
            50.00000000,
            0.00000000,
            400.00000000,
            30,
            102,
            NOW(),
            NOW()
        )
        RETURNING id INTO v_lite_group_id;
    ELSE
        UPDATE groups
           SET description = '轻量正式版 - 30 天 $400 月额度 / $50 日 cap (早鸟 beta)',
               rate_multiplier = 1.0000,
               is_exclusive = FALSE,
               status = 'active',
               platform = 'openai',
               subscription_type = 'subscription',
               daily_limit_usd = 50.00000000,
               weekly_limit_usd = 0.00000000,
               monthly_limit_usd = 400.00000000,
               default_validity_days = 30,
               sort_order = 102,
               updated_at = NOW()
         WHERE id = v_lite_group_id;
    END IF;

    DELETE FROM subscription_plan_groups
     WHERE plan_id = v_plan_id
       AND group_id = (SELECT id FROM groups WHERE name = 'paid-trial-v3');

    INSERT INTO subscription_plan_groups (plan_id, group_id)
    VALUES (v_plan_id, v_lite_group_id)
    ON CONFLICT (plan_id, group_id) DO NOTHING;

    RAISE NOTICE 'paid-lite-v3 synced: plan_id=%, group_id=%', v_plan_id, v_lite_group_id;
END $$;

SELECT
    id,
    name,
    status,
    platform,
    subscription_type,
    daily_limit_usd,
    monthly_limit_usd,
    sort_order
FROM groups
WHERE name LIKE 'paid-%v3%'
ORDER BY sort_order, id;

SELECT
    sp.id,
    sp.name,
    sp.price,
    sp.wallet_quota_usd,
    sp.for_sale,
    sp.sort_order,
    spg.group_id,
    g.name AS group_name
FROM subscription_plans sp
LEFT JOIN subscription_plan_groups spg ON spg.plan_id = sp.id
LEFT JOIN groups g ON g.id = spg.group_id
WHERE sp.name = 'paid-lite-v3-30d'
ORDER BY spg.group_id;

COMMIT;
