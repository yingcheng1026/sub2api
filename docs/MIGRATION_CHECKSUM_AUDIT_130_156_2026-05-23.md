# Migration Checksum Audit · 130-138 / 150-156 · 2026-05-23

> 执行时间: 2026-05-23 12:23 CST；15:29 CST 复核 prod 数据状态
> 范围: `backend/migrations/130-138` 与 `backend/migrations/150-156`
> 目的: 回溯查找 "prod checksum != current git checksum" 且含 `UPDATE/INSERT` 等数据迁移的沉默欠债。

## 结论

- 使用 migration runner 的实际算法 `strings.TrimSpace(content)` 后再 SHA256，对比 prod `schema_migrations`，本次范围内 **全部一致**。
- 本次 audit 没有发现需要补跑 SQL 或新建兜底 migration 的 130-138 / 150-156 checksum 欠债。
- 注意: 直接对文件跑 `shasum -a 256` 会把末尾换行算进去，结果可能不同；runner 与 prod 记录使用的是 trim 后内容。

## 只读命令

```bash
ssh relay "docker exec sub2api-postgres psql -U sub2api -d sub2api -Atc \
  \"SELECT filename, checksum FROM schema_migrations WHERE filename ~ '^(13[0-8]|15[0-6])_' ORDER BY filename;\""

for f in backend/migrations/13{0..8}_*.sql backend/migrations/15{0..6}_*.sql; do
  perl -0pe 's/^\s+|\s+\z//g' "$f" | shasum -a 256 | awk -v file="$f" '{print $1 "  " file}'
done
```

## 文件级结果

| Migration | 含数据写入 | prod vs git runner checksum |
|---|---:|---|
| `130_add_user_affiliates.sql` | no | match |
| `131_affiliate_rebate_hardening.sql` | yes | match |
| `132_affiliate_custom_settings.sql` | no | match |
| `133_affiliate_rebate_freeze.sql` | no | match |
| `134_affiliate_ledger_audit_snapshots.sql` | yes | match |
| `134_image_generation_group_controls.sql` | yes | match |
| `134_usage_billing_ledger_trigger.sql` | yes | match |
| `135_allow_email_oauth_provider_types.sql` | no | match |
| `135_content_moderation.sql` | yes | match |
| `135_double_entry_billing_ledger.sql` | yes | match |
| `136_add_api_key_hash_columns.sql` | no | match |
| `137_backfill_api_key_hash.sql` | yes | match |
| `138_add_api_key_hash_indexes_notx.sql` | no | match |
| `150_add_subscription_plan_groups.sql` | no | match |
| `151_add_subscription_wallet_fields.sql` | no | match |
| `152_add_subscription_wallet_ledger.sql` | no | match |
| `153_add_plan_type.sql` | no | match |
| `154_extend_wallet_ledger_reason_topup.sql` | no | match |
| `155_add_redeem_code_plan_id.sql` | no | match |
| `156_add_user_subscription_locked_rates.sql` | yes | match |

## Prod Data Spot Check

同轮只读检查还确认:

```text
groups.allow_image_generation column_default = true

platform|allow_image_generation|count
anthropic|false|4
antigravity|true|1
cursor|false|1
gemini|true|1
openai|false|1
openai|true|7
```

残余风险: 当前 prod 仍有一个 active OpenAI 分组 `id=18` 的 `allow_image_generation=false`。本任务没有写 prod 数据；是否把该分组改为 true 需要单独确认它是不是有意关闭图片生成。

## Compatibility Rule Smoke Check

新护栏会影响 `migrationChecksumCompatibilityRules` 里的历史白名单，所以同轮额外只读检查了所有白名单 migration 的 prod checksum 与当前 git runner checksum:

```text
054_drop_legacy_cache_columns.sql                       match
061_add_usage_log_request_type.sql                      match
109_auth_identity_compat_backfill.sql                   match
110_pending_auth_and_provider_default_grants.sql        match
112_add_payment_order_provider_key_snapshot.sql         match
115_auth_identity_legacy_external_backfill.sql          match
116_auth_identity_legacy_external_safety_reports.sql    match
118_wechat_dual_mode_and_auth_source_defaults.sql       match
119_enforce_payment_orders_out_trade_no_unique.sql      match
120_enforce_payment_orders_out_trade_no_unique_notx.sql match
123_fix_legacy_auth_source_grant_on_signup_defaults.sql match
```

这说明当前 prod 不依赖这些白名单才能启动；启用"数据 migration mismatch 必须人工 review"不会立刻卡住当前生产库的已应用白名单项。

## Runner Guard Follow-up

本次代码同时增加 migration runner 护栏: 已应用 migration 出现 checksum mismatch 时，如果当前文件包含 `UPDATE` / `INSERT` / `DELETE` / `MERGE` / `COPY`，兼容白名单不再生效，必须人工 review 后决定补跑 SQL、写新 migration，或明确记录无需数据回填。
