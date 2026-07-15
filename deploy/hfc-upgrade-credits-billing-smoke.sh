#!/usr/bin/env bash
# Upgrade the production credits smoke so it never reads encrypted API-key
# ciphertext as if it were a client credential. The dedicated canary secret is
# copied once from the still-legacy database into a root-only credential file.

set -Eeuo pipefail
umask 077

TARGET="${HFC_CREDITS_SMOKE_SCRIPT:-/opt/relay/ai-relay-infra/scripts/sub2api-credits-billing-smoke.sh}"
CREDENTIAL_FILE="${HFC_CREDITS_SMOKE_API_KEY_FILE:-/etc/hfc-secrets/sub2api-credits-billing-smoke-api-key}"
BACKUP_ROOT="${HFC_CREDITS_SMOKE_BACKUP_ROOT:-/var/backups}"
EXPECTED_SHA256="${HFC_CREDITS_SMOKE_EXPECTED_SHA256:-}"
POSTGRES_CONTAINER="${POSTGRES_CONTAINER:-sub2api-postgres}"
POSTGRES_USER="${POSTGRES_USER:-sub2api}"
POSTGRES_DB="${POSTGRES_DB:-sub2api}"
SMOKE_EMAIL="${HFC_CREDITS_SMOKE_EMAIL:-hfc-claude-wallet-smoke@handsfreeclub.local}"
SMOKE_KEY_NAME="${HFC_CREDITS_SMOKE_KEY_NAME:-claude-wallet-smoke-openai-default}"
SMOKE_KEY_GROUP_ID="${HFC_CREDITS_SMOKE_KEY_GROUP_ID:-3}"
STAMP="$(date -u +%Y%m%dT%H%M%SZ)"
BACKUP_DIR="$BACKUP_ROOT/hfc-credits-smoke-key-$STAMP-$$"
TARGET_STAGE=""
CREDENTIAL_STAGE=""

cleanup() {
  [ -z "$TARGET_STAGE" ] || rm -f "$TARGET_STAGE"
  [ -z "$CREDENTIAL_STAGE" ] || rm -f "$CREDENTIAL_STAGE"
}
trap cleanup EXIT

refuse() {
  printf 'REFUSE: %s\n' "$*" >&2
  exit 1
}

for command_name in awk bash chmod chown cp date docker flock mkdir mktemp mv python3 sha256sum stat; do
  command -v "$command_name" >/dev/null 2>&1 || refuse "missing command: $command_name"
done

[ -n "$EXPECTED_SHA256" ] || refuse "HFC_CREDITS_SMOKE_EXPECTED_SHA256 is required"
[ -f "$TARGET" ] && [ ! -L "$TARGET" ] || refuse "credits smoke target must be a regular non-symlink file"
actual_sha256="$(sha256sum "$TARGET" | awk '{print $1}')"
[ "$actual_sha256" = "$EXPECTED_SHA256" ] || refuse "credits smoke target hash does not match the reviewed source"
[[ "$SMOKE_KEY_GROUP_ID" =~ ^[1-9][0-9]*$ ]] || refuse "smoke key group must be a positive integer"

mkdir -p "$BACKUP_ROOT"
exec 9>"$BACKUP_ROOT/.hfc-credits-smoke-upgrade.lock"
flock -n 9 || refuse "another credits smoke upgrade is active"

mkdir "$BACKUP_DIR"
chmod 700 "$BACKUP_DIR"
cp -p "$TARGET" "$BACKUP_DIR/sub2api-credits-billing-smoke.sh.before"
chmod 600 "$BACKUP_DIR/sub2api-credits-billing-smoke.sh.before"
if [ -e "$CREDENTIAL_FILE" ]; then
  [ -f "$CREDENTIAL_FILE" ] && [ ! -L "$CREDENTIAL_FILE" ] || refuse "existing credential must be a regular non-symlink file"
  cp -p "$CREDENTIAL_FILE" "$BACKUP_DIR/canary-key.before"
  chmod 600 "$BACKUP_DIR/canary-key.before"
fi

credential_dir="$(dirname "$CREDENTIAL_FILE")"
mkdir -p "$credential_dir"
chmod 700 "$credential_dir"
[ ! -L "$credential_dir" ] || refuse "credential directory must not be a symlink"

api_key="$(docker exec -i "$POSTGRES_CONTAINER" psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" \
  -v ON_ERROR_STOP=1 -tA \
  -v smoke_email="$SMOKE_EMAIL" \
  -v key_name="$SMOKE_KEY_NAME" \
  -v key_group="$SMOKE_KEY_GROUP_ID" <<'SQL'
SELECT ak.key
FROM users u
JOIN api_keys ak
  ON ak.user_id = u.id
 AND ak.name = :'key_name'
 AND ak.group_id = :'key_group'::bigint
 AND ak.status = 'active'
 AND ak.deleted_at IS NULL
WHERE u.email = :'smoke_email'
  AND u.status = 'active'
  AND u.deleted_at IS NULL
ORDER BY ak.id DESC
LIMIT 1;
SQL
)"

[ -n "$api_key" ] || refuse "dedicated credits canary API key is missing"
[[ "$api_key" != enc:v1:* ]] || refuse "database canary key is already encrypted; provision from a verified pre-cutover backup"
[[ "$api_key" != *[[:space:]]* ]] || refuse "dedicated credits canary API key contains whitespace"
[ "${#api_key}" -ge 16 ] || refuse "dedicated credits canary API key is unexpectedly short"

CREDENTIAL_STAGE="$(mktemp "$credential_dir/.sub2api-credits-smoke-key.XXXXXX")"
printf '%s\n' "$api_key" > "$CREDENTIAL_STAGE"
chmod 600 "$CREDENTIAL_STAGE"
chown root:root "$CREDENTIAL_STAGE"

TARGET_STAGE="$(mktemp "$(dirname "$TARGET")/.sub2api-credits-smoke.XXXXXX")"
python3 - "$TARGET" "$TARGET_STAGE" <<'PY'
from pathlib import Path
import sys

source = Path(sys.argv[1])
target = Path(sys.argv[2])
text = source.read_text()

def replace_once(old: str, new: str) -> None:
    global text
    count = text.count(old)
    if count != 1:
        raise SystemExit(f"reviewed patch marker count mismatch: {count}")
    text = text.replace(old, new, 1)

replace_once(
    'SMOKE_KEY_GROUP_ID="${HFC_CREDITS_SMOKE_KEY_GROUP_ID:-3}"\nSMOKE_MODEL=',
    'SMOKE_KEY_GROUP_ID="${HFC_CREDITS_SMOKE_KEY_GROUP_ID:-3}"\n'
    'SMOKE_API_KEY_FILE="${HFC_CREDITS_SMOKE_API_KEY_FILE:-/etc/hfc-secrets/sub2api-credits-billing-smoke-api-key}"\n'
    'SMOKE_MODEL=',
)
replace_once(
    '  HFC_CREDITS_SMOKE_KEY_GROUP_ID        API key group to exercise\n',
    '  HFC_CREDITS_SMOKE_KEY_GROUP_ID        API key group to exercise\n'
    '  HFC_CREDITS_SMOKE_API_KEY_FILE        root-only dedicated canary credential file\n',
)
replace_once(
    '''require_cmd() {
  command -v "$1" >/dev/null 2>&1 || fail "missing command: $1"
}
''',
    '''require_cmd() {
  command -v "$1" >/dev/null 2>&1 || fail "missing command: $1"
}

read_smoke_api_key() {
  local mode owner line_count key
  [ -f "$SMOKE_API_KEY_FILE" ] && [ ! -L "$SMOKE_API_KEY_FILE" ] || \\
    fail "credits canary API key file must be a regular non-symlink file"
  mode="$(stat -c '%a' "$SMOKE_API_KEY_FILE")"
  case "$mode" in
    400|600) ;;
    *) fail "credits canary API key file permissions must be 400 or 600" ;;
  esac
  owner="$(stat -c '%U:%G' "$SMOKE_API_KEY_FILE")"
  [ "$owner" = "root:root" ] || fail "credits canary API key file must be owned by root:root"
  line_count="$(awk 'END { print NR+0 }' "$SMOKE_API_KEY_FILE")"
  [ "$line_count" -eq 1 ] || fail "credits canary API key file must contain exactly one line"
  IFS= read -r key < "$SMOKE_API_KEY_FILE" || [ -n "$key" ]
  [ -n "$key" ] || fail "credits canary API key file is empty"
  [[ "$key" != *[[:space:]]* ]] || fail "credits canary API key contains whitespace"
  [ "${#key}" -ge 16 ] || fail "credits canary API key is unexpectedly short"
  printf '%s' "$key"
}
''',
)
replace_once(
    'require_cmd curl\nrequire_cmd docker\nrequire_cmd flock\nrequire_cmd python3\n',
    'require_cmd curl\nrequire_cmd docker\nrequire_cmd flock\nrequire_cmd python3\nrequire_cmd stat\n',
)
replace_once(
    '''       us.wallet_balance_usd::text,
       us.wallet_initial_usd::text,
       ak.key
''',
    '''       us.wallet_balance_usd::text,
       us.wallet_initial_usd::text
''',
)
replace_once(
    '''IFS='|' read -r user_id api_key_id subscription_id user_balance_before wallet_before wallet_initial api_key <<EOF
$canary_row
EOF

[ -n "$api_key" ] || fail "canary API key value is empty for api_key_id=${api_key_id}"
''',
    '''IFS='|' read -r user_id api_key_id subscription_id user_balance_before wallet_before wallet_initial <<EOF
$canary_row
EOF

api_key="$(read_smoke_api_key)"
''',
)

target.write_text(text)
PY

chmod 700 "$TARGET_STAGE"
chown root:root "$TARGET_STAGE"
bash -n "$TARGET_STAGE"
"$TARGET_STAGE" --self-test >/dev/null

mv -f "$CREDENTIAL_STAGE" "$CREDENTIAL_FILE"
CREDENTIAL_STAGE=""
mv -f "$TARGET_STAGE" "$TARGET"
TARGET_STAGE=""
chmod 600 "$CREDENTIAL_FILE"
chmod 700 "$TARGET"
chown root:root "$CREDENTIAL_FILE" "$TARGET"

(
  cd "$BACKUP_DIR"
  sha256sum sub2api-credits-billing-smoke.sh.before > SHA256SUMS
  if [ -f canary-key.before ]; then
    sha256sum canary-key.before >> SHA256SUMS
  fi
  chmod 600 SHA256SUMS
  sha256sum -c SHA256SUMS >/dev/null
)

printf 'credits_smoke_upgrade=success\n'
printf 'credential_file=%s\n' "$CREDENTIAL_FILE"
printf 'credential_mode=%s\n' "$(stat -c '%a' "$CREDENTIAL_FILE")"
printf 'target_sha256=%s\n' "$(sha256sum "$TARGET" | awk '{print $1}')"
printf 'backup_dir=%s\n' "$BACKUP_DIR"
