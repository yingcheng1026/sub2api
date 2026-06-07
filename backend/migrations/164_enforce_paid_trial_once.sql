-- 164_enforce_paid_trial_once.sql
--
-- HandsFreeClub production rule:
-- the ¥29.9 paid-trial monthly plan (production plan_id=5,
-- name=paid-trial-v3-30d) is limited to one non-terminal order per user.
--
-- This deliberately does not change pricing, rate multipliers, wallet balance,
-- fulfillment, or any other subscription plan.

CREATE UNIQUE INDEX IF NOT EXISTS payment_orders_paid_trial_once_per_user
    ON payment_orders (user_id, plan_id)
    WHERE order_type = 'subscription'
      AND plan_id = 5
      AND status IN (
          'PENDING',
          'PAID',
          'RECHARGING',
          'COMPLETED',
          'REFUND_REQUESTED',
          'REFUNDING',
          'PARTIALLY_REFUNDED',
          'REFUNDED',
          'REFUND_FAILED'
      );
