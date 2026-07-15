-- Persist immutable fulfillment facts for every new subscription/credits order.
-- Historical orders predate immutable snapshots and remain explicit legacy
-- records: the application must refuse automated fulfillment/refund when their
-- evidence is missing instead of reconstructing mutable product facts.

-- Capture the legacy boundary and install the deferred order trigger under one
-- write-stable transaction. An older application instance that writes after
-- this migration commits receives an ID above the cutover and cannot commit a
-- plan order without a snapshot.
LOCK TABLE payment_orders IN SHARE ROW EXCLUSIVE MODE;

CREATE OR REPLACE FUNCTION hfc_plan_fulfillment_snapshot_shape_valid(candidate jsonb)
RETURNS boolean
LANGUAGE plpgsql
IMMUTABLE
STRICT
AS $$
DECLARE
    schema_version integer;
    snapshot_plan_id bigint;
    snapshot_plan_type text;
    snapshot_plan_price numeric;
    snapshot_group_id bigint;
    snapshot_days integer;
    snapshot_wallet_quota numeric;
    covered_group jsonb;
    covered_group_id bigint;
    locked_rate record;
    locked_rate_value numeric;
BEGIN
    IF jsonb_typeof(candidate) <> 'object'
       OR NOT candidate ?& ARRAY[
            'schema_version', 'plan_id', 'plan_type', 'plan_price',
            'group_id', 'subscription_days', 'wallet_quota_usd',
            'covered_group_ids', 'locked_rates'
       ]
       OR jsonb_typeof(candidate->'covered_group_ids') <> 'array'
       OR jsonb_typeof(candidate->'locked_rates') <> 'object' THEN
        RETURN FALSE;
    END IF;

    schema_version := (candidate->>'schema_version')::integer;
    snapshot_plan_id := (candidate->>'plan_id')::bigint;
    snapshot_plan_type := candidate->>'plan_type';
    snapshot_plan_price := (candidate->>'plan_price')::numeric;
    snapshot_days := (candidate->>'subscription_days')::integer;

    IF schema_version IS DISTINCT FROM 1
       OR snapshot_plan_id IS NULL
       OR snapshot_plan_id <= 0
       OR snapshot_plan_type IS NULL
       OR snapshot_plan_type NOT IN ('subscription', 'credits')
       OR snapshot_plan_price IS NULL
       OR snapshot_plan_price <= 0
       OR snapshot_plan_price IN ('NaN'::numeric, 'Infinity'::numeric, '-Infinity'::numeric)
       OR snapshot_days IS NULL
       OR snapshot_days <= 0
       OR snapshot_days > 36500 THEN
        RETURN FALSE;
    END IF;

    FOR covered_group IN
        SELECT value FROM jsonb_array_elements(candidate->'covered_group_ids')
    LOOP
        IF jsonb_typeof(covered_group) <> 'number' THEN
            RETURN FALSE;
        END IF;
        covered_group_id := (covered_group #>> '{}')::bigint;
        IF covered_group_id <= 0 THEN
            RETURN FALSE;
        END IF;
    END LOOP;

    IF (
        SELECT COUNT(*) <> COUNT(DISTINCT (value #>> '{}')::bigint)
        FROM jsonb_array_elements(candidate->'covered_group_ids')
    ) THEN
        RETURN FALSE;
    END IF;

    FOR locked_rate IN
        SELECT key, value FROM jsonb_each(candidate->'locked_rates')
    LOOP
        IF locked_rate.key::bigint <= 0 OR jsonb_typeof(locked_rate.value) <> 'number' THEN
            RETURN FALSE;
        END IF;
        locked_rate_value := (locked_rate.value #>> '{}')::numeric;
        IF locked_rate_value < 0
           OR locked_rate_value IN ('NaN'::numeric, 'Infinity'::numeric, '-Infinity'::numeric) THEN
            RETURN FALSE;
        END IF;
    END LOOP;

    IF snapshot_plan_type = 'subscription' THEN
        IF candidate->'group_id' = 'null'::jsonb
           OR candidate->'wallet_quota_usd' <> 'null'::jsonb THEN
            RETURN FALSE;
        END IF;
        snapshot_group_id := (candidate->>'group_id')::bigint;
        IF snapshot_group_id IS NULL
           OR snapshot_group_id <= 0
           OR NOT candidate->'covered_group_ids' @> jsonb_build_array(snapshot_group_id) THEN
            RETURN FALSE;
        END IF;
    ELSE
        IF candidate->'group_id' <> 'null'::jsonb
           OR candidate->'wallet_quota_usd' = 'null'::jsonb THEN
            RETURN FALSE;
        END IF;
        snapshot_wallet_quota := (candidate->>'wallet_quota_usd')::numeric;
        IF snapshot_wallet_quota IS NULL
           OR snapshot_wallet_quota <= 0
           OR snapshot_wallet_quota IN ('NaN'::numeric, 'Infinity'::numeric, '-Infinity'::numeric) THEN
            RETURN FALSE;
        END IF;
    END IF;

    RETURN TRUE;
EXCEPTION
    WHEN data_exception OR invalid_text_representation OR numeric_value_out_of_range THEN
        RETURN FALSE;
END $$;

CREATE TABLE IF NOT EXISTS subscription_plan_snapshot_cutovers (
    singleton BOOLEAN PRIMARY KEY DEFAULT TRUE CHECK (singleton),
    max_legacy_payment_order_id BIGINT NOT NULL CHECK (max_legacy_payment_order_id >= 0),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

INSERT INTO subscription_plan_snapshot_cutovers (singleton, max_legacy_payment_order_id)
SELECT TRUE, COALESCE(MAX(id), 0)
FROM payment_orders
ON CONFLICT (singleton) DO NOTHING;

CREATE OR REPLACE FUNCTION hfc_reject_plan_snapshot_cutover_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    RAISE EXCEPTION 'plan snapshot cutover is immutable'
        USING ERRCODE = '23514';
END $$;

DROP TRIGGER IF EXISTS trg_hfc_reject_plan_snapshot_cutover_mutation
    ON subscription_plan_snapshot_cutovers;
CREATE TRIGGER trg_hfc_reject_plan_snapshot_cutover_mutation
BEFORE UPDATE OR DELETE ON subscription_plan_snapshot_cutovers
FOR EACH ROW
EXECUTE FUNCTION hfc_reject_plan_snapshot_cutover_mutation();

CREATE TABLE IF NOT EXISTS subscription_plan_fulfillment_snapshots (
    payment_order_id BIGINT PRIMARY KEY,
    user_id BIGINT NOT NULL,
    source_plan_id BIGINT NOT NULL,
    user_subscription_id BIGINT,
    snapshot JSONB NOT NULL,
    grant_starts_at TIMESTAMPTZ,
    grant_expires_at TIMESTAMPTZ,
    attached_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT fk_plan_fulfillment_snapshot_order
        FOREIGN KEY (payment_order_id)
        REFERENCES payment_orders(id)
        ON DELETE RESTRICT,
    CONSTRAINT fk_plan_fulfillment_snapshot_user
        FOREIGN KEY (user_id)
        REFERENCES users(id)
        ON DELETE RESTRICT,
    CONSTRAINT fk_plan_fulfillment_snapshot_subscription
        FOREIGN KEY (user_subscription_id)
        REFERENCES user_subscriptions(id)
        ON DELETE RESTRICT,
    CONSTRAINT chk_plan_fulfillment_snapshot_shape CHECK (
        hfc_plan_fulfillment_snapshot_shape_valid(snapshot)
    ),
    CONSTRAINT chk_plan_fulfillment_snapshot_source_plan CHECK (
        source_plan_id = (snapshot->>'plan_id')::bigint
    ),
    CONSTRAINT chk_plan_fulfillment_snapshot_grant_window CHECK (
        (
            user_subscription_id IS NULL
            AND grant_starts_at IS NULL
            AND grant_expires_at IS NULL
            AND attached_at IS NULL
        )
        OR
        (
            user_subscription_id IS NOT NULL
            AND grant_starts_at IS NOT NULL
            AND grant_expires_at IS NOT NULL
            AND grant_expires_at > grant_starts_at
            AND attached_at IS NOT NULL
        )
    )
);

CREATE INDEX IF NOT EXISTS idx_plan_fulfillment_snapshot_user
    ON subscription_plan_fulfillment_snapshots(user_id);

CREATE INDEX IF NOT EXISTS idx_plan_fulfillment_snapshot_subscription_grant
    ON subscription_plan_fulfillment_snapshots(user_subscription_id, grant_expires_at)
    WHERE user_subscription_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_plan_fulfillment_snapshot_covered_groups
    ON subscription_plan_fulfillment_snapshots
    USING GIN ((snapshot->'covered_group_ids'));

CREATE OR REPLACE FUNCTION hfc_reject_plan_fulfillment_snapshot_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'plan fulfillment snapshots cannot be deleted'
            USING ERRCODE = '23514';
    END IF;

    IF NEW.payment_order_id IS DISTINCT FROM OLD.payment_order_id
       OR NEW.user_id IS DISTINCT FROM OLD.user_id
       OR NEW.source_plan_id IS DISTINCT FROM OLD.source_plan_id
       OR NEW.snapshot IS DISTINCT FROM OLD.snapshot
       OR NEW.created_at IS DISTINCT FROM OLD.created_at THEN
        RAISE EXCEPTION 'plan fulfillment snapshot immutable fields cannot be changed'
            USING ERRCODE = '23514';
    END IF;

    IF OLD.user_subscription_id IS NOT NULL
       AND (
            NEW.user_subscription_id IS DISTINCT FROM OLD.user_subscription_id
            OR NEW.grant_starts_at IS DISTINCT FROM OLD.grant_starts_at
            OR NEW.grant_expires_at IS DISTINCT FROM OLD.grant_expires_at
            OR NEW.attached_at IS DISTINCT FROM OLD.attached_at
       ) THEN
        RAISE EXCEPTION 'plan fulfillment snapshot grant attachment is immutable'
            USING ERRCODE = '23514';
    END IF;

    NEW.updated_at = NOW();
    RETURN NEW;
END $$;

DROP TRIGGER IF EXISTS trg_hfc_reject_plan_fulfillment_snapshot_mutation
    ON subscription_plan_fulfillment_snapshots;
CREATE TRIGGER trg_hfc_reject_plan_fulfillment_snapshot_mutation
BEFORE UPDATE OR DELETE ON subscription_plan_fulfillment_snapshots
FOR EACH ROW
EXECUTE FUNCTION hfc_reject_plan_fulfillment_snapshot_mutation();

CREATE OR REPLACE FUNCTION hfc_assert_subscription_plan_snapshot(target_order_id bigint)
RETURNS void
LANGUAGE plpgsql
AS $$
DECLARE
    source_order payment_orders%ROWTYPE;
    source_snapshot subscription_plan_fulfillment_snapshots%ROWTYPE;
    snapshot_found boolean;
    snapshot_cutover bigint;
    snapshot_plan_type text;
    snapshot_plan_id bigint;
    snapshot_group_id bigint;
    snapshot_days integer;
    snapshot_price numeric;
    subscription_user_id bigint;
BEGIN
    SELECT *
    INTO source_order
    FROM payment_orders
    WHERE id = target_order_id
    FOR UPDATE;

    IF NOT FOUND THEN
        RAISE EXCEPTION 'plan snapshot violation: payment order % is missing', target_order_id
            USING ERRCODE = '23503';
    END IF;

    SELECT *
    INTO source_snapshot
    FROM subscription_plan_fulfillment_snapshots
    WHERE payment_order_id = target_order_id;
    snapshot_found := FOUND;

    IF source_order.order_type IS DISTINCT FROM 'subscription'
       OR source_order.plan_id IS NULL THEN
        IF snapshot_found THEN
            RAISE EXCEPTION 'plan snapshot violation: order % is not a plan subscription order', target_order_id
                USING ERRCODE = '23514';
        END IF;
        RETURN;
    END IF;

    IF NOT snapshot_found THEN
        SELECT max_legacy_payment_order_id
        INTO snapshot_cutover
        FROM subscription_plan_snapshot_cutovers
        WHERE singleton = TRUE;

        IF snapshot_cutover IS NULL THEN
            RAISE EXCEPTION 'plan snapshot violation: immutable cutover marker is missing'
                USING ERRCODE = '23514';
        END IF;
        IF target_order_id <= snapshot_cutover THEN
            RETURN;
        END IF;
        RAISE EXCEPTION 'plan snapshot violation: subscription order % has no immutable fulfillment snapshot', target_order_id
            USING ERRCODE = '23514';
    END IF;

    snapshot_plan_type := source_snapshot.snapshot->>'plan_type';
    snapshot_plan_id := (source_snapshot.snapshot->>'plan_id')::bigint;
    snapshot_days := (source_snapshot.snapshot->>'subscription_days')::integer;
    snapshot_price := (source_snapshot.snapshot->>'plan_price')::numeric;

    IF source_snapshot.user_id IS DISTINCT FROM source_order.user_id
       OR source_snapshot.source_plan_id IS DISTINCT FROM source_order.plan_id
       OR snapshot_plan_id IS DISTINCT FROM source_order.plan_id
       OR snapshot_days IS DISTINCT FROM source_order.subscription_days
       OR snapshot_price IS DISTINCT FROM source_order.amount::numeric THEN
        RAISE EXCEPTION 'plan snapshot violation: order % snapshot does not match immutable order facts', target_order_id
            USING ERRCODE = '23514';
    END IF;

    IF snapshot_plan_type = 'subscription' THEN
        snapshot_group_id := (source_snapshot.snapshot->>'group_id')::bigint;
        IF snapshot_group_id IS DISTINCT FROM source_order.subscription_group_id THEN
            RAISE EXCEPTION 'plan snapshot violation: order % monthly group does not match snapshot', target_order_id
                USING ERRCODE = '23514';
        END IF;
    ELSIF snapshot_plan_type = 'credits' THEN
        IF source_order.subscription_group_id IS NOT NULL THEN
            RAISE EXCEPTION 'plan snapshot violation: order % credits snapshot has a monthly group', target_order_id
                USING ERRCODE = '23514';
        END IF;
    ELSE
        RAISE EXCEPTION 'plan snapshot violation: order % has invalid snapshot plan type', target_order_id
            USING ERRCODE = '23514';
    END IF;

    IF source_snapshot.user_subscription_id IS NOT NULL THEN
        SELECT user_id
        INTO subscription_user_id
        FROM user_subscriptions
        WHERE id = source_snapshot.user_subscription_id;
        IF NOT FOUND OR subscription_user_id IS DISTINCT FROM source_order.user_id THEN
            RAISE EXCEPTION 'plan snapshot violation: order % grant belongs to another user', target_order_id
                USING ERRCODE = '23514';
        END IF;
    END IF;
END $$;

CREATE OR REPLACE FUNCTION hfc_check_subscription_plan_snapshot_from_order()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF TG_OP = 'INSERT' THEN
        IF NEW.order_type = 'subscription' AND NEW.plan_id IS NOT NULL THEN
            PERFORM hfc_assert_subscription_plan_snapshot(NEW.id);
        END IF;
    ELSIF (NEW.order_type = 'subscription' AND NEW.plan_id IS NOT NULL)
       OR (OLD.order_type = 'subscription' AND OLD.plan_id IS NOT NULL) THEN
        PERFORM hfc_assert_subscription_plan_snapshot(NEW.id);
    END IF;
    RETURN NEW;
END $$;

DROP TRIGGER IF EXISTS trg_hfc_check_subscription_plan_snapshot_from_order
    ON payment_orders;
CREATE CONSTRAINT TRIGGER trg_hfc_check_subscription_plan_snapshot_from_order
AFTER INSERT OR UPDATE OF user_id, order_type, plan_id, subscription_group_id, subscription_days, amount, status
ON payment_orders
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW
EXECUTE FUNCTION hfc_check_subscription_plan_snapshot_from_order();

CREATE OR REPLACE FUNCTION hfc_check_subscription_plan_snapshot_from_snapshot()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    PERFORM hfc_assert_subscription_plan_snapshot(NEW.payment_order_id);
    RETURN NEW;
END $$;

DROP TRIGGER IF EXISTS trg_hfc_check_subscription_plan_snapshot_from_snapshot
    ON subscription_plan_fulfillment_snapshots;
CREATE CONSTRAINT TRIGGER trg_hfc_check_subscription_plan_snapshot_from_snapshot
AFTER INSERT OR UPDATE
ON subscription_plan_fulfillment_snapshots
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW
EXECUTE FUNCTION hfc_check_subscription_plan_snapshot_from_snapshot();

-- Migration 176 had to freeze mutable plan facts while any fulfillable order
-- still depended on them. A validated immutable snapshot removes that
-- dependency. Keep the guard for legacy unsnapshotted orders, but allow plan
-- administration for orders whose complete fulfillment evidence is frozen.
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
        -- The deferred snapshot trigger validates all immutable order facts.
        -- Do not consult a mutable (or subsequently deleted) plan once this
        -- order has a validated snapshot.
        IF NEW.plan_id IS NOT NULL AND EXISTS (
            SELECT 1
            FROM subscription_plan_fulfillment_snapshots snapshot
            WHERE snapshot.payment_order_id = NEW.id
        ) THEN
            RETURN NEW;
        END IF;

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
                RAISE EXCEPTION 'legacy unsnapshotted subscription order references missing plan; plan_id=%', NEW.plan_id
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
          AND NOT EXISTS (
              SELECT 1
              FROM subscription_plan_fulfillment_snapshots snapshot
              WHERE snapshot.payment_order_id = po.id
          )
    ) THEN
        RAISE EXCEPTION 'plan fulfillment shape cannot change while legacy unsnapshotted orders are still fulfillable; plan_id=%',
            OLD.id
            USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END $$;

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
          AND NOT EXISTS (
              SELECT 1
              FROM subscription_plan_fulfillment_snapshots snapshot
              WHERE snapshot.payment_order_id = po.id
          )
    ) THEN
        RAISE EXCEPTION 'plan cannot be deleted while legacy unsnapshotted orders are still fulfillable; plan_id=%',
            OLD.id
            USING ERRCODE = '23514';
    END IF;
    RETURN OLD;
END $$;

COMMENT ON TABLE subscription_plan_snapshot_cutovers IS
    'immutable payment-order ID boundary; lower IDs are legacy and never auto-backfilled from mutable plan data';
COMMENT ON TABLE subscription_plan_fulfillment_snapshots IS
    'immutable fulfillment snapshot for subscription and credits payment orders';
