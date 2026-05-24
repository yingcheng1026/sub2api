-- Prepare the HandsFreeClub paid-lite monthly plan.
-- Not auto-run. Review and execute only when the 99 yuan product is ready to go live.
--
-- Business shape:
--   price: ¥99 / 30 days
--   wallet quota: $400
--   covered groups: {1,3,4,5,24}
--   excluded: group_id=2 (claude-Max pool exclusive; assigned manually to high-ticket users)
--
-- Safety: the plan is inserted/updated with for_sale = FALSE by default. Flip it to TRUE
-- together with the public 链动小铺 URL handoff.

BEGIN;

DO $$
DECLARE
    v_plan_id BIGINT;
    v_plan_count INT;
    v_missing_groups BIGINT[];
BEGIN
    SELECT COUNT(*)
      INTO v_plan_count
      FROM subscription_plans
     WHERE name = 'paid-lite-v3-30d';

    IF v_plan_count > 1 THEN
        RAISE EXCEPTION 'more than one subscription_plans row named paid-lite-v3-30d; aborting';
    END IF;

    SELECT array_agg(group_id)
      INTO v_missing_groups
      FROM unnest(ARRAY[1,3,4,5,24]::BIGINT[]) AS required(group_id)
      LEFT JOIN groups g ON g.id = required.group_id
     WHERE g.id IS NULL;

    IF v_missing_groups IS NOT NULL THEN
        RAISE EXCEPTION 'missing required covered group ids: %', v_missing_groups;
    END IF;

    SELECT sp.id
      INTO v_plan_id
      FROM subscription_plans sp
     WHERE sp.name = 'paid-lite-v3-30d';

    IF v_plan_id IS NULL THEN
        INSERT INTO subscription_plans (
            group_id,
            wallet_quota_usd,
            plan_type,
            name,
            description,
            price,
            original_price,
            validity_days,
            validity_unit,
            features,
            product_name,
            for_sale,
            sort_order,
            created_at,
            updated_at
        ) VALUES (
            NULL,
            400.00000000,
            'subscription',
            'paid-lite-v3-30d',
            '30 天 $400 钱包额度,轻量 Claude Code / GPT 日常使用',
            99.00,
            199.00,
            30,
            'days',
            E'轻量 Claude Code / GPT 日常使用\n$400 月度钱包额度\n每日 cap $15\n覆盖常用公共模型分组',
            '轻量正式版',
            FALSE,
            15,
            NOW(),
            NOW()
        )
        RETURNING id INTO v_plan_id;
    ELSE
        UPDATE subscription_plans
           SET group_id = NULL,
               wallet_quota_usd = 400.00000000,
               plan_type = 'subscription',
               description = '30 天 $400 钱包额度,轻量 Claude Code / GPT 日常使用',
               price = 99.00,
               original_price = 199.00,
               validity_days = 30,
               validity_unit = 'days',
               features = E'轻量 Claude Code / GPT 日常使用\n$400 月度钱包额度\n每日 cap $15\n覆盖常用公共模型分组',
               product_name = '轻量正式版',
               for_sale = FALSE,
               sort_order = 15,
               updated_at = NOW()
         WHERE id = v_plan_id;
    END IF;

    DELETE FROM subscription_plan_groups
     WHERE plan_id = v_plan_id;

    INSERT INTO subscription_plan_groups (plan_id, group_id)
    SELECT v_plan_id, group_id
      FROM unnest(ARRAY[1,3,4,5,24]::BIGINT[]) AS covered(group_id)
    ON CONFLICT (plan_id, group_id) DO NOTHING;

    IF EXISTS (
        SELECT 1
          FROM subscription_plan_groups
         WHERE plan_id = v_plan_id
           AND group_id = 2
    ) THEN
        RAISE EXCEPTION 'exclusive group_id=2 must not be linked to paid-lite-v3-30d';
    END IF;
END $$;

SELECT
    sp.id,
    sp.name,
    sp.price,
    sp.wallet_quota_usd,
    sp.for_sale,
    array_agg(spg.group_id ORDER BY spg.group_id) AS plan_group_ids
FROM subscription_plans sp
LEFT JOIN subscription_plan_groups spg ON spg.plan_id = sp.id
WHERE sp.name = 'paid-lite-v3-30d'
GROUP BY sp.id, sp.name, sp.price, sp.wallet_quota_usd, sp.for_sale;

COMMIT;
