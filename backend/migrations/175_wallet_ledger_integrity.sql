-- Make the wallet ledger authoritative without changing any cached balance.
-- Historical wallet creation wrote wallet_initial_usd/wallet_balance_usd but
-- omitted reason=activation. Backfill only rows whose observed gap is exactly
-- explained by the opening amount; otherwise abort for manual review.

-- Hold a stable snapshot until the backfill, preflight, and commit-time guards
-- are installed. Without this lock an old application instance could create a
-- wallet after the scan but before the guard exists, permanently omitting its
-- activation row. The migration runner's lock timeout makes deployment retry
-- instead of silently accepting that race.
LOCK TABLE user_subscriptions, subscription_wallet_ledger
    IN SHARE ROW EXCLUSIVE MODE;

WITH wallet_ledger AS (
    SELECT
        us.id AS subscription_id,
        us.wallet_initial_usd,
        us.wallet_balance_usd,
        us.created_at AS subscription_created_at,
        COALESCE(SUM(l.delta_usd), 0) AS ledger_sum,
        COALESCE(SUM(l.delta_usd) FILTER (WHERE l.reason = 'topup'), 0) AS topup_sum,
        BOOL_OR(l.reason = 'activation') AS has_activation
    FROM user_subscriptions us
    LEFT JOIN subscription_wallet_ledger l ON l.subscription_id = us.id
    WHERE us.wallet_balance_usd IS NOT NULL
      AND us.wallet_initial_usd IS NOT NULL
    GROUP BY us.id, us.wallet_initial_usd, us.wallet_balance_usd, us.created_at
), safe_backfill AS (
    SELECT
        subscription_id,
        (wallet_initial_usd - topup_sum) AS activation_delta,
        (wallet_balance_usd - ledger_sum) AS observed_gap,
        subscription_created_at
    FROM wallet_ledger
    WHERE NOT COALESCE(has_activation, FALSE)
)
INSERT INTO subscription_wallet_ledger
    (subscription_id, delta_usd, balance_after, reason, notes, created_at)
SELECT
    subscription_id,
    observed_gap,
    observed_gap,
    'activation',
    'historical activation baseline (migration 175)',
    subscription_created_at
FROM safe_backfill
WHERE activation_delta >= 0
  AND observed_gap >= 0
  AND ABS(activation_delta - observed_gap) <= 0.01;

DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM user_subscriptions
        WHERE wallet_balance_usd IS NOT NULL
          AND (
              wallet_initial_usd IS NULL
              OR wallet_initial_usd < 0
              OR wallet_initial_usd IN (
                  'NaN'::numeric,
                  'Infinity'::numeric,
                  '-Infinity'::numeric
              )
              OR wallet_balance_usd IN (
                  'NaN'::numeric,
                  'Infinity'::numeric,
                  '-Infinity'::numeric
              )
          )
    ) THEN
        RAISE EXCEPTION 'wallet migration blocked: wallet opening amounts must be paired, non-negative, and finite';
    END IF;

    IF EXISTS (
        SELECT 1
        FROM subscription_wallet_ledger
        WHERE delta_usd IN (
                  'NaN'::numeric,
                  'Infinity'::numeric,
                  '-Infinity'::numeric
              )
           OR balance_after IN (
                  'NaN'::numeric,
                  'Infinity'::numeric,
                  '-Infinity'::numeric
              )
    ) THEN
        RAISE EXCEPTION 'wallet migration blocked: ledger amounts must be finite';
    END IF;

    IF EXISTS (
        SELECT 1
        FROM subscription_wallet_ledger
        WHERE reason = 'activation'
        GROUP BY subscription_id
        HAVING COUNT(*) > 1
    ) THEN
        RAISE EXCEPTION 'wallet migration blocked: duplicate activation ledger rows require review';
    END IF;

    IF EXISTS (
        SELECT 1
        FROM user_subscriptions us
        WHERE us.wallet_balance_usd IS NOT NULL
          AND NOT EXISTS (
              SELECT 1
              FROM subscription_wallet_ledger l
              WHERE l.subscription_id = us.id
                AND l.reason = 'activation'
          )
    ) THEN
        RAISE EXCEPTION 'wallet migration blocked: activation backfill is not safely explainable';
    END IF;

    IF EXISTS (
        SELECT 1
        FROM user_subscriptions us
        LEFT JOIN subscription_wallet_ledger l ON l.subscription_id = us.id
        WHERE us.wallet_balance_usd IS NOT NULL
        GROUP BY us.id, us.wallet_balance_usd
        HAVING us.wallet_balance_usd IS DISTINCT FROM COALESCE(SUM(l.delta_usd), 0)
    ) THEN
        RAISE EXCEPTION 'wallet migration blocked: cached balance and ledger still drift';
    END IF;

    IF EXISTS (
        SELECT 1
        FROM user_subscriptions
        WHERE wallet_balance_usd IS NOT NULL
          AND status = 'active'
          AND deleted_at IS NULL
          AND expires_at >= TIMESTAMPTZ '2099-12-30 23:59:59+00'
        GROUP BY user_id
        HAVING COUNT(*) > 1
    ) THEN
        RAISE EXCEPTION 'wallet migration blocked: user has multiple active permanent credits wallets';
    END IF;
END $$;

ALTER TABLE subscription_wallet_ledger
    DROP CONSTRAINT IF EXISTS chk_wallet_ledger_amounts_finite;
ALTER TABLE subscription_wallet_ledger
    ADD CONSTRAINT chk_wallet_ledger_amounts_finite CHECK (
        delta_usd NOT IN (
            'NaN'::numeric,
            'Infinity'::numeric,
            '-Infinity'::numeric
        )
        AND balance_after NOT IN (
            'NaN'::numeric,
            'Infinity'::numeric,
            '-Infinity'::numeric
        )
    );

-- Prevent a syntactically valid reason from disguising the direction of a
-- financial movement. Refunds and admin adjustments are intentionally
-- bidirectional; all automatic lifecycle reasons have one fixed direction.
-- A zero activation remains valid for historical wallets that were funded
-- entirely by top-ups before an opening ledger row existed.
ALTER TABLE subscription_wallet_ledger
    DROP CONSTRAINT IF EXISTS chk_wallet_ledger_reason_direction;
ALTER TABLE subscription_wallet_ledger
    ADD CONSTRAINT chk_wallet_ledger_reason_direction CHECK (
        (reason = 'activation' AND delta_usd >= 0)
        OR (reason = 'topup' AND delta_usd > 0)
        OR (reason = 'usage' AND delta_usd < 0)
        OR (reason = 'expiration' AND delta_usd <= 0)
        OR (reason IN ('refund', 'adjustment') AND delta_usd <> 0)
    );

-- A financial ledger must survive deletion of its cached parent row. The
-- original schema used ON DELETE CASCADE, which could erase the only audit
-- trail together with user_subscriptions.
ALTER TABLE subscription_wallet_ledger
    DROP CONSTRAINT IF EXISTS subscription_wallet_ledger_subscription_id_fkey;
ALTER TABLE subscription_wallet_ledger
    ADD CONSTRAINT subscription_wallet_ledger_subscription_id_fkey
    FOREIGN KEY (subscription_id) REFERENCES user_subscriptions(id) ON DELETE RESTRICT;

CREATE OR REPLACE FUNCTION hfc_reject_wallet_ledger_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    RAISE EXCEPTION 'wallet ledger is append-only; UPDATE and DELETE are forbidden; ledger_id=%',
        OLD.id
        USING ERRCODE = '23514';
END $$;

DROP TRIGGER IF EXISTS trg_hfc_reject_wallet_ledger_mutation ON subscription_wallet_ledger;
CREATE TRIGGER trg_hfc_reject_wallet_ledger_mutation
BEFORE UPDATE OR DELETE
ON subscription_wallet_ledger
FOR EACH ROW
EXECUTE FUNCTION hfc_reject_wallet_ledger_mutation();

-- Every ledger append locks its parent wallet row before changing the
-- authoritative ledger. This serializes cross-table integrity checks even when a
-- caller bypasses the repository's normal SELECT ... FOR UPDATE path.
CREATE OR REPLACE FUNCTION hfc_lock_wallet_ledger_parent()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    PERFORM 1
    FROM user_subscriptions us
    WHERE us.id = NEW.subscription_id
      AND us.wallet_balance_usd IS NOT NULL
    FOR UPDATE;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'wallet ledger append requires a wallet subscription; subscription_id=%',
            NEW.subscription_id
            USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END $$;

DROP TRIGGER IF EXISTS trg_hfc_lock_wallet_ledger_parent ON subscription_wallet_ledger;
CREATE TRIGGER trg_hfc_lock_wallet_ledger_parent
BEFORE INSERT
ON subscription_wallet_ledger
FOR EACH ROW
EXECUTE FUNCTION hfc_lock_wallet_ledger_parent();

-- Serialize changes that enter or leave the one-active-permanent-wallet set.
-- Normal balance deductions do not take this user-row lock because the UPDATE
-- trigger's WHEN clause compares membership, not the numeric balance value.
CREATE OR REPLACE FUNCTION hfc_lock_credits_wallet_owner()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    target_user_id bigint;
BEGIN
    FOR target_user_id IN
        SELECT u.id
        FROM users u
        WHERE u.id IN (
            CASE WHEN TG_OP = 'INSERT' THEN NEW.user_id ELSE OLD.user_id END,
            NEW.user_id
        )
        ORDER BY u.id
        FOR UPDATE
    LOOP
        NULL;
    END LOOP;
    RETURN NEW;
END $$;

DROP TRIGGER IF EXISTS trg_hfc_lock_credits_wallet_owner_insert ON user_subscriptions;
CREATE TRIGGER trg_hfc_lock_credits_wallet_owner_insert
BEFORE INSERT
ON user_subscriptions
FOR EACH ROW
WHEN (
    NEW.wallet_balance_usd IS NOT NULL
    AND NEW.status = 'active'
    AND NEW.deleted_at IS NULL
    AND NEW.expires_at >= TIMESTAMPTZ '2099-12-30 23:59:59+00'
)
EXECUTE FUNCTION hfc_lock_credits_wallet_owner();

DROP TRIGGER IF EXISTS trg_hfc_lock_credits_wallet_owner_update ON user_subscriptions;
CREATE TRIGGER trg_hfc_lock_credits_wallet_owner_update
BEFORE UPDATE OF user_id, wallet_balance_usd, status, deleted_at, expires_at
ON user_subscriptions
FOR EACH ROW
WHEN (
    ROW(
        OLD.user_id,
        OLD.wallet_balance_usd IS NOT NULL,
        OLD.status = 'active',
        OLD.deleted_at IS NULL,
        OLD.expires_at >= TIMESTAMPTZ '2099-12-30 23:59:59+00'
    ) IS DISTINCT FROM ROW(
        NEW.user_id,
        NEW.wallet_balance_usd IS NOT NULL,
        NEW.status = 'active',
        NEW.deleted_at IS NULL,
        NEW.expires_at >= TIMESTAMPTZ '2099-12-30 23:59:59+00'
    )
)
EXECUTE FUNCTION hfc_lock_credits_wallet_owner();

CREATE OR REPLACE FUNCTION hfc_assert_wallet_ledger_integrity(target_subscription_id bigint)
RETURNS void
LANGUAGE plpgsql
AS $$
DECLARE
    target_user_id bigint;
    cached_balance numeric;
    opening_amount numeric;
    wallet_status text;
    wallet_deleted_at timestamptz;
    wallet_expires_at timestamptz;
    activation_count bigint;
    ledger_total numeric;
    has_non_finite_amount boolean;
BEGIN
    SELECT
        us.user_id,
        us.wallet_balance_usd,
        us.wallet_initial_usd,
        us.status,
        us.deleted_at,
        us.expires_at
    INTO
        target_user_id,
        cached_balance,
        opening_amount,
        wallet_status,
        wallet_deleted_at,
        wallet_expires_at
    FROM user_subscriptions us
    WHERE us.id = target_subscription_id
    FOR UPDATE;

    IF NOT FOUND OR cached_balance IS NULL THEN
        RETURN;
    END IF;

    IF opening_amount IS NULL
        OR opening_amount < 0
        OR opening_amount IN ('NaN'::numeric, 'Infinity'::numeric, '-Infinity'::numeric)
        OR cached_balance IN ('NaN'::numeric, 'Infinity'::numeric, '-Infinity'::numeric) THEN
        RAISE EXCEPTION 'wallet integrity violation: opening amount must be paired, non-negative, and finite; subscription_id=%',
            target_subscription_id
            USING ERRCODE = '23514';
    END IF;

    SELECT
        COUNT(*) FILTER (WHERE l.reason = 'activation'),
        COALESCE(SUM(l.delta_usd), 0),
        COALESCE(BOOL_OR(
            l.delta_usd IN ('NaN'::numeric, 'Infinity'::numeric, '-Infinity'::numeric)
            OR l.balance_after IN ('NaN'::numeric, 'Infinity'::numeric, '-Infinity'::numeric)
        ), FALSE)
    INTO activation_count, ledger_total, has_non_finite_amount
    FROM subscription_wallet_ledger l
    WHERE l.subscription_id = target_subscription_id;

    IF has_non_finite_amount THEN
        RAISE EXCEPTION 'wallet integrity violation: ledger amount must be finite; subscription_id=%',
            target_subscription_id
            USING ERRCODE = '23514';
    END IF;

    IF activation_count <> 1 THEN
        RAISE EXCEPTION 'wallet integrity violation: exactly one activation ledger row is required; subscription_id=%, count=%',
            target_subscription_id, activation_count
            USING ERRCODE = '23514';
    END IF;

    IF ledger_total IS DISTINCT FROM cached_balance THEN
        RAISE EXCEPTION 'wallet integrity violation: cached balance must equal ledger sum; subscription_id=%, cached=%, ledger=%',
            target_subscription_id, cached_balance, ledger_total
            USING ERRCODE = '23514';
    END IF;

    IF wallet_deleted_at IS NULL
        AND wallet_status = 'active'
        AND wallet_expires_at >= TIMESTAMPTZ '2099-12-30 23:59:59+00'
        AND (
            SELECT COUNT(*)
            FROM user_subscriptions other_wallet
            WHERE other_wallet.user_id = target_user_id
              AND other_wallet.wallet_balance_usd IS NOT NULL
              AND other_wallet.status = 'active'
              AND other_wallet.deleted_at IS NULL
              AND other_wallet.expires_at >= TIMESTAMPTZ '2099-12-30 23:59:59+00'
        ) <> 1 THEN
        RAISE EXCEPTION 'wallet integrity violation: user must have at most one active permanent credits wallet; user_id=%',
            target_user_id
            USING ERRCODE = '23514';
    END IF;
END $$;

CREATE OR REPLACE FUNCTION hfc_check_wallet_subscription_integrity()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    PERFORM hfc_assert_wallet_ledger_integrity(NEW.id);
    RETURN NEW;
END $$;

DROP TRIGGER IF EXISTS trg_hfc_check_wallet_subscription_integrity ON user_subscriptions;
CREATE CONSTRAINT TRIGGER trg_hfc_check_wallet_subscription_integrity
AFTER INSERT OR UPDATE OF
    user_id,
    group_id,
    wallet_balance_usd,
    wallet_initial_usd,
    status,
    deleted_at,
    expires_at
ON user_subscriptions
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW
EXECUTE FUNCTION hfc_check_wallet_subscription_integrity();

CREATE OR REPLACE FUNCTION hfc_check_wallet_ledger_integrity()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    PERFORM hfc_assert_wallet_ledger_integrity(NEW.subscription_id);
    RETURN NEW;
END $$;

DROP TRIGGER IF EXISTS trg_hfc_check_wallet_ledger_integrity ON subscription_wallet_ledger;
CREATE CONSTRAINT TRIGGER trg_hfc_check_wallet_ledger_integrity
AFTER INSERT
ON subscription_wallet_ledger
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW
EXECUTE FUNCTION hfc_check_wallet_ledger_integrity();

COMMENT ON TABLE subscription_wallet_ledger IS
    'Append-only authoritative wallet ledger. SUM(delta_usd) must equal user_subscriptions.wallet_balance_usd; activation is included in that sum.';

-- Migration 178 builds redundant unique indexes online after this
-- transactional guard commits. Keeping the large index scans out of this
-- transaction avoids a long blocking index build, while the triggers preserve
-- correctness throughout the 175 -> 178 deployment window.
