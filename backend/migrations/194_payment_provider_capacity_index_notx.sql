-- The provider-capacity admission transaction sums today's reservations while
-- holding one provider-instance row lock. Keep that serialized query bounded
-- as the payment_orders table grows, without blocking writes during rollout.
CREATE INDEX CONCURRENTLY IF NOT EXISTS paymentorder_provider_instance_id_created_at
    ON payment_orders (provider_instance_id, created_at)
    WHERE provider_instance_id IS NOT NULL;
