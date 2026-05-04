-- 135_double_entry_billing_ledger.sql
-- Add a source-linked double-entry ledger for wallet and API usage accounting.
--
-- This migration is append-only and has no direct billing side effects: it does
-- not mutate users.balance, api_keys.quota_used, subscription usage counters, or
-- payment order state. It records balanced ledger transactions from existing
-- source-of-truth writes so production billing can be reconciled independently.

CREATE TABLE IF NOT EXISTS ledger_accounts (
    id BIGSERIAL PRIMARY KEY,
    account_code TEXT NOT NULL UNIQUE,
    account_type TEXT NOT NULL CHECK (account_type IN ('asset', 'liability', 'revenue', 'expense', 'equity')),
    owner_type TEXT NOT NULL DEFAULT 'system' CHECK (owner_type IN ('system', 'user', 'api_key', 'provider')),
    owner_id BIGINT,
    currency CHAR(3) NOT NULL DEFAULT 'USD',
    normal_balance TEXT NOT NULL CHECK (normal_balance IN ('debit', 'credit')),
    name TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_ledger_accounts_owner
    ON ledger_accounts (owner_type, owner_id);

CREATE TABLE IF NOT EXISTS ledger_transactions (
    id BIGSERIAL PRIMARY KEY,
    transaction_type TEXT NOT NULL,
    idempotency_key TEXT NOT NULL UNIQUE,
    source_type TEXT NOT NULL,
    source_id BIGINT NOT NULL,
    user_id BIGINT REFERENCES users(id) ON DELETE SET NULL,
    api_key_id BIGINT REFERENCES api_keys(id) ON DELETE SET NULL,
    usage_log_id BIGINT REFERENCES usage_logs(id) ON DELETE SET NULL,
    billing_usage_entry_id BIGINT REFERENCES billing_usage_entries(id) ON DELETE SET NULL,
    redeem_code_id BIGINT REFERENCES redeem_codes(id) ON DELETE SET NULL,
    payment_order_id BIGINT REFERENCES payment_orders(id) ON DELETE SET NULL,
    payment_audit_log_id BIGINT REFERENCES payment_audit_logs(id) ON DELETE SET NULL,
    affiliate_ledger_id BIGINT REFERENCES user_affiliate_ledger(id) ON DELETE SET NULL,
    currency CHAR(3) NOT NULL DEFAULT 'USD',
    amount_usd NUMERIC(20,10) NOT NULL CHECK (amount_usd > 0),
    status TEXT NOT NULL DEFAULT 'posted' CHECK (status IN ('posted', 'void')),
    description TEXT NOT NULL DEFAULT '',
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    occurred_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_ledger_transactions_source
    ON ledger_transactions (source_type, source_id);

CREATE INDEX IF NOT EXISTS idx_ledger_transactions_user_time
    ON ledger_transactions (user_id, occurred_at);

CREATE INDEX IF NOT EXISTS idx_ledger_transactions_usage_entry
    ON ledger_transactions (billing_usage_entry_id);

CREATE INDEX IF NOT EXISTS idx_ledger_transactions_redeem_code
    ON ledger_transactions (redeem_code_id);

CREATE INDEX IF NOT EXISTS idx_ledger_transactions_payment_audit
    ON ledger_transactions (payment_audit_log_id);

CREATE INDEX IF NOT EXISTS idx_ledger_transactions_affiliate_ledger
    ON ledger_transactions (affiliate_ledger_id);

CREATE TABLE IF NOT EXISTS ledger_lines (
    id BIGSERIAL PRIMARY KEY,
    transaction_id BIGINT NOT NULL REFERENCES ledger_transactions(id) ON DELETE CASCADE,
    account_id BIGINT NOT NULL REFERENCES ledger_accounts(id) ON DELETE RESTRICT,
    line_no SMALLINT NOT NULL,
    side TEXT NOT NULL CHECK (side IN ('debit', 'credit')),
    amount_usd NUMERIC(20,10) NOT NULL CHECK (amount_usd > 0),
    currency CHAR(3) NOT NULL DEFAULT 'USD',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (transaction_id, line_no)
);

CREATE INDEX IF NOT EXISTS idx_ledger_lines_account
    ON ledger_lines (account_id);

CREATE INDEX IF NOT EXISTS idx_ledger_lines_transaction
    ON ledger_lines (transaction_id);

CREATE OR REPLACE FUNCTION ensure_system_ledger_account(
    p_account_code TEXT,
    p_account_type TEXT,
    p_normal_balance TEXT,
    p_name TEXT
)
RETURNS BIGINT AS $$
DECLARE
    v_account_id BIGINT;
BEGIN
    INSERT INTO ledger_accounts (
        account_code,
        account_type,
        owner_type,
        owner_id,
        currency,
        normal_balance,
        name,
        updated_at
    )
    VALUES (
        p_account_code,
        p_account_type,
        'system',
        NULL,
        'USD',
        p_normal_balance,
        p_name,
        NOW()
    )
    ON CONFLICT (account_code) DO UPDATE
    SET
        account_type = EXCLUDED.account_type,
        owner_type = EXCLUDED.owner_type,
        owner_id = EXCLUDED.owner_id,
        currency = EXCLUDED.currency,
        normal_balance = EXCLUDED.normal_balance,
        name = EXCLUDED.name,
        updated_at = NOW()
    RETURNING id INTO v_account_id;

    RETURN v_account_id;
END;
$$ LANGUAGE plpgsql;

CREATE OR REPLACE FUNCTION ensure_user_wallet_ledger_account(p_user_id BIGINT)
RETURNS BIGINT AS $$
DECLARE
    v_account_id BIGINT;
BEGIN
    IF p_user_id IS NULL THEN
        RETURN NULL;
    END IF;

    INSERT INTO ledger_accounts (
        account_code,
        account_type,
        owner_type,
        owner_id,
        currency,
        normal_balance,
        name,
        updated_at
    )
    VALUES (
        'user:' || p_user_id::text || ':wallet_liability',
        'liability',
        'user',
        p_user_id,
        'USD',
        'credit',
        'User ' || p_user_id::text || ' wallet liability',
        NOW()
    )
    ON CONFLICT (account_code) DO UPDATE
    SET
        account_type = EXCLUDED.account_type,
        owner_type = EXCLUDED.owner_type,
        owner_id = EXCLUDED.owner_id,
        currency = EXCLUDED.currency,
        normal_balance = EXCLUDED.normal_balance,
        name = EXCLUDED.name,
        updated_at = NOW()
    RETURNING id INTO v_account_id;

    RETURN v_account_id;
END;
$$ LANGUAGE plpgsql;

DO $$
BEGIN
    PERFORM ensure_system_ledger_account('asset:payment_cash_clearing', 'asset', 'debit', 'Payment cash clearing');
    PERFORM ensure_system_ledger_account('revenue:api_usage', 'revenue', 'credit', 'API usage revenue');
    PERFORM ensure_system_ledger_account('expense:wallet_credit_grants', 'expense', 'debit', 'Wallet credit grants and adjustments');
    PERFORM ensure_system_ledger_account('expense:affiliate_rebate', 'expense', 'debit', 'Affiliate rebate expense');
    PERFORM ensure_system_ledger_account('equity:opening_balance_adjustment', 'equity', 'credit', 'Opening wallet balance adjustment');
END;
$$;

CREATE OR REPLACE FUNCTION post_two_line_ledger_transaction(
    p_transaction_type TEXT,
    p_idempotency_key TEXT,
    p_source_type TEXT,
    p_source_id BIGINT,
    p_user_id BIGINT,
    p_api_key_id BIGINT,
    p_usage_log_id BIGINT,
    p_billing_usage_entry_id BIGINT,
    p_redeem_code_id BIGINT,
    p_payment_order_id BIGINT,
    p_payment_audit_log_id BIGINT,
    p_affiliate_ledger_id BIGINT,
    p_debit_account_id BIGINT,
    p_credit_account_id BIGINT,
    p_amount_usd NUMERIC,
    p_description TEXT,
    p_metadata JSONB,
    p_occurred_at TIMESTAMPTZ
)
RETURNS BIGINT AS $$
DECLARE
    v_transaction_id BIGINT;
BEGIN
    IF p_amount_usd IS NULL
       OR p_amount_usd <= 0
       OR p_debit_account_id IS NULL
       OR p_credit_account_id IS NULL
       OR p_idempotency_key IS NULL
       OR btrim(p_idempotency_key) = '' THEN
        RETURN NULL;
    END IF;

    WITH inserted AS (
        INSERT INTO ledger_transactions (
            transaction_type,
            idempotency_key,
            source_type,
            source_id,
            user_id,
            api_key_id,
            usage_log_id,
            billing_usage_entry_id,
            redeem_code_id,
            payment_order_id,
            payment_audit_log_id,
            affiliate_ledger_id,
            currency,
            amount_usd,
            status,
            description,
            metadata,
            occurred_at
        )
        VALUES (
            p_transaction_type,
            p_idempotency_key,
            p_source_type,
            p_source_id,
            p_user_id,
            p_api_key_id,
            p_usage_log_id,
            p_billing_usage_entry_id,
            p_redeem_code_id,
            p_payment_order_id,
            p_payment_audit_log_id,
            p_affiliate_ledger_id,
            'USD',
            p_amount_usd,
            'posted',
            COALESCE(p_description, ''),
            COALESCE(p_metadata, '{}'::jsonb),
            COALESCE(p_occurred_at, NOW())
        )
        ON CONFLICT (idempotency_key) DO NOTHING
        RETURNING id
    )
    SELECT id INTO v_transaction_id
    FROM inserted
    UNION ALL
    SELECT id
    FROM ledger_transactions
    WHERE idempotency_key = p_idempotency_key
    LIMIT 1;

    IF v_transaction_id IS NULL THEN
        RETURN NULL;
    END IF;

    INSERT INTO ledger_lines (
        transaction_id,
        account_id,
        line_no,
        side,
        amount_usd,
        currency,
        created_at
    )
    VALUES
        (v_transaction_id, p_debit_account_id, 1, 'debit', p_amount_usd, 'USD', COALESCE(p_occurred_at, NOW())),
        (v_transaction_id, p_credit_account_id, 2, 'credit', p_amount_usd, 'USD', COALESCE(p_occurred_at, NOW()))
    ON CONFLICT (transaction_id, line_no) DO NOTHING;

    RETURN v_transaction_id;
END;
$$ LANGUAGE plpgsql;

CREATE OR REPLACE FUNCTION assert_ledger_transaction_balanced()
RETURNS TRIGGER AS $$
DECLARE
    v_transaction_id BIGINT;
    v_debit_total NUMERIC(20,10);
    v_credit_total NUMERIC(20,10);
BEGIN
    IF TG_TABLE_NAME = 'ledger_lines' THEN
        IF TG_OP = 'DELETE' THEN
            v_transaction_id := OLD.transaction_id;
        ELSE
            v_transaction_id := NEW.transaction_id;
        END IF;
    ELSE
        IF TG_OP = 'DELETE' THEN
            v_transaction_id := OLD.id;
        ELSE
            v_transaction_id := NEW.id;
        END IF;
    END IF;

    IF v_transaction_id IS NULL THEN
        IF TG_OP = 'DELETE' THEN
            RETURN OLD;
        END IF;
        RETURN NEW;
    END IF;

    IF NOT EXISTS (
        SELECT 1
        FROM ledger_transactions
        WHERE id = v_transaction_id
          AND status = 'posted'
    ) THEN
        IF TG_OP = 'DELETE' THEN
            RETURN OLD;
        END IF;
        RETURN NEW;
    END IF;

    SELECT
        COALESCE(SUM(amount_usd) FILTER (WHERE side = 'debit'), 0),
        COALESCE(SUM(amount_usd) FILTER (WHERE side = 'credit'), 0)
    INTO v_debit_total, v_credit_total
    FROM ledger_lines
    WHERE transaction_id = v_transaction_id;

    IF v_debit_total <> v_credit_total OR v_debit_total <= 0 THEN
        RAISE EXCEPTION 'ledger transaction % is unbalanced: debit %, credit %',
            v_transaction_id, v_debit_total, v_credit_total
            USING ERRCODE = '23514';
    END IF;

    IF TG_OP = 'DELETE' THEN
        RETURN OLD;
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_ledger_transactions_balanced ON ledger_transactions;
CREATE CONSTRAINT TRIGGER trg_ledger_transactions_balanced
AFTER INSERT OR UPDATE ON ledger_transactions
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW
EXECUTE FUNCTION assert_ledger_transaction_balanced();

DROP TRIGGER IF EXISTS trg_ledger_lines_balanced ON ledger_lines;
CREATE CONSTRAINT TRIGGER trg_ledger_lines_balanced
AFTER INSERT OR UPDATE OR DELETE ON ledger_lines
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW
EXECUTE FUNCTION assert_ledger_transaction_balanced();

CREATE OR REPLACE FUNCTION post_usage_billing_entry_to_ledger(p_entry_id BIGINT)
RETURNS BIGINT AS $$
DECLARE
    v_entry RECORD;
    v_wallet_account_id BIGINT;
    v_revenue_account_id BIGINT;
BEGIN
    SELECT
        b.id,
        b.usage_log_id,
        b.user_id,
        b.api_key_id,
        b.subscription_id,
        b.billing_type,
        b.delta_usd,
        b.created_at,
        ul.request_id,
        ul.model,
        ul.requested_model,
        ul.upstream_model,
        ul.inbound_endpoint,
        ul.upstream_endpoint
    INTO v_entry
    FROM billing_usage_entries b
    LEFT JOIN usage_logs ul ON ul.id = b.usage_log_id
    WHERE b.id = p_entry_id
      AND b.applied = TRUE
      AND b.delta_usd > 0;

    IF NOT FOUND THEN
        RETURN NULL;
    END IF;

    v_wallet_account_id := ensure_user_wallet_ledger_account(v_entry.user_id);
    v_revenue_account_id := ensure_system_ledger_account('revenue:api_usage', 'revenue', 'credit', 'API usage revenue');

    RETURN post_two_line_ledger_transaction(
        'usage_charge',
        'billing_usage_entry:' || v_entry.id::text,
        'billing_usage_entry',
        v_entry.id,
        v_entry.user_id,
        v_entry.api_key_id,
        v_entry.usage_log_id,
        v_entry.id,
        NULL,
        NULL,
        NULL,
        NULL,
        v_wallet_account_id,
        v_revenue_account_id,
        v_entry.delta_usd,
        'API usage charge',
        jsonb_strip_nulls(jsonb_build_object(
            'billing_type', v_entry.billing_type,
            'subscription_id', v_entry.subscription_id,
            'request_id', v_entry.request_id,
            'model', v_entry.model,
            'requested_model', v_entry.requested_model,
            'upstream_model', v_entry.upstream_model,
            'inbound_endpoint', v_entry.inbound_endpoint,
            'upstream_endpoint', v_entry.upstream_endpoint
        )),
        v_entry.created_at
    );
END;
$$ LANGUAGE plpgsql;

CREATE OR REPLACE FUNCTION record_usage_billing_double_entry()
RETURNS TRIGGER AS $$
BEGIN
    PERFORM post_usage_billing_entry_to_ledger(NEW.id);
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_billing_usage_entries_double_entry ON billing_usage_entries;
CREATE TRIGGER trg_billing_usage_entries_double_entry
AFTER INSERT OR UPDATE OF applied, delta_usd ON billing_usage_entries
FOR EACH ROW
WHEN (NEW.applied = TRUE AND NEW.delta_usd > 0)
EXECUTE FUNCTION record_usage_billing_double_entry();

CREATE OR REPLACE FUNCTION post_redeem_code_balance_to_ledger(p_redeem_code_id BIGINT)
RETURNS BIGINT AS $$
DECLARE
    v_code RECORD;
    v_wallet_account_id BIGINT;
    v_debit_account_id BIGINT;
    v_credit_account_id BIGINT;
    v_payment_cash_account_id BIGINT;
    v_grant_account_id BIGINT;
    v_amount NUMERIC(20,10);
BEGIN
    SELECT
        rc.id,
        rc.type,
        rc.value,
        rc.used_by,
        rc.used_at,
        rc.created_at,
        po.id AS payment_order_id
    INTO v_code
    FROM redeem_codes rc
    LEFT JOIN payment_orders po
      ON po.recharge_code = rc.code
     AND po.order_type = 'balance'
    WHERE rc.id = p_redeem_code_id
      AND rc.status = 'used'
      AND rc.used_by IS NOT NULL
      AND rc.type IN ('balance', 'admin_balance')
      AND rc.value <> 0;

    IF NOT FOUND THEN
        RETURN NULL;
    END IF;

    v_amount := abs(v_code.value)::numeric(20,10);
    v_wallet_account_id := ensure_user_wallet_ledger_account(v_code.used_by);
    v_payment_cash_account_id := ensure_system_ledger_account('asset:payment_cash_clearing', 'asset', 'debit', 'Payment cash clearing');
    v_grant_account_id := ensure_system_ledger_account('expense:wallet_credit_grants', 'expense', 'debit', 'Wallet credit grants and adjustments');

    IF v_code.value > 0 THEN
        IF v_code.payment_order_id IS NOT NULL THEN
            v_debit_account_id := v_payment_cash_account_id;
        ELSE
            v_debit_account_id := v_grant_account_id;
        END IF;
        v_credit_account_id := v_wallet_account_id;
    ELSE
        v_debit_account_id := v_wallet_account_id;
        IF v_code.payment_order_id IS NOT NULL THEN
            v_credit_account_id := v_payment_cash_account_id;
        ELSE
            v_credit_account_id := v_grant_account_id;
        END IF;
    END IF;

    RETURN post_two_line_ledger_transaction(
        CASE WHEN v_code.value > 0 THEN 'wallet_credit' ELSE 'wallet_debit' END,
        'redeem_code:' || v_code.id::text || ':wallet',
        'redeem_code',
        v_code.id,
        v_code.used_by,
        NULL,
        NULL,
        NULL,
        v_code.id,
        v_code.payment_order_id,
        NULL,
        NULL,
        v_debit_account_id,
        v_credit_account_id,
        v_amount,
        CASE WHEN v_code.value > 0 THEN 'Wallet credit from redeem code' ELSE 'Wallet debit from redeem code' END,
        jsonb_strip_nulls(jsonb_build_object(
            'redeem_type', v_code.type,
            'payment_order_id', v_code.payment_order_id
        )),
        COALESCE(v_code.used_at, v_code.created_at)
    );
END;
$$ LANGUAGE plpgsql;

CREATE OR REPLACE FUNCTION record_redeem_code_balance_double_entry()
RETURNS TRIGGER AS $$
BEGIN
    PERFORM post_redeem_code_balance_to_ledger(NEW.id);
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_redeem_codes_balance_double_entry ON redeem_codes;
CREATE TRIGGER trg_redeem_codes_balance_double_entry
AFTER INSERT OR UPDATE OF status, value, used_by, type ON redeem_codes
FOR EACH ROW
WHEN (NEW.status = 'used' AND NEW.used_by IS NOT NULL AND NEW.type IN ('balance', 'admin_balance') AND NEW.value <> 0)
EXECUTE FUNCTION record_redeem_code_balance_double_entry();

CREATE OR REPLACE FUNCTION post_affiliate_transfer_to_ledger(p_affiliate_ledger_id BIGINT)
RETURNS BIGINT AS $$
DECLARE
    v_entry RECORD;
    v_wallet_account_id BIGINT;
    v_affiliate_expense_account_id BIGINT;
BEGIN
    SELECT id, user_id, action, amount, created_at
    INTO v_entry
    FROM user_affiliate_ledger
    WHERE id = p_affiliate_ledger_id
      AND action = 'transfer'
      AND amount > 0;

    IF NOT FOUND THEN
        RETURN NULL;
    END IF;

    v_wallet_account_id := ensure_user_wallet_ledger_account(v_entry.user_id);
    v_affiliate_expense_account_id := ensure_system_ledger_account('expense:affiliate_rebate', 'expense', 'debit', 'Affiliate rebate expense');

    RETURN post_two_line_ledger_transaction(
        'affiliate_wallet_credit',
        'affiliate_ledger:' || v_entry.id::text || ':transfer',
        'user_affiliate_ledger',
        v_entry.id,
        v_entry.user_id,
        NULL,
        NULL,
        NULL,
        NULL,
        NULL,
        NULL,
        v_entry.id,
        v_affiliate_expense_account_id,
        v_wallet_account_id,
        v_entry.amount::numeric(20,10),
        'Affiliate rebate transferred to wallet',
        jsonb_build_object('affiliate_action', v_entry.action),
        v_entry.created_at
    );
END;
$$ LANGUAGE plpgsql;

CREATE OR REPLACE FUNCTION record_affiliate_transfer_double_entry()
RETURNS TRIGGER AS $$
BEGIN
    PERFORM post_affiliate_transfer_to_ledger(NEW.id);
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_user_affiliate_ledger_transfer_double_entry ON user_affiliate_ledger;
CREATE TRIGGER trg_user_affiliate_ledger_transfer_double_entry
AFTER INSERT OR UPDATE OF action, amount ON user_affiliate_ledger
FOR EACH ROW
WHEN (NEW.action = 'transfer' AND NEW.amount > 0)
EXECUTE FUNCTION record_affiliate_transfer_double_entry();

CREATE OR REPLACE FUNCTION ledger_safe_jsonb(p_text TEXT)
RETURNS JSONB AS $$
BEGIN
    RETURN COALESCE(NULLIF(p_text, ''), '{}')::jsonb;
EXCEPTION WHEN OTHERS THEN
    RETURN '{}'::jsonb;
END;
$$ LANGUAGE plpgsql;

CREATE OR REPLACE FUNCTION ledger_safe_numeric(p_text TEXT)
RETURNS NUMERIC AS $$
BEGIN
    RETURN COALESCE(NULLIF(p_text, '')::numeric, 0);
EXCEPTION WHEN OTHERS THEN
    RETURN 0;
END;
$$ LANGUAGE plpgsql;

CREATE OR REPLACE FUNCTION post_payment_refund_audit_to_ledger(p_payment_audit_log_id BIGINT)
RETURNS BIGINT AS $$
DECLARE
    v_audit RECORD;
    v_detail JSONB;
    v_balance_deducted NUMERIC(20,10);
    v_payment_order_id BIGINT;
    v_user_id BIGINT;
    v_wallet_account_id BIGINT;
    v_payment_cash_account_id BIGINT;
BEGIN
    SELECT id, order_id, action, detail, created_at
    INTO v_audit
    FROM payment_audit_logs
    WHERE id = p_payment_audit_log_id
      AND action = 'REFUND_SUCCESS';

    IF NOT FOUND THEN
        RETURN NULL;
    END IF;

    v_detail := ledger_safe_jsonb(v_audit.detail);
    v_balance_deducted := ledger_safe_numeric(v_detail->>'balanceDeducted');

    IF v_balance_deducted <= 0 OR v_audit.order_id !~ '^[0-9]+$' THEN
        RETURN NULL;
    END IF;

    v_payment_order_id := v_audit.order_id::bigint;

    SELECT user_id
    INTO v_user_id
    FROM payment_orders
    WHERE id = v_payment_order_id
      AND order_type = 'balance';

    IF v_user_id IS NULL THEN
        RETURN NULL;
    END IF;

    v_wallet_account_id := ensure_user_wallet_ledger_account(v_user_id);
    v_payment_cash_account_id := ensure_system_ledger_account('asset:payment_cash_clearing', 'asset', 'debit', 'Payment cash clearing');

    RETURN post_two_line_ledger_transaction(
        'wallet_refund_debit',
        'payment_audit_log:' || v_audit.id::text || ':refund_balance_deduction',
        'payment_audit_log',
        v_audit.id,
        v_user_id,
        NULL,
        NULL,
        NULL,
        NULL,
        v_payment_order_id,
        v_audit.id,
        NULL,
        v_wallet_account_id,
        v_payment_cash_account_id,
        v_balance_deducted,
        'Wallet debit for refunded balance order',
        jsonb_strip_nulls(jsonb_build_object(
            'refund_amount', v_detail->>'refundAmount',
            'balance_deducted', v_balance_deducted,
            'force', v_detail->>'force'
        )),
        v_audit.created_at
    );
END;
$$ LANGUAGE plpgsql;

CREATE OR REPLACE FUNCTION post_opening_wallet_balance_adjustment(p_user_id BIGINT)
RETURNS BIGINT AS $$
DECLARE
    v_user_balance NUMERIC(20,10);
    v_ledger_balance NUMERIC(20,10);
    v_delta NUMERIC(20,10);
    v_wallet_account_id BIGINT;
    v_opening_account_id BIGINT;
    v_debit_account_id BIGINT;
    v_credit_account_id BIGINT;
BEGIN
    SELECT balance::numeric(20,10)
    INTO v_user_balance
    FROM users
    WHERE id = p_user_id
      AND deleted_at IS NULL;

    IF NOT FOUND THEN
        RETURN NULL;
    END IF;

    SELECT (
        COALESCE(SUM(ll.amount_usd) FILTER (WHERE ll.side = 'credit'), 0)
        - COALESCE(SUM(ll.amount_usd) FILTER (WHERE ll.side = 'debit'), 0)
    )::numeric(20,10)
    INTO v_ledger_balance
    FROM ledger_accounts la
    LEFT JOIN ledger_lines ll ON ll.account_id = la.id
    WHERE la.owner_type = 'user'
      AND la.owner_id = p_user_id
      AND la.account_code = 'user:' || p_user_id::text || ':wallet_liability';

    v_delta := (COALESCE(v_user_balance, 0) - COALESCE(v_ledger_balance, 0))::numeric(20,10);

    IF abs(v_delta) < 0.0000000001 THEN
        RETURN NULL;
    END IF;

    v_wallet_account_id := ensure_user_wallet_ledger_account(p_user_id);
    v_opening_account_id := ensure_system_ledger_account('equity:opening_balance_adjustment', 'equity', 'credit', 'Opening wallet balance adjustment');

    IF v_delta > 0 THEN
        v_debit_account_id := v_opening_account_id;
        v_credit_account_id := v_wallet_account_id;
    ELSE
        v_debit_account_id := v_wallet_account_id;
        v_credit_account_id := v_opening_account_id;
    END IF;

    RETURN post_two_line_ledger_transaction(
        'opening_wallet_balance',
        'opening_wallet_balance:user:' || p_user_id::text || ':135',
        'users',
        p_user_id,
        p_user_id,
        NULL,
        NULL,
        NULL,
        NULL,
        NULL,
        NULL,
        NULL,
        v_debit_account_id,
        v_credit_account_id,
        abs(v_delta),
        'Opening wallet balance adjustment',
        jsonb_build_object(
            'users_balance', v_user_balance,
            'ledger_wallet_balance_before_adjustment', v_ledger_balance,
            'delta', v_delta
        ),
        NOW()
    );
END;
$$ LANGUAGE plpgsql;

CREATE OR REPLACE FUNCTION record_payment_refund_audit_double_entry()
RETURNS TRIGGER AS $$
BEGIN
    PERFORM post_payment_refund_audit_to_ledger(NEW.id);
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_payment_audit_logs_refund_double_entry ON payment_audit_logs;
CREATE TRIGGER trg_payment_audit_logs_refund_double_entry
AFTER INSERT OR UPDATE OF action, detail ON payment_audit_logs
FOR EACH ROW
WHEN (NEW.action = 'REFUND_SUCCESS')
EXECUTE FUNCTION record_payment_refund_audit_double_entry();

DO $$
DECLARE
    v_id BIGINT;
BEGIN
    FOR v_id IN
        SELECT id
        FROM billing_usage_entries
        WHERE applied = TRUE
          AND delta_usd > 0
          AND NOT EXISTS (
              SELECT 1
              FROM ledger_transactions lt
              WHERE lt.idempotency_key = 'billing_usage_entry:' || billing_usage_entries.id::text
          )
        ORDER BY id
    LOOP
        PERFORM post_usage_billing_entry_to_ledger(v_id);
    END LOOP;

    FOR v_id IN
        SELECT id
        FROM redeem_codes
        WHERE status = 'used'
          AND used_by IS NOT NULL
          AND type IN ('balance', 'admin_balance')
          AND value <> 0
          AND NOT EXISTS (
              SELECT 1
              FROM ledger_transactions lt
              WHERE lt.idempotency_key = 'redeem_code:' || redeem_codes.id::text || ':wallet'
          )
        ORDER BY id
    LOOP
        PERFORM post_redeem_code_balance_to_ledger(v_id);
    END LOOP;

    FOR v_id IN
        SELECT id
        FROM user_affiliate_ledger
        WHERE action = 'transfer'
          AND amount > 0
          AND NOT EXISTS (
              SELECT 1
              FROM ledger_transactions lt
              WHERE lt.idempotency_key = 'affiliate_ledger:' || user_affiliate_ledger.id::text || ':transfer'
          )
        ORDER BY id
    LOOP
        PERFORM post_affiliate_transfer_to_ledger(v_id);
    END LOOP;

    FOR v_id IN
        SELECT id
        FROM payment_audit_logs
        WHERE action = 'REFUND_SUCCESS'
          AND NOT EXISTS (
              SELECT 1
              FROM ledger_transactions lt
              WHERE lt.idempotency_key = 'payment_audit_log:' || payment_audit_logs.id::text || ':refund_balance_deduction'
          )
        ORDER BY id
    LOOP
        PERFORM post_payment_refund_audit_to_ledger(v_id);
    END LOOP;

    FOR v_id IN
        SELECT id
        FROM users
        WHERE deleted_at IS NULL
          AND NOT EXISTS (
              SELECT 1
              FROM ledger_transactions lt
              WHERE lt.idempotency_key = 'opening_wallet_balance:user:' || users.id::text || ':135'
          )
        ORDER BY id
    LOOP
        PERFORM post_opening_wallet_balance_adjustment(v_id);
    END LOOP;
END;
$$;

CREATE OR REPLACE VIEW ledger_transaction_balances AS
SELECT
    lt.id AS transaction_id,
    lt.idempotency_key,
    lt.transaction_type,
    lt.source_type,
    lt.source_id,
    lt.status,
    COALESCE(SUM(ll.amount_usd) FILTER (WHERE ll.side = 'debit'), 0)::numeric(20,10) AS debit_total,
    COALESCE(SUM(ll.amount_usd) FILTER (WHERE ll.side = 'credit'), 0)::numeric(20,10) AS credit_total,
    (
        COALESCE(SUM(ll.amount_usd) FILTER (WHERE ll.side = 'debit'), 0)
        - COALESCE(SUM(ll.amount_usd) FILTER (WHERE ll.side = 'credit'), 0)
    )::numeric(20,10) AS imbalance
FROM ledger_transactions lt
LEFT JOIN ledger_lines ll ON ll.transaction_id = lt.id
GROUP BY lt.id, lt.idempotency_key, lt.transaction_type, lt.source_type, lt.source_id, lt.status;

CREATE OR REPLACE VIEW ledger_unbalanced_transactions AS
SELECT *
FROM ledger_transaction_balances
WHERE status = 'posted'
  AND (debit_total <> credit_total OR debit_total <= 0);

CREATE OR REPLACE VIEW ledger_user_wallet_balances AS
SELECT
    la.owner_id AS user_id,
    (
        COALESCE(SUM(ll.amount_usd) FILTER (WHERE ll.side = 'credit'), 0)
        - COALESCE(SUM(ll.amount_usd) FILTER (WHERE ll.side = 'debit'), 0)
    )::numeric(20,10) AS ledger_wallet_balance_usd
FROM ledger_accounts la
LEFT JOIN ledger_lines ll ON ll.account_id = la.id
WHERE la.owner_type = 'user'
  AND la.account_code LIKE 'user:%:wallet_liability'
GROUP BY la.owner_id;

CREATE OR REPLACE VIEW ledger_wallet_reconciliation AS
SELECT
    u.id AS user_id,
    u.balance::numeric(20,10) AS users_balance_usd,
    COALESCE(luwb.ledger_wallet_balance_usd, 0)::numeric(20,10) AS ledger_wallet_balance_usd,
    (u.balance::numeric(20,10) - COALESCE(luwb.ledger_wallet_balance_usd, 0))::numeric(20,10) AS difference_usd
FROM users u
LEFT JOIN ledger_user_wallet_balances luwb ON luwb.user_id = u.id
WHERE u.deleted_at IS NULL;

CREATE OR REPLACE VIEW ledger_usage_reconciliation AS
SELECT
    b.id AS billing_usage_entry_id,
    b.usage_log_id,
    b.user_id,
    b.api_key_id,
    b.delta_usd,
    lt.id AS ledger_transaction_id,
    ltb.debit_total,
    ltb.credit_total,
    CASE
        WHEN lt.id IS NULL THEN 'missing_ledger_transaction'
        WHEN ltb.debit_total <> b.delta_usd OR ltb.credit_total <> b.delta_usd THEN 'amount_mismatch'
        ELSE 'ok'
    END AS status
FROM billing_usage_entries b
LEFT JOIN ledger_transactions lt
  ON lt.idempotency_key = 'billing_usage_entry:' || b.id::text
LEFT JOIN ledger_transaction_balances ltb
  ON ltb.transaction_id = lt.id
WHERE b.applied = TRUE
  AND b.delta_usd > 0;
