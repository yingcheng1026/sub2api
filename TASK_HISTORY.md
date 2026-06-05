# Task History

## 2026-06-04 - Sync paid-lite Liandong SKU into renewal modal

- Added the `轻量正式版` paid-lite monthly SKU to the shared Liandong monthly tier constants: `¥99 / $400 / https://pay.ldxp.cn/item/neu4dr`.
- Updated the renewal/top-up modal comment from 4 monthly tiers to 5 monthly tiers and added a focused frontend test that locks `matchMonthlyTier(400)` to the paid-lite SKU.
- Added a stable admin subscription wallet marker (`data-hfc-wallet-marker="hasWalletBalance"`) so the stricter front/wallet anti-overwrite gate can detect the wallet display protection after frontend minification.
- This is frontend display/linkage only. It does not change production data, subscription fulfillment, billing, wallet ledger, monthly-card entitlement logic, model pricing, rate multipliers, group coverage, payment webhooks, LoadFactor, or account dispatch.

## 2026-06-03 - HFC safe-fusion Claude Code handoff

- Added `HFC_SAFE_FUSION_CC_HANDOFF_2026-06-03.md` as the canonical Claude Code handoff for the current official safe-fusion upgrade line.
- The handoff records the current branch, HEAD, completed safe-fusion areas, explicit HFC holds, production-deploy prohibition without fresh approval, and the required server/CI verification gates before any rollout.
- It also includes a paste-ready Claude Code task prompt so the next agent keeps HFC billing, wallet, monthly-card, ledger, pricing, multiplier, group coverage, LoadFactor, dispatch priority, production migration, and production data untouched unless Mark explicitly approves a business-rule change.
- No runtime code, production deploy, production data, migration, billing, wallet, model pricing, multiplier, payment, platform quota, group coverage, LoadFactor, or dispatch behavior changed.

## 2026-06-01 - HFC Image 2 safety P0 fusion and guard dry run

- Merged `29589052 Add Image 2 safety audit gate` into the local official-fusion branch as `17684267`, preserving HFC billing, wallet, subscription, daily/monthly cap, ledger, pricing, multiplier, scheduler, platform quota, and group-routing boundaries.
- Added a trusted content-moderation Base URL allowlist: OpenAI official endpoint, HFC-owned HTTPS domains, and loopback-only local proxies. Untrusted third-party hosts are rejected during admin config validation and runtime audit calls.
- Added `deploy/build_image.sh` source and binary marker guards so production-style candidate builds fail if Image 2 safety markers are missing from source or from `/app/sub2api` in the built image.
- Added shell/source marker guard coverage and Go tests for trusted/untrusted moderation Base URL behavior.
- Verification: `bash -n deploy/build_image.sh`, `bash -n deploy/test_build_image_retention.sh`, `bash deploy/test_build_image_retention.sh`, and `git diff --check` passed. Go tests were not run because this Mac has no `go`; Docker-based verification was not run because the Docker daemon is not running.
- No production deploy, production config change, DB change, image cutover, real image-generation request, billing change, wallet change, account-pool change, or risk control enablement was performed.

## 2026-05-30 - Codex tool-output WS continuation fusion

- Fused the official Codex tool-output recognition enhancement for OpenAI WS continuation.
- Continuation logic now treats `tool_search_output`, `custom_tool_call_output`, and `mcp_tool_call_output` like `function_call_output`, preserving `previous_response_id` / replay context when tool outputs need upstream response-chain continuity.
- This is protocol compatibility only; it does not change HFC billing, wallet ledger, token price, model multiplier, subscription caps, group routing, account priority, or scheduler selection.
- Added/expanded continuation tests for Codex tool-output and tool-call context item types.
- Verification: `git diff --check` passed. Backend Go tests were not run locally because this Mac does not have `go/gofmt`.

## 2026-05-30 - Claude thinking-block retry compatibility fusion

- Fused the official extended-thinking retry compatibility pattern for upstream errors like `each thinking block must contain thinking`.
- The existing retry rectifier can now recognize that error and use the already-present request-body sanitizer to drop invalid empty thinking blocks while preserving valid text content.
- This is a compatibility retry path only; it does not change token accounting, HFC billing, wallet ledger, model pricing, subscription caps, group routing, or scheduler priority.
- Added the official empty-thinking-block sanitizer test case.
- Verification: `git diff --check` passed. Backend Go tests were not run locally because this Mac does not have `go/gofmt`.

## 2026-05-30 - OAuth refresh_token_reused classification fusion

- Fused the official `refresh_token_reused` non-retryable OAuth refresh classification.
- OpenAI OAuth accounts with a reused refresh token now follow the existing invalid-credential path instead of retrying as a transient refresh failure.
- This is account-health classification only; it does not change HFC billing, wallet ledger, model pricing, subscription caps, request admission, group routing, or scheduler priority.
- Added the official classification case to the token refresh unit-test table.
- Verification: `git diff --check` passed. Backend Go tests were not run locally because this Mac does not have `go/gofmt`.

## 2026-05-30 - Scheduler account delete cache cleanup fusion

- Fused the official scheduler-cache cleanup behavior for deleted accounts.
- Account deletion now removes the single-account scheduler cache snapshot after the database delete succeeds, so sticky/session cache cannot keep a stale deleted account.
- This is cache cleanup only; it does not change account selection order, group routing, LoadFactor capacity weighting, scheduler priority, wallet/billing, model pricing, or request admission.
- Added an integration-test assertion path for the cache delete hook.
- Verification: `git diff --check` passed. Backend Go tests were not run locally because this Mac does not have `go/gofmt`.

## 2026-05-30 - Group account availability count fusion

- Fused the official group account count display fix while preserving HFC account dispatch behavior.
- Group detail/list counts now use the same availability filters as the existing schedulable account query: active, schedulable, not expired under auto-pause, not rate-limited, not overloaded, and not temp-unschedulable.
- This is admin display/data accuracy only; it does not change account selection order, group routing, LoadFactor capacity weighting, wallet/billing, model pricing, or request admission.
- Verification: `git diff --check` passed. Backend Go tests were not run locally because this Mac does not have `go`.

## 2026-05-30 - API Key daily usage detail fusion

- Fused the official API Key daily usage detail enhancement as read-only usage reporting.
- `/v1/usage` can now include `daily_usage` for explicit 7/30/90 day windows, and the user-owned API key daily usage endpoint verifies key ownership before returning rows.
- The key usage page now shows a daily detail table with requests, token breakdown, cache read/write, and actual cost; it only reads existing usage aggregates and does not alter HFC billing, wallet ledger, model pricing, rate multipliers, subscription caps, or request admission.
- Added backend handler tests for ownership, invalid day ranges, empty data, and aggregation mapping, plus a frontend view test for rendering daily usage rows.
- Preserved old `/v1/usage` behavior for clients that do not pass `days`, avoiding an extra daily-aggregation query on existing usage integrations.
- Verification: `git diff --check`, frontend `pnpm typecheck`, and targeted frontend `eslint` passed. Frontend Vitest was blocked by this Mac's Rollup native package code-signing error before tests started; backend Go tests were not run locally because this Mac does not have `go`.

## 2026-05-30 - HFC redeem batch update fusion

- Fused the official redeem-code batch edit operator workflow while preserving the HFC issue-based expiry rule.
- Admins can batch edit status, redeem-by time, notes, and subscription group from the management page; bulk updates reject type/value changes so redemption semantics and HFC billing logic stay unchanged.
- Used-code protections remain in place: bulk edits cannot change status, expiry, or group for already redeemed codes.
- Kept HFC stock-code behavior explicit in the UI: unissued stock codes do not count down; the 30-day redemption window starts when the external shop/admin issue endpoint marks the code as customer-issued.
- Verification: `git diff --check`, frontend `pnpm typecheck`, and targeted frontend `eslint` passed. Go tests were not run locally because this Mac does not have `go`; a unit test file was added for the service validation path and should be run in the server/Docker build environment before deployment.

## 2026-05-30 - Anthropic context-management beta sanitize fusion

- Fused official Anthropic `context_management` body/header compatibility fix into the HFC branch.
- Gateway now strips `body.context_management` before upstream forwarding when the final `anthropic-beta` header does not include `context-management-2025-06-27`, including direct Anthropic, API-key passthrough, Vertex, count_tokens, and Antigravity-compatible paths.
- Preserved HFC-specific count_tokens payload filtering, ops request body markers, CCH signing order, beta-policy filtering, Claude mimic headers, billing/wallet/subscription logic, model pricing, and account dispatch priority.
- Verification: `git diff --cached --check` passed. Backend Go tests were not run locally because this machine has no `go/gofmt`.

## 2026-05-30 - Content moderation async test stability fusion

- Fused official content-moderation WebSocket test stability fix into the HFC branch.
- The test-only content moderation log repo now uses a mutex and snapshot helper, and asynchronous moderation-log assertions wait with `require.Eventually`.
- Runtime behavior is unchanged; this does not touch request handling, billing, wallet, subscription, routing, moderation thresholds, or account dispatch.
- Verification: `git diff --check` passed. Backend Go tests were not run locally because this machine has no `go/gofmt`.

## 2026-05-30 - Usage request context preservation fusion

- Fused official request-correlation preservation into the HFC generic gateway usage-record path.
- Async usage-record tasks for Messages, Chat Completions, Responses, Gemini, Kiro, and Cursor now keep `client_request_id` / `request_id` from the inbound request context.
- `ClientRequestID` middleware now returns `X-Client-Request-ID` on generated and preserved IDs so client/admin logs can correlate the same request end to end.
- This is observability-only: it does not change usage numbers, token accounting, pricing, wallet ledger, monthly/daily caps, payment, model routing, or account dispatch priority.
- Verification: `git diff --check` passed. Backend Go tests were not run locally because this machine has no `go/gofmt`.

## 2026-05-30 - Registration email whitelist wildcard support

- Fused official registration email-domain whitelist wildcard support while keeping email registration as the only active login/register baseline.
- Exact domains keep the existing behavior; admins may now configure wildcard entries like `*.edu.cn` to match the base domain and subdomains.
- Updated backend/frontend normalization, user-facing allowed-domain messages, and Settings UI copy; escaped the i18n `@` placeholder to avoid Vue I18n linked-message parsing.
- Verification: frontend `registrationEmailPolicy.spec.ts` passed. `git diff --check` passed. Backend Go tests were not run locally because this machine has no `go/gofmt`.

## 2026-05-30 - HFC content moderation agent-loop dedupe

- Fused official content-moderation input extraction behavior for agent/tool-loop requests.
- Moderation now audits only the current last user message for chat/messages/responses/gemini payloads; if the latest turn is assistant/tool/function output, it skips re-auditing historical user text.
- This reduces duplicate moderation logs and external audit calls during tool loops while keeping first-turn and latest-user-turn moderation intact.
- Verification: `git diff --check` passed. Backend Go tests were not run locally because this machine has no `go/gofmt`.

## 2026-05-30 - HFC ops business-limit marker fusion

- Fused official ops business-limit marker behavior into HFC local rejection paths without changing request admission, billing, wallet, subscription, model pricing, or account dispatch rules.
- Added ops markers for API key IP restriction denials, ungrouped-key denials, local route feature gates, Anthropic beta policy blocks, Antigravity whitelist denials, and OpenAI fast-policy blocks across HTTP and WebSocket paths.
- Purpose: keep expected local/HFC policy denials visible in ops logs while excluding them from SLA/upstream-failure metrics.
- Verification: `git diff --check` passed. Backend Go tests were not run locally because this machine has no `go/gofmt`.

## 2026-05-30 - HFC group custom models list fusion

- Fused official group-level custom `/v1/models` display list into the HFC branch.
- Preserved HFC default `/v1/models` behavior and Claude Code model-list hiding; the custom list only applies when an admin explicitly enables `models_list_config`.
- Added `groups.models_list_config` migration and auth-cache snapshot version bump so API key group snapshots include the new display config.
- Added admin create/edit UI controls and a candidate-model API for configuring the display list.
- Verification: frontend `groupsModelsList*` Vitest specs passed and `vue-tsc --noEmit` passed. Backend Go tests were not run locally because this machine has no `go/gofmt` and Docker commands were not responding.

## 2026-05-30 - HFC official fusion safe cutover attempt

- Built candidate image `hfc/sub2api:official-fusion-6066bf5c-appchown-20260530-113519`.
- Fixed candidate runtime smoke by preserving `/app` ownership for the `sub2api` user in `Dockerfile`.
- Candidate preflight passed anti-overwrite frontend markers and candidate health checks.
- Production cutover was not completed because stream-drain timed out after 1800 seconds with active streaming requests.
- Production remained on `hfc/sub2api:long-request-resilience-4aedd982-20260530-030914` and stayed healthy.
- Full handoff report: `HFC_OFFICIAL_FUSION_CUTOVER_ATTEMPT_2026-05-30.md`.

## 2026-05-31 - HFC approved OpenAI WS safe fusion

- Fused the approved OpenAI Responses/WebSocket compatibility subset locally only; no production deployment, migration, or production data change was performed.
- API-key/OpenAI WS rate-limit failures now return `UpstreamFailoverError` before any downstream output, so the handler can switch to another eligible account for the same request while excluding only the failed account for this attempt.
- Follow-up WS `response.create` frames may omit `model`; the gateway reuses the last validated client model and writes a concrete upstream model before policy, mapping, and image-permission checks.
- Added display-only OpenAI endpoint compatibility hints to account create/edit modals. The panel explicitly holds embeddings behind HFC billing review and does not persist official endpoint capability/gating fields.
- Preserved HFC baselines: no billing, wallet, subscription cap, ledger, model pricing, rate multiplier, payment, platform quota, LoadFactor, or dispatch-priority changes.
- Verification: `git diff --check`, frontend `pnpm typecheck`, and frontend `pnpm lint:check` passed. Targeted Vitest was blocked by the existing Rollup native optional-dependency code-signing error; backend Go tests were not run locally because this machine has no `go/gofmt`.

## 2026-05-31 - HFC approved scheduler and OAuth compatibility fusion

- Fused another approved local-only compatibility slice; no production deployment, migration, or production data change was performed.
- OpenAI OAuth upstream requests that arrive with a browser-style `User-Agent` are normalized to the Codex CLI user agent for ChatGPT internal API compatibility. API-key upstream requests preserve the caller `User-Agent`.
- OpenAI WS v2 passthrough now carries the existing turn-state/turn-metadata headers and `prompt_cache_key` into the handshake path, matching the safer session-continuity behavior without changing usage accounting.
- Error-marked accounts are now also marked not schedulable so the scheduler skips them until an operator explicitly reviews/re-enables scheduling. This does not delete accounts, rewrite priorities, or change LoadFactor/group routing.
- Preserved HFC baselines: no billing, wallet, monthly/daily cap, ledger, model pricing, rate multiplier, payment, platform quota, embeddings, OAuth login, email, Airwallex, multi-currency, or endpoint-gating changes.
- Verification: `git diff --check` passed. Backend Go tests were not run locally because this machine has no `go/gofmt`; targeted tests were added for the new helper, OAuth/API-key user-agent behavior, and error unscheduling path.

## 2026-05-31 - HFC official v0.1.133 remaining upgrade report

- Rechecked `upstream-official/main` at `f18451e56f15b31ef602ab238037b56c3522b19f` / `v0.1.133` against the HFC safe-fusion branch.
- Consolidated remaining official upgrade gaps into `HFC_OFFICIAL_V0_1_133_REMAINING_UPGRADE_REPORT_2026-05-31.md`.
- Classified remaining items into: already fused/equivalent, strict HFC holds, candidates requiring isolated testing, and low-priority admin conveniences.
- No runtime code, database schema, migration, production data, image, deployment script, billing logic, wallet logic, model pricing, or account dispatch behavior was changed by this reporting step.

## 2026-05-31 - HFC OpenAI quota guard 5h/7d threshold

- Extended the existing HFC OpenAI quota guard from 7d-only to both 5h and 7d Codex usage windows.
- When either `codex_5h_used_percent` or `codex_7d_used_percent` reaches 95%, the OAuth OpenAI account becomes temporarily unschedulable until the corresponding reset time.
- If both windows are over threshold, the guard keeps the later reset time so weekly quota protection is not released early.
- This is account-pool protection only: no billing, wallet, monthly-card entitlement, ledger, model pricing, multiplier, payment, platform quota, group coverage, LoadFactor, or dispatch-priority logic was changed.

## 2026-06-01 - HFC monthly Opus 4.8 layout decision checklist

- Added `HFC_MONTHLY_PLAN_OPUS48_LAYOUT_DECISION_CHECKLIST_2026-06-01.md` to collect the monthly-plan improvement, page-layout, and decision items before implementation.
- Clarified that Opus 4.8 is intended to be opened, but must first use HFC-owned model IDs, pricing/multipliers, group coverage, model-log semantics, and page copy rather than official pricing/quota semantics.
- Updated the remaining-upgrade report so Opus 4.8 is no longer described as a blanket hold; it is now a pending HFC decision-and-layout item.
- This documentation-only step did not change runtime behavior, production data, migrations, billing, wallet, model pricing, multipliers, payment, platform quota, group coverage, LoadFactor, or dispatch priority.

## 2026-06-01 - HFC Opus 4.8 page display upgrade

- Added frontend display constants for `Claude Opus 4.8` and the HFC monthly-plan/account-protection notices.
- Added visible Opus 4.8 monthly-plan notices to purchase, subscription, renewal, admin plan edit, and dashboard quick-launch surfaces; the redeem page already carries the 30-day issue-time expiry reminder from the HFC redeem batch work.
- Added Opus 4.8 to the frontend Claude/Kiro/Cursor model candidates and dashboard quick launch while preserving GPT-5.5 as the default and Opus 4.7 as fallback.
- This is display/candidate work only: no billing, wallet, monthly-card logic, ledger, model price, multiplier, payment, platform quota, group coverage, LoadFactor, dispatch priority, production data, migration, or deployment changed.

## 2026-06-01 - HFC Opus 4.8 production read-only audit

- Audited production server, production database, public frontend assets, public `/v1/models`, and the current local safe-fusion branch for Opus 4.8 status.
- Confirmed production DB already has Opus 4.8 traffic: 6,770 matching `usage_logs` rows at audit time, from 2026-05-29 01:43:33 +08 to 2026-06-01 14:14:52 +08.
- Confirmed account `205 / destiny` has an Anthropic account-level mapping to `claude-opus-4-8-F`, while `channels.model_mapping` and `channel_model_pricing` do not explicitly contain Opus 4.8 entries.
- Confirmed the production worktree contains an Opus 4.8 backend patch, but the currently running image could not be proven to include it because `/app/sub2api` strings did not expose `claude-opus-4-8` and the image label lacks a usable git commit.
- Wrote the detailed audit report to `HFC_OPUS48_PRODUCTION_AUDIT_2026-06-01.md`.
- No production data, deployment, billing, wallet, monthly-card logic, model pricing, multiplier, payment, platform quota, group coverage, LoadFactor, or dispatch priority was changed.

## 2026-06-01 - HFC Claude Code Codex plugin and model mapping fusion

- Unified OpenAI OAuth Codex-compatible client detection so account-scoped Claude Code Codex plugin allow-list entries (`codex_cli_only_allowed_clients: ["claude_code"]`) now feed the same compatibility predicate used by HTTP `/responses`, OpenAI WS mode, and WS v2 passthrough.
- Preserved the HFC layering decision: the plugin only controls client compatibility and upstream request shaping; account-level `model_mapping` remains the business routing layer and still applies to plugin traffic.
- Preserved plugin client signatures for explicitly allowed Claude Code plugin traffic instead of rewriting the `User-Agent` back to `codex_cli_rs` in passthrough/WS paths.
- Added targeted backend tests for compatible-client detection, model-mapping preservation, passthrough signature preservation, and WS v2 signature preservation.
- No production data, deployment, billing, wallet, monthly-card logic, model pricing, multiplier, payment, platform quota, group coverage, LoadFactor, dispatch priority, or Opus 4.8 pricing/mapping policy was changed.

## 2026-06-01 - HFC OpenAI silent-refusal failover guard

- Fused the HFC-safe subset of the official OpenAI silent-refusal failover behavior for raw Chat Completions and Responses-to-Chat streaming paths.
- Large streaming requests now buffer empty assistant/finish-only prelude chunks until real content, tool calls, reasoning, usage, or an error appears; if upstream ends with `finish_reason=stop` and no content/usage, the service returns an `UpstreamFailoverError` before committing a 200 stream to the client.
- Failover-exhausted handlers now return a clear upstream-error message for this specific silent-refusal marker.
- This changes only pre-output retry eligibility and ops visibility. It does not change billing, wallet, monthly-card logic, ledger, model pricing, multipliers, payment, platform quota, group coverage, LoadFactor, dispatch priority, or usage-token extraction.
- Verification: `git diff --check` passed. Backend Go tests were not run locally because this machine has no `go/gofmt`.

## 2026-06-01 - HFC OpenAI runtime cooldown fast-path fusion

- Added a process-local OpenAI account runtime block fast path for HFC-safe cooldown signals: 429 rate limits, 529 overloads, OpenAI 403 temporary cooldowns, OAuth 401 temporary unscheduling, temporary-unschedulable rules, stream timeouts, privacy-required errors, and the HFC 95% Codex 5h/7d quota guard.
- OpenAI account selection now skips runtime-blocked accounts across previous-response sticky routing, session sticky routing, load-aware selection, fallback wait plans, and direct gateway selection.
- Admin clear-account-error and RateLimitService clear paths now clear the runtime scheduling block alongside rate-limit/temp-unschedulable state.
- This is only a skip/cooldown/clear fast path. It does not auto-delete accounts, does not change HFC LoadFactor weighting, does not rewrite dispatch priority, and does not touch billing, wallet, monthly-card logic, ledger, model pricing, multipliers, payment, platform quota, group coverage, production data, migrations, or deployment.
- Verification: `git diff --check` passed. Backend Go tests and `gofmt` were not run locally because this machine has no `go`/`gofmt`.

## 2026-06-01 - HFC OpenAI image moderation error passthrough fusion

- Fused the HFC-safe subset of the official OpenAI image moderation error surfacing change.
- OAuth Images/Responses non-streaming and streaming paths now detect upstream `error` / `response.failed` SSE payloads, including `moderation_blocked` and `image_generation_user_error`, and return a clear client-facing error type, code, and sanitized message instead of falling through to a generic "upstream did not return image output" failure.
- Ops upstream error context is set for these upstream moderation/user errors, and the images handler treats them as non-account-fault user/safety errors so they do not trigger account failover.
- This is visibility only: HFC Image 2 request/output audit, adult/minor/violence/image-generation high-risk policy, billing, wallet, monthly-card logic, ledger, model pricing, multipliers, payment, platform quota, group coverage, LoadFactor, dispatch priority, production data, migrations, and deployment were not changed.

## 2026-06-02 - HFC admin proxy resource link

- Fused the low-risk official admin proxy IP resource link as a compact frontend-only helper on proxy creation and account proxy selection surfaces.
- Added shared `ProxyAdBanner` plus Chinese/English copy; the link opens in a new tab with `noopener noreferrer`.
- This is a display-only admin convenience: no backend API, proxy credentials, account routing, billing, wallet, monthly-card logic, ledger, model pricing, multipliers, payment, platform quota, group coverage, LoadFactor, dispatch priority, production data, migrations, or deployment changed.

## 2026-06-02 - HFC Bedrock Claude Code compatibility toggle

- Fused a Bedrock-only Claude Code compatibility switch under channel feature config as `bedrock_cc_compat`.
- When enabled for an Anthropic channel and the selected account is actually Bedrock, Bedrock request preparation cleans up Claude Code shapes that AWS rejects: Opus 4.7+ `thinking.type=enabled` becomes `adaptive`, non-Opus-4.7 enabled thinking receives a default budget, and `tool_use.id` / `tool_result.tool_use_id` characters outside Bedrock's allowed set are normalized.
- The switch defaults off and is scoped to Bedrock request preparation only. No Anthropic/OpenAI main routing, billing, wallet, monthly-card logic, ledger, model pricing, multipliers, payment, platform quota, group coverage, LoadFactor, dispatch priority, production data, migrations, or deployment changed.
- Verification: `git diff --check`, `pnpm --dir frontend typecheck`, and `pnpm --dir frontend lint:check` passed. `pnpm --dir frontend build` was blocked by the local Rollup native optional dependency code-signature failure for `@rollup/rollup-darwin-arm64`. Backend Go tests and `gofmt` were not run locally because this machine has no `go`/`gofmt`.

## 2026-06-02 - HFC Claude Code mimic tool_use name compatibility

- Fused the safe request-shape subset of the official mimic tool-name compatibility fix.
- Claude Code mimic request rewriting now keeps `tools[]`, `tool_choice.name`, and historical `messages[].content[].name` tool_use blocks self-consistent after tool-name obfuscation, preventing Anthropic/Bedrock-style upstream 400 errors where a message references a tool name that is no longer declared in `tools[]`.
- The change is limited to request body compatibility before upstream forwarding. No billing, wallet, monthly-card logic, ledger, model pricing, multipliers, payment, platform quota, group coverage, LoadFactor, dispatch priority, production data, migrations, or deployment changed.
- Verification: `git diff --check`, `pnpm --dir frontend typecheck`, and `pnpm --dir frontend lint:check` passed. Backend Go tests and `gofmt` were not run locally because this machine has no `go`/`gofmt`.

## 2026-06-02 - HFC official safe-fusion offline completion pass

- Re-audited the local safe-fusion branch against `upstream-official/main` after the latest compatibility commits and updated `HFC_OFFICIAL_V0_1_133_REMAINING_UPGRADE_REPORT_2026-05-31.md` so it reflects the current state: approved local/offline safe-fusion candidates are handled; remaining official items are either explicit HFC holds or deployment-time verification gates.
- Confirmed the already-present safe subsets cover Gemini Messages tool-use stream ordering, `count_tokens` generation-field filtering, OpenAI WS terminal-event first-token protection, channel monitor API mode / Responses extraction, account created-at display, Claude Code mimic tool-name consistency, Bedrock CC compat toggle, image moderation surfacing, content audit observe/alert flow, and HFC runtime account cooldown / 95% guard boundaries.
- Explicitly preserved all HFC baselines: no production deploy, no production migration or data change, no payment/OAuth/email/Airwallex/multicurrency/platform-quota enablement, no embeddings opening, no official pricing override, no wallet/monthly-card/ledger/model-price/multiplier/group-coverage/LoadFactor/dispatch-priority rewrite.
- Verification: `git diff --check` passed. Frontend typecheck/lint were rerun for this pass. Backend Go tests and `gofmt` still require a server or CI environment with Go because this Mac does not expose `go`/`gofmt`; Docker availability is also not assumed for production readiness.

## 2026-06-04 - HFC safe-fusion progress recheck after CC work

- Rechecked the current candidate branch `codex/hfc-admin-wallet-ui-split-20260604` after CC/Codex follow-up work and wrote `HFC_SAFE_FUSION_PROGRESS_RECHECK_2026-06-04.md`.
- Confirmed CC/Codex commit `783dcd81` adds admin-only display of active subscription wallet summary beside recharge balance in the user balance-history modal. This is read-only display work and does not change billing, wallet ledger, monthly-card deduction, prices, multipliers, payment, platform quota, group coverage, LoadFactor, or dispatch priority.
- Cherry-picked the safe intent from CC branch commit `435236ce` into the current candidate as `5a1e0aea`, removing incorrect Opus 4.8 monthly-card banners from customer purchase, subscription, and renewal surfaces while keeping backend/admin/dashboard model display surfaces intact.
- Confirmed the active 4AM automation `hfc-4am-safe-upgrade-deployment-gate` exists as a safety-gated check: it should deploy only when all production gates pass, otherwise report blockers instead of blindly cutting over.
- Verification: `git diff --check`, `pnpm --dir frontend typecheck`, and `pnpm --dir frontend lint:check` passed. Targeted Vitest remains blocked by the local Rollup native optional dependency/code-signature issue. Backend Go tests and `gofmt` were not run locally because this Mac has no `go`/`gofmt`.

## 2026-06-04 - HFC frontend page and sidebar change review

- Wrote `HFC_FRONTEND_PAGE_NAV_CHANGE_REVIEW_2026-06-04.md` for Mark's frontend audit before deployment.
- Confirmed the current candidate does not change `frontend/src/components/layout/AppSidebar.vue` or `frontend/src/router/index.ts`; no personal sidebar menu entry is newly added by this candidate.
- Catalogued user-facing page changes: Dashboard balance CTA and shared recharge/renew modal, purchase-page redeem-code expiry reminder, subscription-page shared modal reuse, redeem-page localized error extraction, Opus 4.8 dashboard/model badge display, and wallet display helpers.
- Reconfirmed the removed Opus 4.8 customer-side monthly-card banners stay removed from purchase, subscription, and renewal surfaces; retained Opus 4.8 display only for backend/admin/dashboard/model-entry contexts.

## 2026-06-04 - HFC wallet renewal entry always visible

- Fixed the wallet dashboard renewal entry so users with an active wallet monthly card can always see a `Renew` / `续费` button, even when the remaining wallet balance is still above the low-balance warning threshold.
- Kept the low-balance / exhausted warning as a reminder only; it no longer controls whether the renewal entry exists.
- Added a frontend marker `data-hfc-renew-entry="wallet"` and a targeted component test covering both normal-balance and low-balance wallet states.
- This is frontend display/entry behavior only. It does not change billing, wallet ledger, monthly-card deduction, prices, multipliers, payment callback handling, platform quota, group coverage, LoadFactor, dispatch priority, production data, or migrations.

## 2026-06-05 - HFC paid-lite subscription group sync

- Synced production admin subscription assignment data for the 99 yuan paid-lite tier by adding/updating `paid-lite-v3` as an active subscription group with 400 USD monthly quota, 50 USD daily cap, and sort order between `paid-trial-v3` and `paid-standard-v3`.
- Corrected `paid-lite-v3-30d` plan coverage so the plan maps to `paid-lite-v3` instead of `paid-trial-v3`, while preserving its shared coverage groups `cc-default`, `openai-default`, `gemini-default`, and `cc-antigravity`.
- Added `deploy/sync_paid_lite_subscription_group_20260605.sql` as an idempotent production replay script for the admin subscription-group sync.
- Production verification: groups query showed `paid-lite-v3` active at sort order 102, `paid-lite-v3-30d` remained price 99 / wallet quota 400 / for_sale=true / sort order 2, public admin/API probes returned 200, and the anti-overwrite gate plus monthly billing smoke passed with the dedicated zero-balance monthly test account still at balance 0.00000000.

## 2026-06-01 - HFC GPT-group Image2 production recovery

- Cleaned content-moderation audit non-hit policy names so allowed input/output audit rows record `moderation_pass_input` / `moderation_pass_output` instead of legacy `moderation_flagged_*` fallback labels.
- Enabled the production Codex `/responses` image-generation bridge with `GATEWAY_CODEX_IMAGE_GENERATION_BRIDGE_ENABLED=true`, so Codex official clients receive the native `image_generation` tool and bridge instructions when the API key group allows image generation.
- Recovered a stuck production cutover by returning `sub2api` to a healthy container, then deployed the running `hfc/sub2api:admin-wallet-display-82ab0ca0-20260601-215803` image with the Image2 audit fixes and bridge environment.
- Customer key canaries passed without exposing the full key: direct `/v1/images/generations` using `gpt-image-2` returned HTTP 200 with one b64 image, and Codex-style `/responses` returned HTTP 200 with one `image_generation_call` and image result.
- Production billing and audit evidence: customer usage rows billed to subscription `157`; direct Image2 cost recorded as `0.0500000000`, Responses bridge image cost as `0.1000000000`; content moderation logs recorded `moderation_pass_input` and `moderation_pass_output`.
- Anti-overwrite gate passed on the running image: wallet markers passed, monthly billing smoke passed with zero-balance monthly test account still at balance `0.00000000`; the `sub2api-monthly-billing-smoke.timer` remained active/enabled and the 22:46 CST automatic run also passed.
- Verification: production HTTP `/health` returned 200 after the final restart, bridge injection logs showed `/responses image_generation` tool injection and bridge instructions, and `git diff --check` passed locally. Backend Go tests and `gofmt` were not run locally because this machine has no `go`/`gofmt`; the production canaries above cover the live customer path.
