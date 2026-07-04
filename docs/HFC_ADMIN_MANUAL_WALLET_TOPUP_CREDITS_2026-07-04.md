# HFC Admin Manual Wallet Topup Credits Alignment

Date: 2026-07-04

## Conclusion

Admin manual wallet topups now use the same permanent credits wallet semantics as customer recharge/redeem flows.

When an admin fills `wallet_initial_usd`, the admin assignment request is mapped to `plan_type=credits`. The service then creates or tops up a user-level credits wallet with a near-permanent expiry instead of creating the old finite wallet shape.

## Reason

Finite wallet subscriptions are blocked by production database rules because they are ambiguous with older monthly wallet subscriptions. Manual custom topups must therefore be represented as credits wallet balance, not as a wallet subscription with `validity_days`.

## Scope

- Backend admin assignment maps manual wallet amount to credits.
- Frontend wallet mode no longer shows, validates, or submits validity days.
- Admin wording now says wallet topup instead of user-level wallet assignment.
- Plan assignment and legacy group assignment remain unchanged.

## Verification

- `go test -tags unit ./internal/handler/admin ./internal/service -run 'TestAssignSubscriptionInputFromRequest|TestAssignWalletSubscriptionCreditsPlanForcesMaxExpiresAt|TestAssignWalletSubscriptionToppedUpWhenCreditsAndExistingActive|TestAssignWalletSubscriptionConflictWhenCreditsButTopupServiceMissing'` passed in `golang:1.26.3`.
- `./node_modules/.bin/vue-tsc --noEmit` passed from `frontend/`.
- `git diff --check` passed.

## Notes

Local `pnpm --dir frontend install --frozen-lockfile` installed dependencies, but the command returned non-zero because pnpm blocked dependency build scripts for `esbuild` and `vue-demi`. The direct Vue typecheck still passed after dependency installation.
