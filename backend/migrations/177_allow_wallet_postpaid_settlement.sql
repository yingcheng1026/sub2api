-- A wallet request is priced from the actual upstream response. If the final
-- cost is larger than the pre-request balance, the response has already been
-- delivered and the full cost must still be recorded. Keeping the old small
-- positive balance would let every following request pass preflight and fail
-- deduction forever, creating unlimited free usage.

ALTER TABLE user_subscriptions
    DROP CONSTRAINT IF EXISTS chk_user_subscriptions_wallet_balance_nonneg;

ALTER TABLE user_subscriptions
    DROP CONSTRAINT IF EXISTS chk_user_subscriptions_wallet_balance_finite;
ALTER TABLE user_subscriptions
    ADD CONSTRAINT chk_user_subscriptions_wallet_balance_finite CHECK (
        wallet_balance_usd IS NULL
        OR wallet_balance_usd NOT IN (
            'NaN'::numeric,
            'Infinity'::numeric,
            '-Infinity'::numeric
        )
    );

ALTER TABLE user_subscriptions
    DROP CONSTRAINT IF EXISTS chk_user_subscriptions_wallet_initial_valid;
ALTER TABLE user_subscriptions
    ADD CONSTRAINT chk_user_subscriptions_wallet_initial_valid CHECK (
        wallet_initial_usd IS NULL
        OR (
            wallet_initial_usd >= 0
            AND wallet_initial_usd NOT IN (
                'NaN'::numeric,
                'Infinity'::numeric,
                '-Infinity'::numeric
            )
        )
    );

COMMENT ON COLUMN user_subscriptions.wallet_balance_usd IS
    '钱包当前余额（USD，含倍率扣减）。可为负数，表示已交付请求的待补足欠费；余额 <= 0 时新请求必须被拒绝。NULL = 非钱包订阅。';
