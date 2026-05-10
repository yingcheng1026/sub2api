-- 150_add_subscription_plan_groups.sql
-- 钱包模式 v4 — Phase A schema #1
--
-- 背景:
--   v3 模式下 subscription_plans.group_id 死绑 1 个 group, 用户激活后只能用该 group。
--   v4 钱包模式需要 plan ↔ N 个 group, 用户激活后所有关联 group 都可用,
--   按各 group 的 rate_multiplier 从共享钱包扣费。
--
-- 详细设计: ai-relay-infra/docs/plans/2026-05-10-wallet-mode-design.md §1.1

CREATE TABLE IF NOT EXISTS subscription_plan_groups (
    plan_id    BIGINT NOT NULL REFERENCES subscription_plans(id) ON DELETE CASCADE,
    group_id   BIGINT NOT NULL REFERENCES groups(id) ON DELETE CASCADE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (plan_id, group_id)
);

CREATE INDEX IF NOT EXISTS idx_subscription_plan_groups_group_id
    ON subscription_plan_groups(group_id);

COMMENT ON TABLE  subscription_plan_groups   IS '钱包模式 plan ↔ group 多对多关联（v4）。空 = 走老的 plan.group_id 单 group 模式（v3）';
COMMENT ON COLUMN subscription_plan_groups.plan_id  IS '订阅 plan ID';
COMMENT ON COLUMN subscription_plan_groups.group_id IS '激活后用户可访问的 group ID';
