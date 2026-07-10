# Second-phase Task 2A report: durable usage billing outbox core

Date: 2026-07-10

Base: `0c2520b1`

Scope: Task 2A only. This change adds the durable schema, immutable envelope,
PostgreSQL repository, replay processor, ownership validation and tests. Handler,
producer, worker lifecycle/Wire registration, WebSocket turn IDs and queue cutover
remain Task 2B and are not claimed complete here.

## Implemented

- Forward-only migration `174_add_usage_billing_outbox.sql`:
  - dedicated `usage_billing_outbox` table;
  - unique `(request_id, api_key_id)`;
  - pending/retry/completed/dead-letter lifecycle;
  - bounded attempts, per-claim fencing token, final-attempt crash recovery and partial claim/DLQ indexes;
  - no business foreign keys.
- Versioned immutable `UsageBillingEnvelope`:
  - explicit IDs, final billing model, token/image facts, price evidence,
    multipliers and exact billing side effects;
  - no arbitrary maps or request/client credential fields;
  - canonical fingerprint and defensive pointer copies;
  - validation for IDs, versions, finite/non-negative values, incompatible billing
    paths and fingerprint tampering.
- PostgreSQL outbox repository:
  - same-key/same-fingerprint enqueue idempotency;
  - an already-durable event remains idempotent after target soft-delete, while a different fingerprint still fails with a typed conflict;
  - typed conflict without payload overwrite;
  - `FOR UPDATE SKIP LOCKED` claim with a 30-second logical lease and random fencing token;
  - owner-and-token-matched completion/retry/dead-letter transitions;
  - enqueue-time row locks for API-key/user/group, routed account/group and subscription billing mode;
  - fixed-group keys and real `group_id=NULL` wallet universal keys are validated separately.
- Processor:
  - fixed order `ValidateBindings -> UsageBillingRepository.Apply -> replay writer -> Complete`;
  - calls `UsageBillingRepository.Apply` directly and has no legacy billing fallback;
  - bounded deterministic retry backoff;
  - poison/version/fingerprint/cross-tenant direct DLQ;
  - max-attempt DLQ;
  - acknowledgement failure leaves the lease for replay through billing dedup.
  - batch processing continues after one event fails, so already-claimed siblings do not silently consume attempts;
  - persisted errors are allow-listed and never store raw database/request error text.
- Frozen replay commands can complete balance, API-key/account quota, monthly and wallet effects after a target is soft-deleted; direct non-outbox billing keeps its existing soft-delete behavior.
- Narrow `UsageBillingReplayWriter` seam for Task 2B's concrete idempotent usage-log
  writer. The Task 2A integration test proves the required ordering and replay
  behavior without wiring handlers prematurely.

## TDD evidence

### RED

1. Envelope:

```text
/Users/markdonish/.local/share/go/go1.26.3/bin/go test ./internal/service -run '^TestUsageBillingEnvelope_' -count=1
```

Failed at compile time because `UsageBillingEnvelopeInput`, constructor, decoder
and typed validation errors did not exist.

2. Migration on real PostgreSQL:

```text
/Users/markdonish/.local/share/go/go1.26.3/bin/go test -tags=integration ./internal/repository -run '^TestUsageBillingOutboxMigration_SchemaAndIndexes$' -count=1
```

Failed with `expected usage_billing_outbox table to exist`.

3. Repository:

```text
/Users/markdonish/.local/share/go/go1.26.3/bin/go test -tags=integration ./internal/repository -run '^TestUsageBillingOutboxRepository_' -count=1
```

Failed at compile time because the repository constructor, lifecycle constants,
lease and stale-owner error did not exist.

4. Processor:

```text
/Users/markdonish/.local/share/go/go1.26.3/bin/go test ./internal/service -run '^TestUsageBillingOutboxProcessor_' -count=1
```

Failed at compile time because the processor and poison-envelope event field did
not exist.

### GREEN

Envelope and processor unit tests:

```text
/Users/markdonish/.local/share/go/go1.26.3/bin/go test ./internal/service -run 'TestUsageBilling(Envelope|OutboxProcessor)' -count=1
```

Result: PASS.

Broader service/repository unit tests:

```text
/Users/markdonish/.local/share/go/go1.26.3/bin/go test ./internal/service ./internal/repository -count=1
```

Latest result: PASS (`service 44.424s`, `repository 3.046s`).

Targeted real PostgreSQL/Redis Testcontainers integration:

```text
/Users/markdonish/.local/share/go/go1.26.3/bin/go test -tags=integration ./internal/repository -run 'TestUsageBillingOutbox(Migration|Repository|ProcessorIntegration)' -count=1
```

Latest result: PASS (`3.738s`). Covered schema/index/checks, strict outer-envelope
identity, enqueue idempotency/conflict, `SKIP LOCKED`, lease expiry, same-owner
fencing, final-attempt reclaim, retry/DLQ, fixed-group and universal-wallet binding
matrices, apply-commit then ack-crash replay, soft-deleted targets, one balance
deduction, one usage log, monthly counters and wallet deduction/ledger semantics
with frozen subscription IDs.

Formatting/diff gate:

```text
git diff --check
```

Result: PASS.

Independent completion reviews:

- Code review: PASS after closing the universal wallet-key and post-delete enqueue idempotency gaps.
- Security/data-integrity review: PASS. No blocking finding; the remaining non-blocking risk is the lack of lease renewal for unusually long processing, which is contained by billing dedup and must remain idempotent in Task 2B's replay writer/finalizer.

## Security and data-integrity boundaries

- Envelope JSON has a strict allow-list and tests assert absence of API key
  material, credentials, cookies, headers, bodies, prompts, messages, tools, IP,
  User-Agent and request payload hashes.
- SQL values are parameterized. Dynamic SQL fragments are internal constants,
  not external input.
- Cross-tenant API key/user/group/account and subscription billing-mode bindings fail atomically at enqueue.
- A `group_id=NULL` key is accepted only when its exact name marks the wallet universal-key contract and the frozen subscription is a real wallet row; arbitrary unbound keys and universal-key/monthly combinations fail closed.
- Lease completion uses a random fencing token in addition to owner identity; a stale goroutine cannot complete a later lease held by the same process owner.
- `last_error` contains a fixed safe message while typed `last_error_code` retains the actionable class.
- Poison payloads remain durable as dead-letter rows; completed/dead rows are not
  automatically deleted.
- Migration 172 and 173 were not modified.
- The private production 503 report was not read, modified or staged.

## Residual scope: Task 2B

Task 2A does not make live gateway billing durable by itself. Task 2B must:

- prepare and persist the envelope before any in-memory worker submission;
- cut all billable handler paths to outbox IDs only, with no drop/sample loss;
- provide the concrete idempotent usage-log replay writer;
- register processor lifecycle/Wire startup and shutdown;
- assign deterministic per-turn WebSocket request IDs;
- prove queue saturation, restart recovery and all handler/WS entrypoints.

Production remains `NO-GO` until Task 2B and the remaining second-phase tasks are
implemented and verified.
