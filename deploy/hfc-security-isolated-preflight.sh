#!/usr/bin/env bash
# Run the HFC security release candidate against a restored production snapshot.
# The candidate, PostgreSQL, and Redis are attached to a new internal-only
# Docker network. Nothing in this preflight mutates the live database or the
# live Sub2API data directory.

set -euo pipefail

IMAGE="${1:-}"
EXPECTED_REVISION="${2:-}"
LIVE_APP_CONTAINER="${HFC_PREFLIGHT_LIVE_APP_CONTAINER:-sub2api}"
LIVE_DB_CONTAINER="${HFC_PREFLIGHT_LIVE_DB_CONTAINER:-sub2api-postgres}"
LIVE_REDIS_CONTAINER="${HFC_PREFLIGHT_LIVE_REDIS_CONTAINER:-sub2api-redis}"
LIVE_DATA_DIR="${HFC_PREFLIGHT_LIVE_DATA_DIR:-/opt/relay/ai-relay-infra/sub2api/data}"
SECURITY_ENV_FILE="${HFC_PREFLIGHT_SECURITY_ENV_FILE:-/opt/relay/ai-relay-infra/sub2api/.env}"
API_KEY_CANARY_FILE="${HFC_PREFLIGHT_API_KEY_CANARY_FILE:-/etc/hfc-secrets/sub2api-credits-billing-smoke-api-key}"
BACKUP_ROOT="${HFC_PREFLIGHT_BACKUP_ROOT:-/var/backups}"
WORK_ROOT="${HFC_PREFLIGHT_WORK_ROOT:-/opt/relay/deploy-candidates}"
TIMEOUT_SECONDS="${HFC_PREFLIGHT_TIMEOUT_SECONDS:-300}"

if [ -z "$IMAGE" ]; then
  printf 'usage: %s hfc/sub2api:<tag> [expected-revision]\n' "$0" >&2
  exit 2
fi

for command_name in docker sha256sum awk sed grep cmp date openssl stat; do
  command -v "$command_name" >/dev/null 2>&1 || {
    printf 'REFUSE: missing command: %s\n' "$command_name" >&2
    exit 1
  }
done

if [ ! -f "$API_KEY_CANARY_FILE" ] || [ -L "$API_KEY_CANARY_FILE" ]; then
  printf 'REFUSE: API key canary must be a regular non-symlink file: %s\n' "$API_KEY_CANARY_FILE" >&2
  exit 1
fi
case "$(stat -c '%a' "$API_KEY_CANARY_FILE")" in
  400|600) ;;
  *)
    printf 'REFUSE: API key canary permissions must be 400 or 600\n' >&2
    exit 1
    ;;
esac
if [ "$(stat -c '%U:%G' "$API_KEY_CANARY_FILE")" != "root:root" ]; then
  printf 'REFUSE: API key canary must be owned by root:root\n' >&2
  exit 1
fi
API_KEY_CANARY="$(awk 'NR == 1 { print; found=1 } NR > 1 { extra=1 } END { if (!found || extra) exit 1 }' "$API_KEY_CANARY_FILE")" || {
  printf 'REFUSE: API key canary file must contain exactly one line\n' >&2
  exit 1
}
if [ -z "$API_KEY_CANARY" ] || [[ "$API_KEY_CANARY" == *[[:space:]]* ]]; then
  printf 'REFUSE: API key canary must be non-empty and contain no whitespace\n' >&2
  exit 1
fi

SECURITY_ENV_KEYS=(
  SECRET_ENCRYPTION_TOTP_SECRET_KEY
  SECRET_ENCRYPTION_TOTP_CACHE_KEY
  SECRET_ENCRYPTION_ACCOUNT_CREDENTIAL_KEY
  SECRET_ENCRYPTION_BACKUP_S3_KEY
  SECRET_ENCRYPTION_CONTENT_MODERATION_KEY
  SECRET_ENCRYPTION_CHANNEL_MONITOR_KEY
  SECRET_ENCRYPTION_PAYMENT_PROVIDER_KEY
  SECRET_ENCRYPTION_PROXY_CREDENTIAL_KEY
  SECRET_ENCRYPTION_SCHEDULER_CACHE_KEY
  SECRET_ENCRYPTION_OAUTH_TOKEN_CACHE_KEY
  SECRET_ENCRYPTION_JWT_HMAC_KEY
  SECRET_ENCRYPTION_SETTING_SECRET_KEY
  API_KEY_ENCRYPTION_KEY
  PAYMENT_RESUME_SIGNING_KEY
)

if [ ! -f "$SECURITY_ENV_FILE" ] || [ -L "$SECURITY_ENV_FILE" ]; then
  printf 'REFUSE: security env must be a regular non-symlink file: %s\n' "$SECURITY_ENV_FILE" >&2
  exit 1
fi
case "$(stat -c '%a' "$SECURITY_ENV_FILE")" in
  400|600) ;;
  *)
    printf 'REFUSE: security env permissions must be 400 or 600\n' >&2
    exit 1
    ;;
esac
for key in "${SECURITY_ENV_KEYS[@]}"; do
  count="$(awk -F= -v key="$key" '$1 == key { count++ } END { print count+0 }' "$SECURITY_ENV_FILE")"
  if [ "$count" -ne 1 ]; then
    printf 'REFUSE: %s must occur exactly once in security env\n' "$key" >&2
    exit 1
  fi
  value="$(awk -F= -v key="$key" '$1 == key { print substr($0, index($0, "=")+1) }' "$SECURITY_ENV_FILE")"
  if [[ ! "$value" =~ ^[0-9a-fA-F]{64}$ ]]; then
    printf 'REFUSE: %s must be 64 hex characters\n' "$key" >&2
    exit 1
  fi
done

case "$TIMEOUT_SECONDS" in
  ''|*[!0-9]*)
    printf 'REFUSE: HFC_PREFLIGHT_TIMEOUT_SECONDS must be a positive integer\n' >&2
    exit 1
    ;;
esac
if [ "$TIMEOUT_SECONDS" -le 0 ]; then
  printf 'REFUSE: HFC_PREFLIGHT_TIMEOUT_SECONDS must be a positive integer\n' >&2
  exit 1
fi

docker image inspect "$IMAGE" >/dev/null 2>&1 || {
  printf 'REFUSE: candidate image is not loaded: %s\n' "$IMAGE" >&2
  exit 1
}

for container_name in "$LIVE_APP_CONTAINER" "$LIVE_DB_CONTAINER" "$LIVE_REDIS_CONTAINER"; do
  docker inspect "$container_name" >/dev/null 2>&1 || {
    printf 'REFUSE: live dependency is missing: %s\n' "$container_name" >&2
    exit 1
  }
done

LIVE_APP_HEALTH="$(docker inspect "$LIVE_APP_CONTAINER" --format '{{if .State.Health}}{{.State.Health.Status}}{{else}}{{.State.Status}}{{end}}')"
if [ "$LIVE_APP_HEALTH" != "healthy" ]; then
  printf 'REFUSE: live application is not healthy: %s\n' "$LIVE_APP_HEALTH" >&2
  exit 1
fi

IMAGE_ARCH="$(docker image inspect "$IMAGE" --format '{{.Os}}/{{.Architecture}}')"
IMAGE_ID="$(docker image inspect "$IMAGE" --format '{{.Id}}')"
IMAGE_REVISION="$(docker image inspect "$IMAGE" --format '{{index .Config.Labels "org.opencontainers.image.revision"}}')"
if [ "$IMAGE_ARCH" != "linux/amd64" ]; then
  printf 'REFUSE: candidate architecture must be linux/amd64, got %s\n' "$IMAGE_ARCH" >&2
  exit 1
fi
if [ -n "$EXPECTED_REVISION" ] && [ "$IMAGE_REVISION" != "$EXPECTED_REVISION" ]; then
  printf 'REFUSE: candidate revision mismatch\n' >&2
  exit 1
fi

RUN_STAMP="$(date -u +%Y%m%dT%H%M%SZ)"
RUN_ID="hfc-security-preflight-${RUN_STAMP}-$$"
BACKUP_DIR="$BACKUP_ROOT/$RUN_ID"
WORK_DIR="$WORK_ROOT/$RUN_ID"
DUMP_FILE="$BACKUP_DIR/sub2api-production-snapshot.dump"
SUMMARY_FILE="$BACKUP_DIR/preflight-summary.txt"
LOG_FILE="$BACKUP_DIR/candidate.log"
ENV_FILE="$WORK_DIR/candidate.env"
DATA_DIR="$WORK_DIR/data"
NETWORK="${RUN_ID}-net"
PG_CONTAINER="${RUN_ID}-postgres"
REDIS_CONTAINER="${RUN_ID}-redis"
CANDIDATE_CONTAINER="${RUN_ID}-candidate"
PG_VOLUME="${RUN_ID}-pgdata"
PG_USER="sub2api_preflight"
PG_DB="sub2api"
PG_PASSWORD="$(openssl rand -hex 32)"
SUCCESS=0

mkdir -p "$BACKUP_DIR" "$WORK_DIR" "$DATA_DIR"
chmod 700 "$BACKUP_DIR" "$WORK_DIR" "$DATA_DIR"

cleanup_container() {
  local name="$1"
  if docker inspect "$name" >/dev/null 2>&1; then
    if [ "$(docker inspect "$name" --format '{{.State.Running}}')" = "true" ]; then
      docker stop --time 30 "$name" >/dev/null 2>&1 || true
    fi
    docker rm "$name" >/dev/null 2>&1 || true
  fi
}

cleanup() {
  local exit_code=$?
  cleanup_container "$CANDIDATE_CONTAINER"
  cleanup_container "$REDIS_CONTAINER"
  cleanup_container "$PG_CONTAINER"
  docker network rm "$NETWORK" >/dev/null 2>&1 || true
  docker volume rm "$PG_VOLUME" >/dev/null 2>&1 || true
  rm -f "$ENV_FILE"
  rm -rf "$WORK_DIR"
  if [ "$SUCCESS" -ne 1 ]; then
    printf 'preflight_result=failed\n' >> "$SUMMARY_FILE"
    printf 'evidence_dir=%s\n' "$BACKUP_DIR" >&2
  fi
  exit "$exit_code"
}
trap cleanup EXIT

printf 'phase=production_snapshot\n'
docker exec "$LIVE_DB_CONTAINER" sh -ec \
  'PGPASSWORD="$POSTGRES_PASSWORD" exec pg_dump --format=custom --no-owner --no-acl --username="$POSTGRES_USER" --dbname="$POSTGRES_DB"' \
  > "$DUMP_FILE.tmp"
chmod 600 "$DUMP_FILE.tmp"
if [ ! -s "$DUMP_FILE.tmp" ]; then
  printf 'REFUSE: production snapshot is empty\n' >&2
  exit 1
fi
mv "$DUMP_FILE.tmp" "$DUMP_FILE"
sha256sum "$DUMP_FILE" > "$BACKUP_DIR/SHA256SUMS"
chmod 600 "$BACKUP_DIR/SHA256SUMS"

POSTGRES_IMAGE="$(docker inspect "$LIVE_DB_CONTAINER" --format '{{.Config.Image}}')"
REDIS_IMAGE="$(docker inspect "$LIVE_REDIS_CONTAINER" --format '{{.Config.Image}}')"

printf 'phase=isolated_dependencies\n'
docker network create --internal "$NETWORK" >/dev/null
docker volume create "$PG_VOLUME" >/dev/null
docker run -d \
  --name "$PG_CONTAINER" \
  --network "$NETWORK" \
  --network-alias postgres \
  -e "POSTGRES_USER=$PG_USER" \
  -e "POSTGRES_PASSWORD=$PG_PASSWORD" \
  -e "POSTGRES_DB=$PG_DB" \
  -v "$PG_VOLUME:/var/lib/postgresql" \
  "$POSTGRES_IMAGE" >/dev/null

deadline=$(( $(date +%s) + TIMEOUT_SECONDS ))
until docker exec "$PG_CONTAINER" sh -ec \
  'psql --username="$POSTGRES_USER" --dbname="$POSTGRES_DB" --tuples-only --no-align --command "SELECT 1"' \
  2>/dev/null | grep -qx 1; do
  if [ "$(date +%s)" -ge "$deadline" ]; then
    printf 'REFUSE: isolated PostgreSQL did not become ready\n' >&2
    exit 1
  fi
  sleep 2
done

docker exec -i "$PG_CONTAINER" sh -ec \
  'PGPASSWORD="$POSTGRES_PASSWORD" exec pg_restore --exit-on-error --no-owner --no-acl --username="$POSTGRES_USER" --dbname="$POSTGRES_DB"' \
  < "$DUMP_FILE"

docker run -d \
  --name "$REDIS_CONTAINER" \
  --network "$NETWORK" \
  --network-alias redis \
  "$REDIS_IMAGE" redis-server --save '' --appendonly no >/dev/null

deadline=$(( $(date +%s) + TIMEOUT_SECONDS ))
until docker exec "$REDIS_CONTAINER" redis-cli ping 2>/dev/null | grep -qx PONG; do
  if [ "$(date +%s)" -ge "$deadline" ]; then
    printf 'REFUSE: isolated Redis did not become ready\n' >&2
    exit 1
  fi
  sleep 1
done

printf 'phase=candidate_data_clone\n'
cp -a "$LIVE_DATA_DIR/." "$DATA_DIR/"

# Reuse the live process's security keys without printing them. Every mutable
# dependency and every externally active feature is overridden below.
docker inspect "$LIVE_APP_CONTAINER" --format '{{range .Config.Env}}{{println .}}{{end}}' \
  | awk -F= '
    $1 !~ /^(DATABASE_HOST|DATABASE_PORT|DATABASE_USER|DATABASE_PASSWORD|DATABASE_DBNAME|DATABASE_SSLMODE|DATABASE_MAX_OPEN_CONNS|DATABASE_MAX_IDLE_CONNS|REDIS_HOST|REDIS_PORT|REDIS_PASSWORD|REDIS_DB|REDIS_ENABLE_TLS|REDIS_POOL_SIZE|REDIS_MIN_IDLE_CONNS|SERVER_HOST|SERVER_PORT|TOKEN_REFRESH_ENABLED|OPS_ENABLED|OPS_CLEANUP_ENABLED|OPS_AGGREGATION_ENABLED|DASHBOARD_AGGREGATION_ENABLED|USAGE_CLEANUP_ENABLED|KIRO_ENABLED|KIRO_ROUTE_ENABLED|CURSOR_ENABLED|CURSOR_ROUTE_ENABLED|LINUXDO_CONNECT_ENABLED|WECHAT_CONNECT_ENABLED|WECHAT_CONNECT_OPEN_ENABLED|WECHAT_CONNECT_MP_ENABLED|WECHAT_CONNECT_MOBILE_ENABLED|OIDC_CONNECT_ENABLED|GITHUB_OAUTH_ENABLED|GOOGLE_OAUTH_ENABLED|LOG_OUTPUT_TO_FILE|SECRET_ENCRYPTION_TOTP_SECRET_KEY|SECRET_ENCRYPTION_TOTP_CACHE_KEY|SECRET_ENCRYPTION_ACCOUNT_CREDENTIAL_KEY|SECRET_ENCRYPTION_BACKUP_S3_KEY|SECRET_ENCRYPTION_CONTENT_MODERATION_KEY|SECRET_ENCRYPTION_CHANNEL_MONITOR_KEY|SECRET_ENCRYPTION_PAYMENT_PROVIDER_KEY|SECRET_ENCRYPTION_PROXY_CREDENTIAL_KEY|SECRET_ENCRYPTION_SCHEDULER_CACHE_KEY|SECRET_ENCRYPTION_OAUTH_TOKEN_CACHE_KEY|SECRET_ENCRYPTION_JWT_HMAC_KEY|SECRET_ENCRYPTION_SETTING_SECRET_KEY|API_KEY_ENCRYPTION_KEY|PAYMENT_RESUME_SIGNING_KEY|PAYMENT_RESUME_LEGACY_VERIFY_UNTIL|SUB2API_OFFICIAL_UPDATE_APPLY_ENABLED|HFC_PREFLIGHT_API_KEY_CANARY)$/ { print }
  ' > "$ENV_FILE"
chmod 600 "$ENV_FILE"
awk -F= '
  BEGIN {
    split("SECRET_ENCRYPTION_TOTP_SECRET_KEY SECRET_ENCRYPTION_TOTP_CACHE_KEY SECRET_ENCRYPTION_ACCOUNT_CREDENTIAL_KEY SECRET_ENCRYPTION_BACKUP_S3_KEY SECRET_ENCRYPTION_CONTENT_MODERATION_KEY SECRET_ENCRYPTION_CHANNEL_MONITOR_KEY SECRET_ENCRYPTION_PAYMENT_PROVIDER_KEY SECRET_ENCRYPTION_PROXY_CREDENTIAL_KEY SECRET_ENCRYPTION_SCHEDULER_CACHE_KEY SECRET_ENCRYPTION_OAUTH_TOKEN_CACHE_KEY SECRET_ENCRYPTION_JWT_HMAC_KEY SECRET_ENCRYPTION_SETTING_SECRET_KEY API_KEY_ENCRYPTION_KEY PAYMENT_RESUME_SIGNING_KEY PAYMENT_RESUME_LEGACY_VERIFY_UNTIL SUB2API_OFFICIAL_UPDATE_APPLY_ENABLED", names, " ")
    for (i in names) wanted[names[i]]=1
  }
  $1 in wanted { print }
' "$SECURITY_ENV_FILE" >> "$ENV_FILE"
printf '%s\n' \
  'DATABASE_HOST=postgres' \
  'DATABASE_PORT=5432' \
  "DATABASE_USER=$PG_USER" \
  "DATABASE_PASSWORD=$PG_PASSWORD" \
  "DATABASE_DBNAME=$PG_DB" \
  'DATABASE_SSLMODE=disable' \
  'DATABASE_MAX_OPEN_CONNS=32' \
  'DATABASE_MAX_IDLE_CONNS=8' \
  'REDIS_HOST=redis' \
  'REDIS_PORT=6379' \
  'REDIS_PASSWORD=' \
  'REDIS_DB=0' \
  'REDIS_ENABLE_TLS=false' \
  'REDIS_POOL_SIZE=64' \
  'REDIS_MIN_IDLE_CONNS=4' \
  'SERVER_HOST=0.0.0.0' \
  'SERVER_PORT=8080' \
  'TOKEN_REFRESH_ENABLED=false' \
  'OPS_ENABLED=false' \
  'OPS_CLEANUP_ENABLED=false' \
  'OPS_AGGREGATION_ENABLED=false' \
  'DASHBOARD_AGGREGATION_ENABLED=false' \
  'USAGE_CLEANUP_ENABLED=false' \
  'KIRO_ENABLED=false' \
  'KIRO_ROUTE_ENABLED=false' \
  'CURSOR_ENABLED=false' \
  'CURSOR_ROUTE_ENABLED=false' \
  'LINUXDO_CONNECT_ENABLED=false' \
  'WECHAT_CONNECT_ENABLED=false' \
  'WECHAT_CONNECT_OPEN_ENABLED=false' \
  'WECHAT_CONNECT_MP_ENABLED=false' \
  'WECHAT_CONNECT_MOBILE_ENABLED=false' \
  'OIDC_CONNECT_ENABLED=false' \
  'GITHUB_OAUTH_ENABLED=false' \
  'GOOGLE_OAUTH_ENABLED=false' \
  'LOG_OUTPUT_TO_FILE=false' \
  >> "$ENV_FILE"
printf 'HFC_PREFLIGHT_API_KEY_CANARY=%s\n' "$API_KEY_CANARY" >> "$ENV_FILE"

printf 'phase=candidate_start\n'
docker run -d \
  --name "$CANDIDATE_CONTAINER" \
  --network "$NETWORK" \
  --env-file "$ENV_FILE" \
  -v "$DATA_DIR:/app/data" \
  "$IMAGE" >/dev/null

deadline=$(( $(date +%s) + TIMEOUT_SECONDS ))
while true; do
  candidate_running="$(docker inspect "$CANDIDATE_CONTAINER" --format '{{.State.Running}}')"
  candidate_health="$(docker inspect "$CANDIDATE_CONTAINER" --format '{{if .State.Health}}{{.State.Health.Status}}{{else}}{{.State.Status}}{{end}}')"
  if [ "$candidate_running" != "true" ]; then
    docker logs "$CANDIDATE_CONTAINER" > "$LOG_FILE" 2>&1 || true
    chmod 600 "$LOG_FILE"
    printf 'REFUSE: candidate exited before becoming healthy; protected log=%s\n' "$LOG_FILE" >&2
    exit 1
  fi
  if [ "$candidate_health" = "healthy" ]; then
    break
  fi
  if [ "$(date +%s)" -ge "$deadline" ]; then
    docker logs "$CANDIDATE_CONTAINER" > "$LOG_FILE" 2>&1 || true
    chmod 600 "$LOG_FILE"
    printf 'REFUSE: candidate health check timed out; protected log=%s\n' "$LOG_FILE" >&2
    exit 1
  fi
  sleep 2
done

docker logs "$CANDIDATE_CONTAINER" > "$LOG_FILE" 2>&1
chmod 600 "$LOG_FILE"
docker exec "$CANDIDATE_CONTAINER" sh -ec 'wget -q -T 5 -O - http://127.0.0.1:8080/health >/dev/null'
printf 'phase=candidate_api_key_auth\n'
docker exec "$CANDIDATE_CONTAINER" sh -ec '
  response=/tmp/hfc-preflight-auth-body
  headers=/tmp/hfc-preflight-auth-headers
  rm -f "$response" "$headers"
  wget -S -T 10 -O "$response" \
    --header="x-api-key: ${HFC_PREFLIGHT_API_KEY_CANARY}" \
    --header="content-type: application/json" \
    --post-data="{" \
    http://127.0.0.1:8080/v1/messages 2>"$headers" || true
  grep -Eq "HTTP/[0-9.]+ 400" "$headers"
  ! grep -Eq "HTTP/[0-9.]+ (401|403)" "$headers"
  rm -f "$response" "$headers"
'

EXPECTED_MIGRATIONS_FILE="$WORK_DIR/expected-migrations.txt"
ACTUAL_MIGRATIONS_FILE="$WORK_DIR/actual-migrations.txt"
printf '%s\n' \
  '175_wallet_ledger_integrity.sql' \
  '175a_retire_legacy_monthly_billing_smoke.sql' \
  '176_enforce_monthly_group_not_wallet.sql' \
  '177_allow_wallet_postpaid_settlement.sql' \
  '178_wallet_integrity_indexes_notx.sql' \
  '179_usage_billing_admissions.sql' \
  '180_wallet_payment_order_sources.sql' \
  '180a_remediate_legacy_reserved_wallet_keys.sql' \
  '181_api_key_purpose.sql' \
  '182_persistent_financial_idempotency.sql' \
  '183_subscription_plan_fulfillment_snapshots.sql' \
  '184_usage_billing_admission_lifecycle_hardening.sql' \
  '185_usage_billing_reconciliation_audit.sql' \
  '186_wallet_open_admission_revoke_guard.sql' \
  '187_usage_billing_unknown_delivery_release_guard.sql' \
  '188_encrypt_api_keys_at_rest.sql' \
  '189_persist_user_token_version.sql' \
  '190_encrypt_proxy_credentials_at_rest.sql' \
  '191_wallet_usage_replay_idempotency.sql' \
  '192_error_passthrough_safe_default.sql' \
  '193_disable_legacy_error_body_passthrough.sql' \
  '194_payment_provider_capacity_index_notx.sql' \
  '195_retire_claude_code_spoof_template.sql' \
  > "$EXPECTED_MIGRATIONS_FILE"

docker exec -i "$PG_CONTAINER" psql --username="$PG_USER" --dbname="$PG_DB" --tuples-only --no-align > "$ACTUAL_MIGRATIONS_FILE" <<'SQL'
SELECT filename
FROM schema_migrations
WHERE filename IN (
  '175_wallet_ledger_integrity.sql',
  '175a_retire_legacy_monthly_billing_smoke.sql',
  '176_enforce_monthly_group_not_wallet.sql',
  '177_allow_wallet_postpaid_settlement.sql',
  '178_wallet_integrity_indexes_notx.sql',
  '179_usage_billing_admissions.sql',
  '180_wallet_payment_order_sources.sql',
  '180a_remediate_legacy_reserved_wallet_keys.sql',
  '181_api_key_purpose.sql',
  '182_persistent_financial_idempotency.sql',
  '183_subscription_plan_fulfillment_snapshots.sql',
  '184_usage_billing_admission_lifecycle_hardening.sql',
  '185_usage_billing_reconciliation_audit.sql',
  '186_wallet_open_admission_revoke_guard.sql',
  '187_usage_billing_unknown_delivery_release_guard.sql',
  '188_encrypt_api_keys_at_rest.sql',
  '189_persist_user_token_version.sql',
  '190_encrypt_proxy_credentials_at_rest.sql',
  '191_wallet_usage_replay_idempotency.sql',
  '192_error_passthrough_safe_default.sql',
  '193_disable_legacy_error_body_passthrough.sql',
  '194_payment_provider_capacity_index_notx.sql',
  '195_retire_claude_code_spoof_template.sql'
)
ORDER BY filename;
SQL

if ! cmp -s "$EXPECTED_MIGRATIONS_FILE" "$ACTUAL_MIGRATIONS_FILE"; then
  printf 'REFUSE: expected security migration set was not fully applied\n' >&2
  exit 1
fi

printf 'phase=database_invariants\n'
INVARIANTS="$(docker exec -i "$PG_CONTAINER" psql --username="$PG_USER" --dbname="$PG_DB" --tuples-only --no-align --field-separator='|' <<'SQL'
SELECT
  (SELECT COUNT(*) FROM api_keys WHERE NOT ((deleted_at IS NULL AND key LIKE 'enc:v1:%') OR (deleted_at IS NOT NULL AND (key LIKE 'enc:v1:%' OR key LIKE '__deleted__%')))) AS plaintext_api_keys,
  (SELECT COUNT(*) FROM proxies WHERE password IS NOT NULL AND password NOT LIKE 'sd:v3:%') AS plaintext_proxy_passwords,
  (SELECT COUNT(*) FROM error_passthrough_rules WHERE passthrough_body IS TRUE) AS raw_error_body_rules,
  (SELECT COUNT(*) FROM api_keys ak
   WHERE ak.deleted_at IS NULL
     AND ak.name = '钱包通用 key（自动路由）'
     AND (
       ak.group_id IS NOT NULL
       OR NOT EXISTS (
         SELECT 1 FROM user_subscriptions us
         WHERE us.user_id = ak.user_id
           AND us.group_id IS NULL
           AND us.wallet_balance_usd IS NOT NULL
           AND us.deleted_at IS NULL
       )
     )) AS invalid_reserved_wallet_keys,
  (SELECT COUNT(*) FROM pg_constraint WHERE conname IN ('chk_api_keys_encrypted_storage', 'chk_proxies_password_encrypted_storage') AND NOT convalidated) AS unvalidated_secret_constraints,
  (SELECT COUNT(*) FROM pg_trigger WHERE NOT tgisinternal AND tgname IN (
    'trg_hfc_reject_wallet_ledger_mutation',
    'trg_hfc_lock_wallet_ledger_parent',
    'trg_hfc_guard_monthly_payment_order_group_path',
    'trg_guard_usage_billing_admission_immutable',
    'trg_hfc_guard_wallet_revoke_with_open_admission',
    'trg_hfc_block_unknown_delivery_wallet_release',
    'trg_hfc_reject_api_key_purpose_mutation',
    'trg_hfc_reject_usage_billing_reconciliation_audit_mutation'
  )) AS critical_trigger_count,
  (SELECT COUNT(*) FROM pg_indexes WHERE indexname IN (
    'idx_wallet_ledger_one_activation',
    'idx_user_subscriptions_one_active_credits_wallet',
    'idx_wallet_ledger_one_payment_credit',
    'idx_wallet_ledger_one_usage_settlement',
    'paymentorder_provider_instance_id_created_at'
  )) AS critical_index_count;
SQL
)"

IFS='|' read -r plaintext_api_keys plaintext_proxy_passwords raw_error_body_rules invalid_reserved_wallet_keys unvalidated_secret_constraints critical_trigger_count critical_index_count <<EOF
$INVARIANTS
EOF

if [ "$plaintext_api_keys" != "0" ] \
  || [ "$plaintext_proxy_passwords" != "0" ] \
  || [ "$raw_error_body_rules" != "0" ] \
  || [ "$invalid_reserved_wallet_keys" != "0" ] \
  || [ "$unvalidated_secret_constraints" != "0" ] \
  || [ "$critical_trigger_count" != "8" ] \
  || [ "$critical_index_count" != "5" ]; then
  printf 'REFUSE: database invariant check failed\n' >&2
  exit 1
fi

UNSAFE_TEMPLATE_COUNT="$(docker exec -i "$PG_CONTAINER" psql --username="$PG_USER" --dbname="$PG_DB" --tuples-only --no-align <<'SQL'
SELECT COUNT(*)
FROM channel_monitor_request_templates
WHERE provider = 'anthropic'
  AND (
    name = 'Claude Code 伪装'
    OR lower(COALESCE(extra_headers::text, '{}')) LIKE '%claude-cli/%'
    OR lower(COALESCE(body_override::text, 'null')) LIKE '%anthropic%official cli for claude%'
    OR lower(COALESCE(body_override::text, 'null')) LIKE '%cc_entrypoint%'
  );
SQL
)"
if [ "$UNSAFE_TEMPLATE_COUNT" != "0" ]; then
  printf 'REFUSE: retired Claude Code spoof template still exists\n' >&2
  exit 1
fi

DUMP_SIZE="$(wc -c < "$DUMP_FILE" | tr -d ' ')"
DUMP_SHA256="$(awk '{print $1}' "$BACKUP_DIR/SHA256SUMS")"
MIGRATION_TOTAL="$(docker exec "$PG_CONTAINER" psql --username="$PG_USER" --dbname="$PG_DB" --tuples-only --no-align --command 'SELECT COUNT(*) FROM schema_migrations;')"

printf '%s\n' \
  "verified_utc=$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
  "image=$IMAGE" \
  "image_id=$IMAGE_ID" \
  "revision=$IMAGE_REVISION" \
  "architecture=$IMAGE_ARCH" \
  "live_app_health_before=$LIVE_APP_HEALTH" \
  "production_snapshot=$DUMP_FILE" \
  "production_snapshot_bytes=$DUMP_SIZE" \
  "production_snapshot_sha256=$DUMP_SHA256" \
  'isolated_internal_network=true' \
  'published_host_ports=none' \
  'candidate_health=healthy' \
  'candidate_inner_health=pass' \
  'candidate_api_key_auth=pass' \
  'required_security_migrations=23/23' \
  "schema_migration_total=$MIGRATION_TOTAL" \
  'plaintext_api_keys=0' \
  'plaintext_proxy_passwords=0' \
  'raw_error_body_rules=0' \
  'invalid_reserved_wallet_keys=0' \
  'unvalidated_secret_constraints=0' \
  'critical_triggers=8/8' \
  'critical_indexes=5/5' \
  'unsafe_claude_code_templates=0' \
  'preflight_result=pass' \
  > "$SUMMARY_FILE"
chmod 600 "$SUMMARY_FILE"

SUCCESS=1
printf 'preflight_result=pass\n'
printf 'evidence_dir=%s\n' "$BACKUP_DIR"
printf 'summary_file=%s\n' "$SUMMARY_FILE"
