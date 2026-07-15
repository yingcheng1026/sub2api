-- Monthly plans use their subscription group and quota windows. Only credits
-- plans may create a permanent wallet. This migration deliberately refuses to
-- guess how a historical monthly wallet plan should map to a group: changing
-- group_id/wallet_quota_usd is a business-data decision and needs an explicit,
-- reviewed remediation migration. Existing finite wallet subscriptions remain
-- readable/chargeable for compatibility, but no new finite wallet can appear.

-- Keep all preflight snapshots stable until their constraints/triggers
-- are installed. The migration runner's 5-second lock timeout makes busy
-- production traffic fail closed and retry instead of allowing a bad row to
-- slip between validation and trigger creation.
LOCK TABLE groups, subscription_plans, subscription_plan_groups, user_subscriptions, payment_orders
    IN SHARE ROW EXCLUSIVE MODE;

-- Keep the database lifecycle guards aligned with webhook recovery. FAILED is
-- fulfillable only after a trusted payment was persisted; provider-creation
-- failures must not freeze plan/group administration. CANCELLED and EXPIRED
-- protect their historical plan/group only during a short likely-settlement
-- window. Trusted late paid evidence is still accepted by the service; if the
-- target was removed, that order becomes an explicit failed fulfillment for
-- admin remediation/refund instead of remaining silently EXPIRED.
CREATE OR REPLACE FUNCTION hfc_payment_order_is_fulfillable(
    order_status text,
    order_updated_at timestamptz,
    order_paid_at timestamptz,
    order_payment_trade_no text
)
RETURNS boolean
LANGUAGE sql
STABLE
AS $$
    SELECT CASE
        WHEN order_status IN ('PENDING', 'PAID', 'RECHARGING') THEN TRUE
        WHEN order_status = 'FAILED' THEN
            order_paid_at IS NOT NULL
            AND BTRIM(COALESCE(order_payment_trade_no, '')) <> ''
        WHEN order_status IN ('CANCELLED', 'EXPIRED') THEN
            order_updated_at >= NOW() - INTERVAL '5 minutes'
        ELSE FALSE
    END
$$;

DO $$
DECLARE
    invalid_plan_ids bigint[];
BEGIN
    SELECT ARRAY_AGG(id ORDER BY id)
    INTO invalid_plan_ids
    FROM (
        SELECT sp.id
        FROM subscription_plans sp
        LEFT JOIN groups plan_group ON plan_group.id = sp.group_id
        WHERE NOT (
            (sp.plan_type = 'subscription' AND sp.group_id IS NOT NULL AND sp.wallet_quota_usd IS NULL)
            OR (
                sp.plan_type = 'credits'
                AND sp.group_id IS NULL
                AND sp.wallet_quota_usd IS NOT NULL
                AND sp.wallet_quota_usd > 0
                AND sp.wallet_quota_usd NOT IN (
                    'NaN'::numeric,
                    'Infinity'::numeric,
                    '-Infinity'::numeric
                )
            )
        )
        OR (
            sp.plan_type = 'subscription'
            AND (
                plan_group.id IS NULL
                OR (
                    sp.for_sale = TRUE
                    AND (
                        plan_group.status <> 'active'
                        OR plan_group.subscription_type <> 'subscription'
                        OR plan_group.deleted_at IS NOT NULL
                    )
                )
            )
        )
        ORDER BY sp.id
        LIMIT 20
    ) invalid_plans;

    IF COALESCE(CARDINALITY(invalid_plan_ids), 0) > 0 THEN
        RAISE EXCEPTION 'monthly/credits plan migration blocked: explicit remediation required for plan_ids=%',
            invalid_plan_ids
            USING ERRCODE = '23514';
    END IF;
END $$;

DO $$
DECLARE
    invalid_subscription_ids bigint[];
BEGIN
    SELECT ARRAY_AGG(subscription_id ORDER BY subscription_id)
    INTO invalid_subscription_ids
    FROM (
        SELECT us.id AS subscription_id
        FROM user_subscriptions us
        LEFT JOIN groups subscription_group ON subscription_group.id = us.group_id
        WHERE us.group_id IS NOT NULL
          AND us.deleted_at IS NULL
          AND us.expires_at > NOW()
          AND us.status IN ('active', 'suspended')
          AND (
              subscription_group.id IS NULL
              OR subscription_group.status <> 'active'
              OR subscription_group.subscription_type <> 'subscription'
              OR subscription_group.deleted_at IS NOT NULL
          )
        ORDER BY us.id
        LIMIT 20
    ) invalid_subscriptions;

    IF COALESCE(CARDINALITY(invalid_subscription_ids), 0) > 0 THEN
        RAISE EXCEPTION 'monthly subscription migration blocked: active or resumable subscriptions reference an unavailable group; subscription_ids=%',
            invalid_subscription_ids
            USING ERRCODE = '23514';
    END IF;
END $$;

DO $$
DECLARE
    mixed_subscription_ids bigint[];
BEGIN
    SELECT ARRAY_AGG(id ORDER BY id)
    INTO mixed_subscription_ids
    FROM (
        SELECT id
        FROM user_subscriptions
        WHERE (wallet_balance_usd IS NULL) <> (wallet_initial_usd IS NULL)
           OR (
               group_id IS NOT NULL
               AND (wallet_balance_usd IS NOT NULL OR wallet_initial_usd IS NOT NULL)
           )
        ORDER BY id
        LIMIT 20
    ) mixed_subscriptions;

    IF COALESCE(CARDINALITY(mixed_subscription_ids), 0) > 0 THEN
        RAISE EXCEPTION 'monthly/credits subscription migration blocked: mixed group+wallet rows require remediation; subscription_ids=%',
            mixed_subscription_ids
            USING ERRCODE = '23514';
    END IF;
END $$;

DO $$
DECLARE
    invalid_order_ids bigint[];
BEGIN
    SELECT ARRAY_AGG(order_id ORDER BY order_id)
    INTO invalid_order_ids
    FROM (
        SELECT po.id AS order_id
        FROM payment_orders po
        LEFT JOIN subscription_plans sp ON sp.id = po.plan_id
        LEFT JOIN groups legacy_group ON legacy_group.id = po.subscription_group_id
        WHERE po.order_type = 'subscription'
          AND hfc_payment_order_is_fulfillable(
              po.status,
              po.updated_at,
              po.paid_at,
              po.payment_trade_no
          )
          AND (
              (
                  po.plan_id IS NULL
                  AND (
                      po.subscription_group_id IS NULL
                      OR legacy_group.id IS NULL
                      OR legacy_group.status <> 'active'
                      OR legacy_group.subscription_type <> 'subscription'
                      OR legacy_group.deleted_at IS NOT NULL
                  )
              )
              OR (
                  po.plan_id IS NOT NULL
                  AND (
                      sp.id IS NULL
                      OR (sp.plan_type = 'subscription' AND (
                          po.subscription_group_id IS NULL
                          OR po.subscription_group_id IS DISTINCT FROM sp.group_id
                      ))
                      OR (sp.plan_type = 'credits' AND po.subscription_group_id IS NOT NULL)
                  )
              )
          )
        ORDER BY po.id
        LIMIT 20
    ) invalid_orders;

    IF COALESCE(CARDINALITY(invalid_order_ids), 0) > 0 THEN
        RAISE EXCEPTION 'monthly/credits order migration blocked: fulfillable orders use an invalid path; order_ids=%',
            invalid_order_ids
            USING ERRCODE = '23514';
    END IF;
END $$;

ALTER TABLE subscription_plans
    DROP CONSTRAINT IF EXISTS chk_hfc_subscription_plans_monthly_group_only;
ALTER TABLE subscription_plans
    ADD CONSTRAINT chk_hfc_subscription_plans_monthly_group_only CHECK (
        (plan_type = 'subscription' AND group_id IS NOT NULL AND wallet_quota_usd IS NULL)
        OR (
            plan_type = 'credits'
            AND group_id IS NULL
            AND wallet_quota_usd IS NOT NULL
            AND wallet_quota_usd > 0
            AND wallet_quota_usd NOT IN (
                'NaN'::numeric,
                'Infinity'::numeric,
                '-Infinity'::numeric
            )
        )
    );

-- Preserve subscription history. The original official schema cascaded a
-- hard group delete into user_subscriptions, which can erase paid active rows.
-- Soft delete remains available after lifecycle guards confirm no live users
-- or fulfillable orders depend on the group.
ALTER TABLE user_subscriptions
    DROP CONSTRAINT IF EXISTS user_subscriptions_group_id_fkey;
ALTER TABLE user_subscriptions
    ADD CONSTRAINT user_subscriptions_group_id_fkey
    FOREIGN KEY (group_id) REFERENCES groups(id) ON DELETE RESTRICT;

ALTER TABLE subscription_plans
    DROP CONSTRAINT IF EXISTS subscription_plans_group_id_fkey;
ALTER TABLE subscription_plans
    ADD CONSTRAINT subscription_plans_group_id_fkey
    FOREIGN KEY (group_id) REFERENCES groups(id) ON DELETE RESTRICT;

ALTER TABLE subscription_plan_groups
    DROP CONSTRAINT IF EXISTS subscription_plan_groups_group_id_fkey;
ALTER TABLE subscription_plan_groups
    ADD CONSTRAINT subscription_plan_groups_group_id_fkey
    FOREIGN KEY (group_id) REFERENCES groups(id) ON DELETE RESTRICT;

CREATE OR REPLACE FUNCTION hfc_guard_monthly_payment_order_group_path()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    plan_kind text;
    expected_group_id bigint;
BEGIN
    IF NEW.order_type = 'subscription'
        AND hfc_payment_order_is_fulfillable(
            NEW.status,
            NEW.updated_at,
            NEW.paid_at,
            NEW.payment_trade_no
        ) THEN
        IF NEW.plan_id IS NULL THEN
            IF NEW.subscription_group_id IS NULL THEN
                RAISE EXCEPTION 'legacy group subscription order requires subscription_group_id'
                    USING ERRCODE = '23514';
            END IF;

            PERFORM 1
            FROM groups g
            WHERE g.id = NEW.subscription_group_id
              AND g.status = 'active'
              AND g.subscription_type = 'subscription'
              AND g.deleted_at IS NULL
            FOR SHARE;
            IF NOT FOUND THEN
                RAISE EXCEPTION 'legacy subscription order group is unavailable or not subscription type; group_id=%',
                    NEW.subscription_group_id
                    USING ERRCODE = '23514';
            END IF;
        ELSE
            SELECT plan_type, group_id
            INTO plan_kind, expected_group_id
            FROM subscription_plans
            WHERE id = NEW.plan_id
            FOR SHARE;

            IF NOT FOUND THEN
                RAISE EXCEPTION 'subscription order references missing plan; plan_id=%', NEW.plan_id
                    USING ERRCODE = '23514';
            END IF;

            IF plan_kind = 'subscription' THEN
                IF NEW.subscription_group_id IS NULL OR NEW.subscription_group_id IS DISTINCT FROM expected_group_id THEN
                    RAISE EXCEPTION 'monthly subscription order must use plan group; plan_id=%, expected=%, got=%',
                        NEW.plan_id, expected_group_id, NEW.subscription_group_id
                        USING ERRCODE = '23514';
                END IF;

                PERFORM 1
                FROM groups g
                WHERE g.id = expected_group_id
                  AND g.status = 'active'
                  AND g.subscription_type = 'subscription'
                  AND g.deleted_at IS NULL
                FOR SHARE;
                IF NOT FOUND THEN
                    RAISE EXCEPTION 'monthly subscription plan group is unavailable or not subscription type; plan_id=%, group_id=%',
                        NEW.plan_id, expected_group_id
                        USING ERRCODE = '23514';
                END IF;
            ELSIF plan_kind = 'credits' AND NEW.subscription_group_id IS NOT NULL THEN
                RAISE EXCEPTION 'credits order must use wallet path; plan_id=%, got group=%',
                    NEW.plan_id, NEW.subscription_group_id
                    USING ERRCODE = '23514';
            END IF;
        END IF;
    END IF;
    RETURN NEW;
END $$;

DROP TRIGGER IF EXISTS trg_hfc_guard_monthly_payment_order_group_path ON payment_orders;
CREATE TRIGGER trg_hfc_guard_monthly_payment_order_group_path
BEFORE INSERT OR UPDATE OF
    order_type,
    plan_id,
    subscription_group_id,
    status,
    updated_at,
    paid_at,
    payment_trade_no
ON payment_orders
FOR EACH ROW
EXECUTE FUNCTION hfc_guard_monthly_payment_order_group_path();

CREATE OR REPLACE FUNCTION hfc_guard_subscription_plan_group()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF NEW.plan_type = 'subscription' THEN
        IF NEW.group_id IS NULL OR NEW.wallet_quota_usd IS NOT NULL THEN
            RETURN NEW;
        END IF;

        PERFORM 1
        FROM groups g
        WHERE g.id = NEW.group_id
          AND g.status = 'active'
          AND g.subscription_type = 'subscription'
          AND g.deleted_at IS NULL
        FOR SHARE;
        IF NOT FOUND THEN
            RAISE EXCEPTION 'monthly plan group is unavailable or not subscription type; group_id=%',
                NEW.group_id
                USING ERRCODE = '23514';
        END IF;
    END IF;
    RETURN NEW;
END $$;

DROP TRIGGER IF EXISTS trg_hfc_guard_subscription_plan_group ON subscription_plans;
CREATE TRIGGER trg_hfc_guard_subscription_plan_group
BEFORE INSERT OR UPDATE OF plan_type, group_id, wallet_quota_usd
ON subscription_plans
FOR EACH ROW
EXECUTE FUNCTION hfc_guard_subscription_plan_group();

CREATE OR REPLACE FUNCTION hfc_guard_plan_shape_change_with_open_orders()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    fulfillment_shape_changed boolean;
BEGIN
    fulfillment_shape_changed := OLD.plan_type IS DISTINCT FROM NEW.plan_type
        OR OLD.group_id IS DISTINCT FROM NEW.group_id
        OR OLD.wallet_quota_usd IS DISTINCT FROM NEW.wallet_quota_usd;

    IF fulfillment_shape_changed AND EXISTS (
        SELECT 1
        FROM payment_orders po
        WHERE po.plan_id = OLD.id
          AND po.order_type = 'subscription'
          AND hfc_payment_order_is_fulfillable(
              po.status,
              po.updated_at,
              po.paid_at,
              po.payment_trade_no
          )
    ) THEN
        RAISE EXCEPTION 'plan fulfillment shape cannot change while orders are still fulfillable; plan_id=%',
            OLD.id
            USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END $$;

DROP TRIGGER IF EXISTS trg_hfc_guard_plan_shape_change_with_open_orders ON subscription_plans;
CREATE TRIGGER trg_hfc_guard_plan_shape_change_with_open_orders
BEFORE UPDATE OF plan_type, group_id, wallet_quota_usd
ON subscription_plans
FOR EACH ROW
EXECUTE FUNCTION hfc_guard_plan_shape_change_with_open_orders();

CREATE OR REPLACE FUNCTION hfc_guard_plan_delete_with_open_orders()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM payment_orders po
        WHERE po.plan_id = OLD.id
          AND po.order_type = 'subscription'
          AND hfc_payment_order_is_fulfillable(
              po.status,
              po.updated_at,
              po.paid_at,
              po.payment_trade_no
          )
    ) THEN
        RAISE EXCEPTION 'plan cannot be deleted while orders are still fulfillable; plan_id=%',
            OLD.id
            USING ERRCODE = '23514';
    END IF;
    RETURN OLD;
END $$;

DROP TRIGGER IF EXISTS trg_hfc_guard_plan_delete_with_open_orders ON subscription_plans;
CREATE TRIGGER trg_hfc_guard_plan_delete_with_open_orders
BEFORE DELETE
ON subscription_plans
FOR EACH ROW
EXECUTE FUNCTION hfc_guard_plan_delete_with_open_orders();

-- A monthly plan is only as valid as its group. Plan/order creation locks the
-- referenced group FOR SHARE; this deferred reverse guard then rejects a
-- concurrent or later disable, type change, soft delete, or hard delete. The
-- deferred check is important for the interleaving where a group mutation
-- waited on a just-created plan/order and must observe it after that commit.
CREATE OR REPLACE FUNCTION hfc_guard_subscription_group_lifecycle()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    target_group_id bigint;
    group_becomes_unavailable boolean;
BEGIN
    target_group_id := OLD.id;
    group_becomes_unavailable := CASE
        WHEN TG_OP = 'DELETE' THEN TRUE
        ELSE NEW.status IS DISTINCT FROM 'active'
            OR NEW.subscription_type IS DISTINCT FROM 'subscription'
            OR NEW.deleted_at IS NOT NULL
    END;

    IF NOT group_becomes_unavailable THEN
        RETURN NEW;
    END IF;

    IF EXISTS (
        SELECT 1
        FROM subscription_plans sp
        WHERE sp.plan_type = 'subscription'
          AND sp.group_id = target_group_id
          AND sp.for_sale = TRUE
    ) THEN
        RAISE EXCEPTION 'subscription group cannot be disabled, retyped, or deleted while referenced by a monthly plan; group_id=%',
            target_group_id
            USING ERRCODE = '23514';
    END IF;

    IF EXISTS (
        SELECT 1
        FROM user_subscriptions us
        WHERE us.group_id = target_group_id
          AND us.deleted_at IS NULL
          AND us.expires_at > NOW()
          AND us.status IN ('active', 'suspended')
    ) THEN
        RAISE EXCEPTION 'subscription group cannot be disabled, retyped, or deleted while active or resumable subscriptions depend on it; group_id=%',
            target_group_id
            USING ERRCODE = '23514';
    END IF;

    IF EXISTS (
        SELECT 1
        FROM payment_orders po
        WHERE po.order_type = 'subscription'
          AND po.plan_id IS NULL
          AND po.subscription_group_id = target_group_id
          AND hfc_payment_order_is_fulfillable(
              po.status,
              po.updated_at,
              po.paid_at,
              po.payment_trade_no
          )
    ) THEN
        RAISE EXCEPTION 'subscription group cannot be disabled, retyped, or deleted while a legacy order remains fulfillable; group_id=%',
            target_group_id
            USING ERRCODE = '23514';
    END IF;

    IF TG_OP = 'DELETE' THEN
        RETURN OLD;
    END IF;
    RETURN NEW;
END $$;

DROP TRIGGER IF EXISTS trg_hfc_guard_subscription_group_lifecycle ON groups;
CREATE CONSTRAINT TRIGGER trg_hfc_guard_subscription_group_lifecycle
AFTER UPDATE OF status, subscription_type, deleted_at OR DELETE
ON groups
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW
EXECUTE FUNCTION hfc_guard_subscription_group_lifecycle();

CREATE OR REPLACE FUNCTION hfc_guard_user_subscription_no_finite_wallet()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    new_is_finite_wallet boolean;
    old_is_finite_wallet boolean := FALSE;
BEGIN
    IF (NEW.wallet_balance_usd IS NULL) <> (NEW.wallet_initial_usd IS NULL) THEN
        RAISE EXCEPTION 'wallet balance and initial amount must be set together'
            USING ERRCODE = '23514';
    END IF;

    IF NEW.group_id IS NOT NULL
        AND (NEW.wallet_balance_usd IS NOT NULL OR NEW.wallet_initial_usd IS NOT NULL) THEN
        RAISE EXCEPTION 'group subscriptions cannot carry wallet balances'
            USING ERRCODE = '23514';
    END IF;

    IF NEW.group_id IS NOT NULL THEN
        PERFORM 1
        FROM groups g
        WHERE g.id = NEW.group_id
          AND g.status = 'active'
          AND g.subscription_type = 'subscription'
          AND g.deleted_at IS NULL
        FOR SHARE;
        IF NOT FOUND THEN
            RAISE EXCEPTION 'group subscription requires an active subscription group; group_id=%',
                NEW.group_id
                USING ERRCODE = '23514';
        END IF;
    END IF;

    new_is_finite_wallet := NEW.group_id IS NULL
        AND NEW.wallet_balance_usd IS NOT NULL
        AND NEW.expires_at < TIMESTAMPTZ '2099-12-30 23:59:59+00';

    IF TG_OP = 'UPDATE' THEN
        old_is_finite_wallet := OLD.group_id IS NULL
            AND OLD.wallet_balance_usd IS NOT NULL
            AND OLD.expires_at < TIMESTAMPTZ '2099-12-30 23:59:59+00';
    END IF;

    IF new_is_finite_wallet AND NOT old_is_finite_wallet THEN
        RAISE EXCEPTION 'finite wallet subscriptions are disabled; monthly plans must use group subscriptions'
            USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END $$;

DROP TRIGGER IF EXISTS trg_hfc_guard_user_subscription_no_finite_wallet ON user_subscriptions;
CREATE TRIGGER trg_hfc_guard_user_subscription_no_finite_wallet
BEFORE INSERT OR UPDATE OF group_id, wallet_balance_usd, wallet_initial_usd, expires_at, status, deleted_at
ON user_subscriptions
FOR EACH ROW
EXECUTE FUNCTION hfc_guard_user_subscription_no_finite_wallet();
