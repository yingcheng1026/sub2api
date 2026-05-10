-- 152_add_subscription_wallet_ledger.sql
-- 钱包模式 v4 — Phase A schema #3
--
-- 钱包流水追加表 — 每次激活/扣费/退款/调账写一行, 永不更新永不删除。
-- user_subscriptions.wallet_balance_usd 是缓存字段, 真实账本以 ledger 为准。
--
-- 对账 cron 每 5 分钟跑:
--   wallet_initial_usd + SUM(ledger.delta_usd) ?= wallet_balance_usd
--   偏差 > $0.01 → telegram 告警 + 自动用 ledger 重算修正字段值
--
-- 详细设计: ai-relay-infra/docs/plans/2026-05-10-wallet-mode-design.md §1.2 §5.3

CREATE TABLE IF NOT EXISTS subscription_wallet_ledger (
    id              BIGSERIAL PRIMARY KEY,
    subscription_id BIGINT NOT NULL REFERENCES user_subscriptions(id) ON DELETE CASCADE,

    -- 流水金额: 正 = 充值/退款回, 负 = 消费
    delta_usd       DECIMAL(20, 10) NOT NULL,

    -- 此次操作后的余额（冗余，便于排查 race / 单笔回放）
    balance_after   DECIMAL(20, 10) NOT NULL,

    -- 流水类型: activation | usage | refund | adjustment | expiration
    reason          VARCHAR(32) NOT NULL,

    -- 关联 usage_log（仅 reason=usage 时填）
    usage_log_id    BIGINT REFERENCES usage_logs(id) ON DELETE SET NULL,

    -- 操作员 / 备注（refund / adjustment 时填）
    operator_id     BIGINT REFERENCES users(id) ON DELETE SET NULL,
    notes           TEXT,

    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- 按订阅查流水（dashboard 显示明细 / 对账）
CREATE INDEX IF NOT EXISTS idx_wallet_ledger_subscription_created
    ON subscription_wallet_ledger(subscription_id, created_at DESC);

-- 按 usage_log 反查（排查某次调用为何扣这么多）
CREATE INDEX IF NOT EXISTS idx_wallet_ledger_usage_log
    ON subscription_wallet_ledger(usage_log_id)
    WHERE usage_log_id IS NOT NULL;

-- reason 合法性约束
ALTER TABLE subscription_wallet_ledger
    DROP CONSTRAINT IF EXISTS chk_wallet_ledger_reason;
ALTER TABLE subscription_wallet_ledger
    ADD CONSTRAINT chk_wallet_ledger_reason CHECK (
        reason IN ('activation', 'usage', 'refund', 'adjustment', 'expiration')
    );

COMMENT ON TABLE  subscription_wallet_ledger IS
    '钱包流水追加表 - 真实账本。user_subscriptions.wallet_balance_usd 是其聚合的缓存字段。';
COMMENT ON COLUMN subscription_wallet_ledger.delta_usd     IS '流水金额: 正=入账, 负=出账';
COMMENT ON COLUMN subscription_wallet_ledger.balance_after IS '此次操作后余额（冗余字段，便于排查）';
COMMENT ON COLUMN subscription_wallet_ledger.reason        IS 'activation 激活 / usage 调用扣费 / refund 退款 / adjustment 客服调账 / expiration 过期清零';
