-- Bind every new paid credits activation/top-up and refund reversal to the
-- immutable payment order that caused it. Historical ledger rows remain NULL:
-- guessing their source from mutable plan values or free-form notes is unsafe,
-- so refunds for those orders intentionally fail closed for manual review.

ALTER TABLE subscription_wallet_ledger
    ADD COLUMN IF NOT EXISTS payment_order_id BIGINT;

ALTER TABLE subscription_wallet_ledger
    DROP CONSTRAINT IF EXISTS fk_wallet_ledger_payment_order;
ALTER TABLE subscription_wallet_ledger
    ADD CONSTRAINT fk_wallet_ledger_payment_order
    FOREIGN KEY (payment_order_id)
    REFERENCES payment_orders(id)
    ON DELETE RESTRICT
    NOT VALID;
ALTER TABLE subscription_wallet_ledger
    VALIDATE CONSTRAINT fk_wallet_ledger_payment_order;

ALTER TABLE subscription_wallet_ledger
    DROP CONSTRAINT IF EXISTS chk_wallet_ledger_payment_source_shape;
ALTER TABLE subscription_wallet_ledger
    ADD CONSTRAINT chk_wallet_ledger_payment_source_shape CHECK (
        payment_order_id IS NULL
        OR (reason IN ('activation', 'topup') AND delta_usd > 0)
        OR (reason = 'refund' AND delta_usd <> 0)
    );

-- One payment credits the wallet once. The matching refund debit and an
-- optional pre-gateway compensation each also occur at most once.
CREATE UNIQUE INDEX IF NOT EXISTS idx_wallet_ledger_one_payment_credit
    ON subscription_wallet_ledger(payment_order_id)
    WHERE payment_order_id IS NOT NULL
      AND reason IN ('activation', 'topup')
      AND delta_usd > 0;

CREATE UNIQUE INDEX IF NOT EXISTS idx_wallet_ledger_one_payment_refund_debit
    ON subscription_wallet_ledger(payment_order_id)
    WHERE payment_order_id IS NOT NULL
      AND reason = 'refund'
      AND delta_usd < 0;

CREATE UNIQUE INDEX IF NOT EXISTS idx_wallet_ledger_one_payment_refund_compensation
    ON subscription_wallet_ledger(payment_order_id)
    WHERE payment_order_id IS NOT NULL
      AND reason = 'refund'
      AND delta_usd > 0;

CREATE INDEX IF NOT EXISTS idx_wallet_ledger_payment_order
    ON subscription_wallet_ledger(payment_order_id)
    WHERE payment_order_id IS NOT NULL;

CREATE OR REPLACE FUNCTION hfc_assert_wallet_payment_source(target_order_id bigint)
RETURNS void
LANGUAGE plpgsql
AS $$
DECLARE
    source_order payment_orders%ROWTYPE;
    source_credit subscription_wallet_ledger%ROWTYPE;
    source_wallet_user_id bigint;
    refund_total numeric;
BEGIN
    SELECT *
    INTO source_order
    FROM payment_orders
    WHERE id = target_order_id
    FOR UPDATE;

    IF NOT FOUND THEN
        RAISE EXCEPTION 'wallet payment source violation: payment order % is missing', target_order_id
            USING ERRCODE = '23503';
    END IF;

    SELECT *
    INTO source_credit
    FROM subscription_wallet_ledger
    WHERE payment_order_id = target_order_id
      AND reason IN ('activation', 'topup')
      AND delta_usd > 0;

    IF source_order.order_type <> 'subscription'
       OR source_order.subscription_group_id IS NOT NULL
       OR source_order.plan_id IS NULL THEN
        RAISE EXCEPTION 'wallet payment source violation: order % is not a credits-wallet-shaped order', target_order_id
            USING ERRCODE = '23514';
    END IF;

    IF source_credit.id IS NULL THEN
        IF source_order.status IN ('COMPLETED', 'REFUNDING', 'PARTIALLY_REFUNDED', 'REFUNDED') THEN
            RAISE EXCEPTION 'wallet payment source violation: fulfilled order % has no immutable credit row', target_order_id
                USING ERRCODE = '23514';
        END IF;
        RETURN;
    END IF;

    SELECT user_id
    INTO source_wallet_user_id
    FROM user_subscriptions
    WHERE id = source_credit.subscription_id;

    IF source_wallet_user_id IS DISTINCT FROM source_order.user_id THEN
        RAISE EXCEPTION 'wallet payment source violation: order % and wallet owner differ', target_order_id
            USING ERRCODE = '23514';
    END IF;

    IF EXISTS (
        SELECT 1
        FROM subscription_wallet_ledger reversal
        WHERE reversal.payment_order_id = target_order_id
          AND reversal.reason = 'refund'
          AND reversal.subscription_id <> source_credit.subscription_id
    ) THEN
        RAISE EXCEPTION 'wallet payment source violation: order % refund targets another wallet', target_order_id
            USING ERRCODE = '23514';
    END IF;

    SELECT COALESCE(SUM(delta_usd), 0)
    INTO refund_total
    FROM subscription_wallet_ledger
    WHERE payment_order_id = target_order_id
      AND reason = 'refund';

    IF refund_total > 0 OR refund_total < -source_credit.delta_usd THEN
        RAISE EXCEPTION 'wallet payment source violation: order % refund delta exceeds its credited amount', target_order_id
            USING ERRCODE = '23514';
    END IF;
END $$;

CREATE OR REPLACE FUNCTION hfc_check_wallet_payment_source_from_ledger()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF NEW.payment_order_id IS NOT NULL THEN
        PERFORM hfc_assert_wallet_payment_source(NEW.payment_order_id);
    END IF;
    RETURN NEW;
END $$;

DROP TRIGGER IF EXISTS trg_hfc_check_wallet_payment_source_from_ledger
    ON subscription_wallet_ledger;
CREATE CONSTRAINT TRIGGER trg_hfc_check_wallet_payment_source_from_ledger
AFTER INSERT OR UPDATE
ON subscription_wallet_ledger
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW
EXECUTE FUNCTION hfc_check_wallet_payment_source_from_ledger();

CREATE OR REPLACE FUNCTION hfc_check_wallet_payment_source_from_order()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF NEW.order_type = 'subscription'
       AND NEW.subscription_group_id IS NULL
       AND NEW.plan_id IS NOT NULL
       AND NEW.status IN ('COMPLETED', 'REFUNDING', 'PARTIALLY_REFUNDED', 'REFUNDED') THEN
        PERFORM hfc_assert_wallet_payment_source(NEW.id);
    END IF;
    RETURN NEW;
END $$;

DROP TRIGGER IF EXISTS trg_hfc_check_wallet_payment_source_from_order
    ON payment_orders;
CREATE CONSTRAINT TRIGGER trg_hfc_check_wallet_payment_source_from_order
AFTER INSERT OR UPDATE OF status, order_type, subscription_group_id, plan_id
ON payment_orders
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW
EXECUTE FUNCTION hfc_check_wallet_payment_source_from_order();

COMMENT ON COLUMN subscription_wallet_ledger.payment_order_id IS
    'immutable payment source for credits activation/topup and its refund reversal; NULL for non-payment or historical unproven rows';
