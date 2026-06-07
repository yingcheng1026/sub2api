-- migration 171: 支持月卡叠月卡（多条 active wallet 订阅并存）
--
-- 背景：移除 migration 151 建立的 one-active-wallet-per-user 唯一约束，
-- 允许同一用户拥有多条 active wallet 订阅行（月卡叠月卡场景）。
--
-- 计费侧按 expires_at ASC 选行（先到期先消费），由 service 层保证，
-- 本迁移补充对应的性能索引。
--
-- 回滚注意：重建唯一索引前必须确保每个 user_id 只剩一条 active wallet 行，
-- 否则 CREATE UNIQUE INDEX 会失败。

-- 删除旧的「一用户一条 active wallet」唯一约束
DROP INDEX IF EXISTS user_subscriptions_one_active_wallet;

-- 新建「先到期先消费」选行性能索引
CREATE INDEX IF NOT EXISTS idx_user_subscriptions_active_wallet_by_expiry
    ON user_subscriptions(user_id, expires_at)
    WHERE wallet_balance_usd IS NOT NULL
      AND status = 'active'
      AND deleted_at IS NULL;
