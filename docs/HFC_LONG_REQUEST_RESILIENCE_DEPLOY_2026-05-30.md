# HFC Long Request Resilience Deploy - 2026-05-30

## Summary

Deployed the OpenAI-compatible buffered non-stream failover fix for long requests.

- Commit: `4aedd982 fix: failover openai buffered stream resets`
- Branch: `codex/hfc-long-request-resilience-20260529`
- Production image: `hfc/sub2api:long-request-resilience-4aedd982-20260530-030914`
- Previous image: `hfc/sub2api:anti-overwrite-admin-wallet-6abdf8d7-20260529-145220`
- Image id: `sha256:dc1239c2e5fcca1ff9bd8d3085785c319e28d2be994ac385ff0034ad76158b08`
- Image created: `2026-05-30T03:34:46.449241178+08:00`

## Change

The fix turns OpenAI-compatible buffered SSE read failures before downstream response write into an upstream failover error. This lets the gateway retry another account instead of returning a final 502 when a long non-stream generation hits an upstream HTTP/2 stream reset while buffering the response.

Changed files:

- `backend/internal/service/openai_gateway_buffered_fallback.go`
- `backend/internal/service/openai_gateway_chat_completions_test.go`
- `backend/internal/service/openai_gateway_messages.go`

## Local Verification

Run from `backend/`:

- `go test ./internal/service -run TestForwardAsChatCompletions_BufferedReadErrorReturnsFailover -count=1` passed.
- `go test ./internal/service -count=1` passed.

## Deploy Notes

The local Mac Docker build was blocked by low disk space and a Docker/containerd I/O error. Production image build was performed on the relay server from a clean `git archive` of commit `4aedd982`, not from the dirty production source checkout.

Temporary clean source build directory:

- `/opt/relay/builds/sub2api-long-request-resilience-4aedd982-20260530-0314`

This temporary source directory was removed during closure after the production image and rollback evidence were verified.

Production resource guard initially refused the build because root disk usage was above the guard threshold. Older automatic backups were removed after writing a cleanup manifest:

- `/opt/relay/ai-relay-infra/backups/disk-cleanup-long-request-20260530-031748/deleted-files.txt`

Recent rollback backups were retained, including full archives for 2026-05-27 through 2026-05-29 and DB auto dumps through 2026-05-30.

## Cutover Notes

The first guarded `hfc-sub2api-safe-cutover.sh --execute` attempt hit a Docker Compose v5 container-name conflict during `--force-recreate`; the script rolled back to the previous image and public probes returned 200.

The final production switch used the same safety gates, then a stop/remove/up sequence with the direct-compose acknowledgement required by the production guard to avoid the Compose name conflict.

Final compose backup:

- `/opt/relay/ai-relay-infra/sub2api/docker-compose.yml.bak-manual-cutover-20260530-041604`

## Production Verification

Post-deploy production state was verified on 2026-05-30.

- Running image: `hfc/sub2api:long-request-resilience-4aedd982-20260530-030914`
- Container state: running and healthy.
- Compose image line points to the new image.
- `https://api.handsfreeclub.com/health` returned `{"status":"ok"}`.
- `https://api.handsfreeclub.com/v1/models` returned HTTP 200.
- Admin login and admin accounts public probes returned HTTP 200 during cutover verification.
- Root disk after deploy: under the production guard threshold in the verified checks.

## Anti-Overwrite Gate

Monthly-card and wallet anti-overwrite checks passed before and after cutover.

Manual post-cutover smoke:

- `monthly_billing_smoke=pass`
- `user_id=142`
- `api_key_id=339`
- `subscription_id=146`
- `balance=0.00000000`

Automatic timer verification:

- `sub2api-monthly-billing-smoke.timer` active.
- The 2026-05-30 04:23 timer run passed on the new image.
- A later 2026-05-30 10:46 timer run also passed on the new image.

Wallet/frontend markers passed on the new image:

- `wallet_balance_usd!=null || hasWalletBalance`
- `wallet_initial_usd!=null || wallet_initial_usd`

## Follow-Up Items

These are not blockers for the deployed fix:

- Fix `hfc-sub2api-safe-cutover.sh` for Docker Compose v5 name-conflict behavior so future cutovers can use only the guarded script path.
- Clean local Mac Docker disk before the next local image build.
- Review the Claude account pool circuit-breaker history separately; it did not block the final deployed image or automatic monthly-card smoke.
