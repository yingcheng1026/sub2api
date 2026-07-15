#!/usr/bin/env bash
# Provision the independent HFC security roots without exposing their values.
# The update is staged and validated before .env or docker-compose.yml changes.

set -Eeuo pipefail
umask 077

APP_DIR="${1:-/opt/relay/ai-relay-infra/sub2api}"
BACKUP_ROOT="${HFC_SECURITY_BACKUP_ROOT:-/var/backups}"
ENV_FILE="$APP_DIR/.env"
COMPOSE_FILE="$APP_DIR/docker-compose.yml"
STAMP="$(date -u +%Y%m%dT%H%M%SZ)"
RUN_ID="hfc-security-secrets-${STAMP}-$$"
BACKUP_DIR="$BACKUP_ROOT/$RUN_ID"
ENV_STAGE=""
COMPOSE_STAGE=""

SECURITY_KEYS=(
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

COMPOSE_KEYS=(
  "${SECURITY_KEYS[@]}"
  PAYMENT_RESUME_LEGACY_VERIFY_UNTIL
  SUB2API_OFFICIAL_UPDATE_APPLY_ENABLED
)

cleanup() {
  if [ -n "$ENV_STAGE" ]; then
    rm -f "$ENV_STAGE"
  fi
  if [ -n "$COMPOSE_STAGE" ]; then
    rm -f "$COMPOSE_STAGE"
  fi
}
trap cleanup EXIT

refuse() {
  printf 'REFUSE: %s\n' "$*" >&2
  exit 1
}

for command_name in awk date docker flock grep mkdir mktemp openssl sha256sum sort; do
  command -v "$command_name" >/dev/null 2>&1 || refuse "missing command: $command_name"
done

[ -d "$APP_DIR" ] || refuse "application directory is missing: $APP_DIR"
[ -f "$ENV_FILE" ] && [ ! -L "$ENV_FILE" ] || refuse ".env must be a regular non-symlink file"
[ -f "$COMPOSE_FILE" ] && [ ! -L "$COMPOSE_FILE" ] || refuse "docker-compose.yml must be a regular non-symlink file"

mkdir -p "$BACKUP_ROOT"
exec 9>"$BACKUP_ROOT/.hfc-security-secrets.lock"
flock -n 9 || refuse "another security-secret provisioning run is active"

mkdir "$BACKUP_DIR"
chmod 700 "$BACKUP_DIR"
cp -p "$ENV_FILE" "$BACKUP_DIR/.env.before"
cp -p "$COMPOSE_FILE" "$BACKUP_DIR/docker-compose.yml.before"
(
  cd "$BACKUP_DIR"
  sha256sum .env.before docker-compose.yml.before > SHA256SUMS
)
chmod 600 "$BACKUP_DIR/.env.before" "$BACKUP_DIR/docker-compose.yml.before" "$BACKUP_DIR/SHA256SUMS"

declare -A wanted=()
declare -A values=()
declare -A counts=()
declare -A emitted=()
for key in "${SECURITY_KEYS[@]}"; do
  wanted["$key"]=1
  counts["$key"]=0
done

legacy_key=""
jwt_secret=""
legacy_count=0
jwt_count=0
while IFS= read -r line || [ -n "$line" ]; do
  case "$line" in
    *=*)
      name="${line%%=*}"
      value="${line#*=}"
      if [ -n "${wanted[$name]+x}" ]; then
        counts["$name"]=$(( counts["$name"] + 1 ))
        values["$name"]="$value"
      elif [ "$name" = "TOTP_ENCRYPTION_KEY" ]; then
        legacy_count=$((legacy_count + 1))
        legacy_key="$value"
      elif [ "$name" = "JWT_SECRET" ]; then
        jwt_count=$((jwt_count + 1))
        jwt_secret="$value"
      fi
      ;;
  esac
done < "$ENV_FILE"

[ "$legacy_count" -eq 1 ] || refuse "TOTP_ENCRYPTION_KEY must occur exactly once"
[[ "$legacy_key" =~ ^[0-9a-fA-F]{64}$ ]] || refuse "TOTP_ENCRYPTION_KEY must be 64 hex characters for legacy migration"
[ "$jwt_count" -eq 1 ] || refuse "JWT_SECRET must occur exactly once"
[ -n "$jwt_secret" ] || refuse "JWT_SECRET must not be empty"

generated_count=0
preserved_count=0
for key in "${SECURITY_KEYS[@]}"; do
  [ "${counts[$key]}" -le 1 ] || refuse "$key occurs more than once"
  value="${values[$key]-}"
  if [ -z "$value" ]; then
    value="$(openssl rand -hex 32)"
    values["$key"]="$value"
    generated_count=$((generated_count + 1))
  else
    preserved_count=$((preserved_count + 1))
  fi
  [[ "$value" =~ ^[0-9a-fA-F]{64}$ ]] || refuse "$key must be 64 hex characters"
done

declare -A seen=()
register_secret() {
  local label="$1"
  local value="$2"
  local canonical="$value"
  if [[ "$value" =~ ^[0-9a-fA-F]{64}$ ]]; then
    canonical="${value,,}"
  fi
  if [ -n "${seen[$canonical]+x}" ]; then
    refuse "$label reuses secret material from ${seen[$canonical]}"
  fi
  seen["$canonical"]="$label"
}

register_secret TOTP_ENCRYPTION_KEY "$legacy_key"
register_secret JWT_SECRET "$jwt_secret"
for key in "${SECURITY_KEYS[@]}"; do
  register_secret "$key" "${values[$key]}"
done

ENV_STAGE="$(mktemp "$APP_DIR/.env.security-next.XXXXXX")"
while IFS= read -r line || [ -n "$line" ]; do
  case "$line" in
    *=*)
      name="${line%%=*}"
      if [ -n "${wanted[$name]+x}" ]; then
        if [ -z "${emitted[$name]+x}" ]; then
          printf '%s=%s\n' "$name" "${values[$name]}" >> "$ENV_STAGE"
          emitted["$name"]=1
        fi
        continue
      fi
      ;;
  esac
  printf '%s\n' "$line" >> "$ENV_STAGE"
done < "$ENV_FILE"

printf '\n# Independent security roots; values are generated once and must remain stable.\n' >> "$ENV_STAGE"
for key in "${SECURITY_KEYS[@]}"; do
  if [ -z "${emitted[$key]+x}" ]; then
    printf '%s=%s\n' "$key" "${values[$key]}" >> "$ENV_STAGE"
  fi
done

if ! grep -q '^PAYMENT_RESUME_LEGACY_VERIFY_UNTIL=' "$ENV_STAGE"; then
  printf 'PAYMENT_RESUME_LEGACY_VERIFY_UNTIL=\n' >> "$ENV_STAGE"
fi
if ! grep -q '^SUB2API_OFFICIAL_UPDATE_APPLY_ENABLED=' "$ENV_STAGE"; then
  printf 'SUB2API_OFFICIAL_UPDATE_APPLY_ENABLED=false\n' >> "$ENV_STAGE"
fi
chmod 600 "$ENV_STAGE"

existing_compose_refs=0
for key in "${COMPOSE_KEYS[@]}"; do
  count="$(awk -v key="$key" 'index($0, "- " key "=") { count++ } END { print count+0 }' "$COMPOSE_FILE")"
  [ "$count" -le 1 ] || refuse "$key occurs more than once in docker-compose.yml"
  existing_compose_refs=$((existing_compose_refs + count))
done

if [ "$existing_compose_refs" -ne 0 ] && [ "$existing_compose_refs" -ne "${#COMPOSE_KEYS[@]}" ]; then
  refuse "docker-compose.yml has a partial security-key mapping block"
fi

COMPOSE_STAGE="$(mktemp "$APP_DIR/docker-compose.security-next.XXXXXX.yml")"
if [ "$existing_compose_refs" -eq "${#COMPOSE_KEYS[@]}" ]; then
  cp "$COMPOSE_FILE" "$COMPOSE_STAGE"
else
  inserted=0
  while IFS= read -r line || [ -n "$line" ]; do
    printf '%s\n' "$line" >> "$COMPOSE_STAGE"
    if [[ "$line" == *'- TOTP_ENCRYPTION_KEY='* ]]; then
      [ "$inserted" -eq 0 ] || refuse "TOTP_ENCRYPTION_KEY occurs more than once in docker-compose.yml"
      printf '%s\n' \
        '      - SECRET_ENCRYPTION_TOTP_SECRET_KEY=${SECRET_ENCRYPTION_TOTP_SECRET_KEY:?SECRET_ENCRYPTION_TOTP_SECRET_KEY is required}' \
        '      - SECRET_ENCRYPTION_TOTP_CACHE_KEY=${SECRET_ENCRYPTION_TOTP_CACHE_KEY:?SECRET_ENCRYPTION_TOTP_CACHE_KEY is required}' \
        '      - SECRET_ENCRYPTION_ACCOUNT_CREDENTIAL_KEY=${SECRET_ENCRYPTION_ACCOUNT_CREDENTIAL_KEY:?SECRET_ENCRYPTION_ACCOUNT_CREDENTIAL_KEY is required}' \
        '      - SECRET_ENCRYPTION_BACKUP_S3_KEY=${SECRET_ENCRYPTION_BACKUP_S3_KEY:?SECRET_ENCRYPTION_BACKUP_S3_KEY is required}' \
        '      - SECRET_ENCRYPTION_CONTENT_MODERATION_KEY=${SECRET_ENCRYPTION_CONTENT_MODERATION_KEY:?SECRET_ENCRYPTION_CONTENT_MODERATION_KEY is required}' \
        '      - SECRET_ENCRYPTION_CHANNEL_MONITOR_KEY=${SECRET_ENCRYPTION_CHANNEL_MONITOR_KEY:?SECRET_ENCRYPTION_CHANNEL_MONITOR_KEY is required}' \
        '      - SECRET_ENCRYPTION_PAYMENT_PROVIDER_KEY=${SECRET_ENCRYPTION_PAYMENT_PROVIDER_KEY:?SECRET_ENCRYPTION_PAYMENT_PROVIDER_KEY is required}' \
        '      - SECRET_ENCRYPTION_PROXY_CREDENTIAL_KEY=${SECRET_ENCRYPTION_PROXY_CREDENTIAL_KEY:?SECRET_ENCRYPTION_PROXY_CREDENTIAL_KEY is required}' \
        '      - SECRET_ENCRYPTION_SCHEDULER_CACHE_KEY=${SECRET_ENCRYPTION_SCHEDULER_CACHE_KEY:?SECRET_ENCRYPTION_SCHEDULER_CACHE_KEY is required}' \
        '      - SECRET_ENCRYPTION_OAUTH_TOKEN_CACHE_KEY=${SECRET_ENCRYPTION_OAUTH_TOKEN_CACHE_KEY:?SECRET_ENCRYPTION_OAUTH_TOKEN_CACHE_KEY is required}' \
        '      - SECRET_ENCRYPTION_JWT_HMAC_KEY=${SECRET_ENCRYPTION_JWT_HMAC_KEY:?SECRET_ENCRYPTION_JWT_HMAC_KEY is required}' \
        '      - SECRET_ENCRYPTION_SETTING_SECRET_KEY=${SECRET_ENCRYPTION_SETTING_SECRET_KEY:?SECRET_ENCRYPTION_SETTING_SECRET_KEY is required}' \
        '      - API_KEY_ENCRYPTION_KEY=${API_KEY_ENCRYPTION_KEY:?API_KEY_ENCRYPTION_KEY is required}' \
        '      - PAYMENT_RESUME_SIGNING_KEY=${PAYMENT_RESUME_SIGNING_KEY:?PAYMENT_RESUME_SIGNING_KEY is required}' \
        '      - PAYMENT_RESUME_LEGACY_VERIFY_UNTIL=${PAYMENT_RESUME_LEGACY_VERIFY_UNTIL:-}' \
        '      - SUB2API_OFFICIAL_UPDATE_APPLY_ENABLED=${SUB2API_OFFICIAL_UPDATE_APPLY_ENABLED:-false}' \
        >> "$COMPOSE_STAGE"
      inserted=1
    fi
  done < "$COMPOSE_FILE"
  [ "$inserted" -eq 1 ] || refuse "docker-compose.yml has no TOTP_ENCRYPTION_KEY insertion point"
fi
chmod 644 "$COMPOSE_STAGE"

for key in "${COMPOSE_KEYS[@]}"; do
  count="$(awk -v key="$key" 'index($0, "- " key "=") { count++ } END { print count+0 }' "$COMPOSE_STAGE")"
  [ "$count" -eq 1 ] || refuse "$key must occur exactly once in staged docker-compose.yml"
done

(
  cd "$APP_DIR"
  docker compose --env-file "$ENV_STAGE" -f "$COMPOSE_STAGE" config --quiet
)

mv -f "$ENV_STAGE" "$ENV_FILE"
ENV_STAGE=""
mv -f "$COMPOSE_STAGE" "$COMPOSE_FILE"
COMPOSE_STAGE=""
chmod 600 "$ENV_FILE"
chmod 644 "$COMPOSE_FILE"

printf 'provision_result=success\n'
printf 'generated_keys=%s\n' "$generated_count"
printf 'preserved_keys=%s\n' "$preserved_count"
printf 'distinct_roots=pass\n'
printf 'compose_validation=pass\n'
printf 'backup_dir=%s\n' "$BACKUP_DIR"
