# Paid Trial One-Time Purchase Deployment

Date: 2026-05-23

Scope:
- Enforce one-time purchase for the 29.9 monthly paid-trial plan only.
- Internal payment plan: `paid-trial-v3-30d`, production `plan_id=5`.
- Liandong/redeem path: production subscription `group_id=13`.
- No pricing, rate multiplier, wallet quota, fulfillment, account routing, or billing multiplier changes.

Production commits:
- `6281d9ab` `fix(payment): enforce trial plan one-time purchase`
- `c7c9a6c2` `fix(payment): enforce trial redeem one-time limit`

Production image:
- `hfc/sub2api:paid-trial-redeem-once-c7c9a6c2-20260523-162451`

Verification:
- Local unit test: `go test -tags unit ./internal/service -run 'TestCheckPlanPurchaseLimit|TestCheckPaidTrialRedeemLimit|TestExecuteSubscriptionFulfillmentWalletPlanAssignsWalletSubscription'`
- Migration dry-run for `165_enforce_paid_trial_redeem_once.sql`: passed with `CREATE FUNCTION`, `CREATE TRIGGER`, then `ROLLBACK`.
- Deployed container: `sub2api` running and healthy on the new image.
- Public health checks: `https://api.handsfreeclub.com/health` returned 200 with `{"status":"ok"}`; public home and pricing pages returned 200.
- Database migrations applied: `164_enforce_paid_trial_once.sql`, `165_enforce_paid_trial_redeem_once.sql`.
- Database safeguards present: `payment_orders_paid_trial_once_per_user` index and `trg_hfc_paid_trial_redeem_once` trigger.
- Transaction probe confirmed a second used redeem code for the same user and group 13 is blocked; probe rows were rolled back.

Residual external boundary:
- Backend now prevents a second entitlement/order/redeem in HandsFreeClub.
- If the Liandong product page itself must prevent payment before the user reaches HandsFreeClub, the Liandong item `https://pay.ldxp.cn/item/z80wd7` also needs item-level purchase limit set to 1 in Liandong's own backend.
