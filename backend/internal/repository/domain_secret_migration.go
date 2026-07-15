package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"entgo.io/ent/dialect"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/channelmonitor"
	"github.com/Wei-Shaw/sub2api/ent/proxy"
	"github.com/Wei-Shaw/sub2api/ent/schema/mixins"
	"github.com/Wei-Shaw/sub2api/ent/setting"
	"github.com/Wei-Shaw/sub2api/ent/user"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

const backupS3ConfigSettingKey = "backup_s3_config"
const domainSecretMigrationAdvisoryLockID int64 = 694208311321144028

// MigrateDomainBoundSecrets rewrites every persistent value that historically
// shared TOTP_ENCRYPTION_KEY, including v2 shared-root domain ciphertext, in one
// transaction. Callers must run it after old binaries are drained and before
// constructing workers or HTTP listeners.
func MigrateDomainBoundSecrets(ctx context.Context, client *dbent.Client, encryptor service.SecretEncryptor) (result DomainSecretMigrationResult, err error) {
	if client == nil {
		return result, errors.New("nil ent client")
	}
	if _, ok := encryptor.(service.DomainSecretEncryptor); !ok {
		return result, errors.New("domain secret encryptor is unavailable")
	}

	tx, err := client.Tx(ctx)
	if err != nil {
		return result, fmt.Errorf("begin domain secret migration: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	txClient := tx.Client()
	exec, _ := txClient.Driver().(sqlQueryExecutor)
	if err = acquireDomainSecretMigrationLock(ctx, client.Driver().Dialect(), exec); err != nil {
		return result, err
	}

	accountResult, err := EncryptPlaintextAccountCredentials(ctx, txClient, encryptor, false)
	if err != nil {
		return result, fmt.Errorf("migrate account credentials: %w", err)
	}
	result.AccountCredentials = accountResult.Updated
	if result.TOTPSecrets, err = migrateTOTPSecrets(ctx, txClient, encryptor); err != nil {
		return result, err
	}
	if result.ChannelMonitorKeys, err = migrateChannelMonitorKeys(ctx, txClient, encryptor); err != nil {
		return result, err
	}
	if result.ChannelMonitorPayloads, err = migrateChannelMonitorRequestPayloads(ctx, txClient, encryptor); err != nil {
		return result, err
	}
	if result.BackupS3Configs, err = migrateBackupS3Config(ctx, txClient, encryptor); err != nil {
		return result, err
	}
	if result.ContentModerationConfigs, err = migrateContentModerationConfig(ctx, txClient, encryptor); err != nil {
		return result, err
	}
	if result.SettingSecrets, err = migrateSensitiveSettingSecrets(ctx, txClient, encryptor); err != nil {
		return result, err
	}
	if result.ProxyCredentials, err = migrateProxyCredentials(ctx, txClient, encryptor); err != nil {
		return result, err
	}
	if err = validateProxyCredentialStorageConstraint(ctx, client.Driver().Dialect(), exec); err != nil {
		return result, err
	}
	if err = tx.Commit(); err != nil {
		return result, fmt.Errorf("commit domain secret migration: %w", err)
	}
	return result, nil
}

func migrateSensitiveSettingSecrets(ctx context.Context, client *dbent.Client, encryptor service.SecretEncryptor) (int, error) {
	keys := make([]string, 0, len(sensitiveSettingKeys))
	for key := range sensitiveSettingKeys {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	rows, err := client.Setting.Query().Where(setting.KeyIn(keys...)).All(ctx)
	if err != nil {
		return 0, fmt.Errorf("query sensitive settings: %w", err)
	}
	updated := 0
	for _, row := range rows {
		if row.Value == "" {
			continue
		}
		bound, changed, err := migratePlaintextSettingSecret(encryptor, row.Value)
		if err != nil {
			return updated, fmt.Errorf("migrate sensitive setting %q: %w", row.Key, err)
		}
		if !changed {
			continue
		}
		if _, err := client.Setting.UpdateOneID(row.ID).
			Where(setting.ValueEQ(row.Value)).
			SetValue(bound).
			Save(ctx); err != nil {
			return updated, fmt.Errorf("update sensitive setting %q with concurrency guard: %w", row.Key, err)
		}
		updated++
	}
	return updated, nil
}

func migratePlaintextSettingSecret(encryptor service.SecretEncryptor, value string) (string, bool, error) {
	if strings.HasPrefix(value, secretDomainCiphertextPrefix) {
		if _, err := service.DecryptForSecretDomain(encryptor, service.SecretDomainSettingSecret, value); err != nil {
			return "", false, err
		}
		return value, false, nil
	}
	if strings.HasPrefix(value, "sd:") {
		return "", false, errors.New("unsupported or malformed setting secret ciphertext")
	}
	bound, err := service.EncryptForSecretDomain(encryptor, service.SecretDomainSettingSecret, value)
	return bound, true, err
}

func acquireDomainSecretMigrationLock(ctx context.Context, dialectName string, exec sqlQueryExecutor) error {
	if dialectName != dialect.Postgres {
		return nil
	}
	if exec == nil {
		return errors.New("domain secret migration SQL executor is unavailable")
	}
	rows, err := exec.QueryContext(ctx, "SELECT pg_advisory_xact_lock($1)", domainSecretMigrationAdvisoryLockID)
	if err != nil {
		return fmt.Errorf("acquire domain secret migration lock: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close domain secret migration lock result: %w", err)
	}
	return nil
}

func validateProxyCredentialStorageConstraint(ctx context.Context, dialectName string, exec sqlQueryExecutor) error {
	if dialectName != dialect.Postgres {
		return nil
	}
	if exec == nil {
		return errors.New("domain secret migration SQL executor is unavailable")
	}
	if _, err := exec.ExecContext(ctx, "ALTER TABLE proxies VALIDATE CONSTRAINT chk_proxies_password_encrypted_storage"); err != nil {
		return fmt.Errorf("validate encrypted proxy credential storage: %w", err)
	}
	return nil
}

func migrateProxyCredentials(ctx context.Context, client *dbent.Client, encryptor service.SecretEncryptor) (int, error) {
	migrationCtx := mixins.SkipSoftDelete(ctx)
	rows, err := client.Proxy.Query().Where(proxy.PasswordNotNil()).All(migrationCtx)
	if err != nil {
		return 0, fmt.Errorf("query proxy credentials: %w", err)
	}
	updated := 0
	for _, row := range rows {
		if row.Password == nil {
			continue
		}
		if *row.Password == "" {
			if _, err := client.Proxy.UpdateOneID(row.ID).
				Where(proxy.PasswordEQ("")).
				ClearPassword().
				Save(migrationCtx); err != nil {
				return updated, fmt.Errorf("clear empty proxy %d credential with concurrency guard: %w", row.ID, err)
			}
			updated++
			continue
		}
		bound, changed, err := migrateLegacyOrPlaintextSecret(encryptor, service.SecretDomainProxyCredential, *row.Password)
		if err != nil {
			return updated, fmt.Errorf("migrate proxy %d credential: %w", row.ID, err)
		}
		if !changed {
			continue
		}
		if _, err := client.Proxy.UpdateOneID(row.ID).
			Where(proxy.PasswordEQ(*row.Password)).
			SetPassword(bound).
			Save(migrationCtx); err != nil {
			return updated, fmt.Errorf("update proxy %d credential with concurrency guard: %w", row.ID, err)
		}
		updated++
	}
	return updated, nil
}

func migrateTOTPSecrets(ctx context.Context, client *dbent.Client, encryptor service.SecretEncryptor) (int, error) {
	migrationCtx := mixins.SkipSoftDelete(ctx)
	rows, err := client.User.Query().Where(user.TotpSecretEncryptedNotNil()).All(migrationCtx)
	if err != nil {
		return 0, fmt.Errorf("query TOTP secrets: %w", err)
	}
	updated := 0
	for _, row := range rows {
		if row.TotpSecretEncrypted == nil || strings.TrimSpace(*row.TotpSecretEncrypted) == "" {
			continue
		}
		bound, changed, err := migrateLegacyCiphertext(encryptor, service.SecretDomainTOTP, *row.TotpSecretEncrypted)
		if err != nil {
			return updated, fmt.Errorf("migrate user %d TOTP secret: %w", row.ID, err)
		}
		if !changed {
			continue
		}
		if _, err := client.User.UpdateOneID(row.ID).
			Where(user.TotpSecretEncryptedEQ(*row.TotpSecretEncrypted)).
			SetTotpSecretEncrypted(bound).
			Save(migrationCtx); err != nil {
			return updated, fmt.Errorf("update user %d TOTP secret with concurrency guard: %w", row.ID, err)
		}
		updated++
	}
	return updated, nil
}

func migrateChannelMonitorKeys(ctx context.Context, client *dbent.Client, encryptor service.SecretEncryptor) (int, error) {
	rows, err := client.ChannelMonitor.Query().All(ctx)
	if err != nil {
		return 0, fmt.Errorf("query channel monitor keys: %w", err)
	}
	updated := 0
	for _, row := range rows {
		bound, changed, err := migrateLegacyCiphertext(encryptor, service.SecretDomainChannelMonitor, row.APIKeyEncrypted)
		if err != nil {
			return updated, fmt.Errorf("migrate channel monitor %d key: %w", row.ID, err)
		}
		if !changed {
			continue
		}
		if _, err := client.ChannelMonitor.UpdateOneID(row.ID).
			Where(channelmonitor.APIKeyEncryptedEQ(row.APIKeyEncrypted)).
			SetAPIKeyEncrypted(bound).
			Save(ctx); err != nil {
			return updated, fmt.Errorf("update channel monitor %d key with concurrency guard: %w", row.ID, err)
		}
		updated++
	}
	return updated, nil
}

// migrateChannelMonitorRequestPayloads encrypts both reusable templates and
// monitor snapshots. Counting is per updated row, even when both JSON fields in
// that row are rewritten.
func migrateChannelMonitorRequestPayloads(ctx context.Context, client *dbent.Client, encryptor service.SecretEncryptor) (int, error) {
	updatedTemplates, err := migrateChannelMonitorTemplatePayloads(ctx, client, encryptor)
	if err != nil {
		return 0, err
	}
	updatedMonitors, err := migrateChannelMonitorSnapshotPayloads(ctx, client, encryptor)
	if err != nil {
		return updatedTemplates, err
	}
	return updatedTemplates + updatedMonitors, nil
}

func migrateChannelMonitorTemplatePayloads(ctx context.Context, client *dbent.Client, encryptor service.SecretEncryptor) (int, error) {
	rows, err := client.ChannelMonitorRequestTemplate.Query().All(ctx)
	if err != nil {
		return 0, fmt.Errorf("query channel monitor template request payloads: %w", err)
	}
	updated := 0
	for _, row := range rows {
		headers, body, changed, retiredAttribution, err := migrateChannelMonitorPayloadMaps(encryptor, row.ExtraHeaders, row.BodyOverride)
		if err != nil {
			return updated, fmt.Errorf("migrate channel monitor template %d request payload: %w", row.ID, err)
		}
		if !changed {
			continue
		}
		updater := client.ChannelMonitorRequestTemplate.UpdateOneID(row.ID).SetExtraHeaders(headers)
		if retiredAttribution {
			updater = updater.SetBodyOverrideMode(service.MonitorBodyOverrideModeOff)
		}
		if body == nil {
			updater = updater.ClearBodyOverride()
		} else {
			updater = updater.SetBodyOverride(body)
		}
		if _, err := updater.Save(ctx); err != nil {
			return updated, fmt.Errorf("update channel monitor template %d request payload: %w", row.ID, err)
		}
		updated++
	}
	return updated, nil
}

func migrateChannelMonitorSnapshotPayloads(ctx context.Context, client *dbent.Client, encryptor service.SecretEncryptor) (int, error) {
	rows, err := client.ChannelMonitor.Query().All(ctx)
	if err != nil {
		return 0, fmt.Errorf("query channel monitor snapshot request payloads: %w", err)
	}
	updated := 0
	for _, row := range rows {
		headers, body, changed, retiredAttribution, err := migrateChannelMonitorPayloadMaps(encryptor, row.ExtraHeaders, row.BodyOverride)
		if err != nil {
			return updated, fmt.Errorf("migrate channel monitor %d request payload: %w", row.ID, err)
		}
		if !changed {
			continue
		}
		updater := client.ChannelMonitor.UpdateOneID(row.ID).SetExtraHeaders(headers)
		if retiredAttribution {
			updater = updater.SetBodyOverrideMode(service.MonitorBodyOverrideModeOff)
		}
		if body == nil {
			updater = updater.ClearBodyOverride()
		} else {
			updater = updater.SetBodyOverride(body)
		}
		if _, err := updater.Save(ctx); err != nil {
			return updated, fmt.Errorf("update channel monitor %d request payload: %w", row.ID, err)
		}
		updated++
	}
	return updated, nil
}

func migrateChannelMonitorPayloadMaps(encryptor service.SecretEncryptor, headers map[string]string, body map[string]any) (map[string]string, map[string]any, bool, bool, error) {
	sealedHeaders := headers
	headersChanged := false
	retiredAttribution := false
	if service.HasChannelMonitorExtraHeadersEnvelopeMarker(headers) {
		if _, err := service.OpenChannelMonitorExtraHeaders(encryptor, headers); err != nil {
			if !errors.Is(err, service.ErrChannelMonitorTemplateOfficialClientAttribution) {
				return nil, nil, false, false, err
			}
			sealedHeaders, err = service.SealChannelMonitorExtraHeaders(encryptor, map[string]string{})
			if err != nil {
				return nil, nil, false, false, err
			}
			headersChanged = true
			retiredAttribution = true
		}
	} else {
		var err error
		sealedHeaders, err = service.SealChannelMonitorExtraHeaders(encryptor, headers)
		if err != nil {
			if !errors.Is(err, service.ErrChannelMonitorTemplateOfficialClientAttribution) {
				return nil, nil, false, false, err
			}
			sealedHeaders, err = service.SealChannelMonitorExtraHeaders(encryptor, map[string]string{})
			if err != nil {
				return nil, nil, false, false, err
			}
			retiredAttribution = true
		}
		headersChanged = true
	}

	sealedBody := body
	bodyChanged := false
	if body != nil {
		if service.HasChannelMonitorBodyOverrideEnvelopeMarker(body) {
			if _, err := service.OpenChannelMonitorBodyOverride(encryptor, body); err != nil {
				if !errors.Is(err, service.ErrChannelMonitorTemplateOfficialClientAttribution) {
					return nil, nil, false, false, err
				}
				sealedBody = nil
				bodyChanged = true
				retiredAttribution = true
			}
		} else {
			var err error
			sealedBody, err = service.SealChannelMonitorBodyOverride(encryptor, body)
			if err != nil {
				if !errors.Is(err, service.ErrChannelMonitorTemplateOfficialClientAttribution) {
					return nil, nil, false, false, err
				}
				sealedBody = nil
				retiredAttribution = true
			}
			bodyChanged = true
		}
	}
	return sealedHeaders, sealedBody, headersChanged || bodyChanged, retiredAttribution, nil
}

func migrateBackupS3Config(ctx context.Context, client *dbent.Client, encryptor service.SecretEncryptor) (int, error) {
	row, err := client.Setting.Query().Where(setting.KeyEQ(backupS3ConfigSettingKey)).Only(ctx)
	if dbent.IsNotFound(err) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("query backup S3 config: %w", err)
	}
	if strings.TrimSpace(row.Value) == "" {
		return 0, nil
	}
	var cfg service.BackupS3Config
	if err := json.Unmarshal([]byte(row.Value), &cfg); err != nil {
		return 0, fmt.Errorf("decode backup S3 config: %w", err)
	}
	if strings.TrimSpace(cfg.SecretAccessKey) == "" {
		return 0, nil
	}
	bound, changed, err := migrateLegacyOrPlaintextSecret(encryptor, service.SecretDomainBackupS3, cfg.SecretAccessKey)
	if err != nil {
		return 0, fmt.Errorf("migrate backup S3 secret: %w", err)
	}
	if !changed {
		return 0, nil
	}
	cfg.SecretAccessKey = bound
	raw, err := json.Marshal(&cfg)
	if err != nil {
		return 0, fmt.Errorf("encode backup S3 config: %w", err)
	}
	if _, err := client.Setting.UpdateOneID(row.ID).
		Where(setting.ValueEQ(row.Value)).
		SetValue(string(raw)).
		Save(ctx); err != nil {
		return 0, fmt.Errorf("update backup S3 config with concurrency guard: %w", err)
	}
	return 1, nil
}

func migrateContentModerationConfig(ctx context.Context, client *dbent.Client, encryptor service.SecretEncryptor) (int, error) {
	row, err := client.Setting.Query().Where(setting.KeyEQ(service.SettingKeyContentModerationConfig)).Only(ctx)
	if dbent.IsNotFound(err) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("query content moderation config: %w", err)
	}
	if strings.TrimSpace(row.Value) == "" {
		return 0, nil
	}
	var cfg service.ContentModerationConfig
	if err := json.Unmarshal([]byte(row.Value), &cfg); err != nil {
		return 0, fmt.Errorf("decode content moderation config: %w", err)
	}
	changed, err := migrateContentModerationKeys(&cfg, encryptor)
	if err != nil {
		return 0, err
	}
	if !changed {
		return 0, nil
	}
	raw, err := json.Marshal(&cfg)
	if err != nil {
		return 0, fmt.Errorf("encode content moderation config: %w", err)
	}
	if _, err := client.Setting.UpdateOneID(row.ID).
		Where(setting.ValueEQ(row.Value)).
		SetValue(string(raw)).
		Save(ctx); err != nil {
		return 0, fmt.Errorf("update content moderation config with concurrency guard: %w", err)
	}
	return 1, nil
}

func migrateContentModerationKeys(cfg *service.ContentModerationConfig, encryptor service.SecretEncryptor) (bool, error) {
	plaintextKeys := normalizeSecretList(append(append([]string{}, cfg.APIKeys...), cfg.APIKey))
	decryptedKeys := make([]string, 0, len(cfg.EncryptedAPIKeys))
	changed := len(plaintextKeys) > 0
	for i, ciphertext := range cfg.EncryptedAPIKeys {
		bound, plaintext, itemChanged, err := migrateLegacyCiphertextWithPlaintext(encryptor, service.SecretDomainContentModeration, ciphertext)
		if err != nil {
			return false, fmt.Errorf("migrate content moderation key %d: %w", i, err)
		}
		decryptedKeys = append(decryptedKeys, plaintext)
		cfg.EncryptedAPIKeys[i] = bound
		changed = changed || itemChanged
	}
	decryptedKeys = normalizeSecretList(decryptedKeys)
	if len(decryptedKeys) > 0 && len(plaintextKeys) > 0 && !reflect.DeepEqual(decryptedKeys, plaintextKeys) {
		return false, errors.New("content moderation plaintext compatibility keys do not match encrypted keys")
	}
	if len(decryptedKeys) == 0 && len(plaintextKeys) > 0 {
		cfg.EncryptedAPIKeys = make([]string, 0, len(plaintextKeys))
		for i, plaintext := range plaintextKeys {
			bound, err := service.EncryptForSecretDomain(encryptor, service.SecretDomainContentModeration, plaintext)
			if err != nil {
				return false, fmt.Errorf("encrypt plaintext content moderation key %d: %w", i, err)
			}
			cfg.EncryptedAPIKeys = append(cfg.EncryptedAPIKeys, bound)
		}
	}
	if changed {
		cfg.APIKey = ""
		cfg.APIKeys = nil
	}
	return changed, nil
}

func migrateLegacyCiphertext(encryptor service.SecretEncryptor, domain, ciphertext string) (string, bool, error) {
	bound, _, changed, err := migrateLegacyCiphertextWithPlaintext(encryptor, domain, ciphertext)
	return bound, changed, err
}

func migrateLegacyCiphertextWithPlaintext(encryptor service.SecretEncryptor, domain, ciphertext string) (bound, plaintext string, changed bool, err error) {
	if strings.TrimSpace(ciphertext) == "" {
		return "", "", false, errors.New("empty ciphertext")
	}
	if strings.HasPrefix(ciphertext, secretDomainCiphertextPrefix) {
		plaintext, err = service.DecryptForSecretDomain(encryptor, domain, ciphertext)
		return ciphertext, plaintext, false, err
	}
	plaintext, err = decryptLegacySecretForMigration(encryptor, domain, ciphertext)
	if err != nil {
		return "", "", false, err
	}
	if plaintext == "" {
		return "", "", false, errors.New("legacy ciphertext decrypted to an empty secret")
	}
	bound, err = service.EncryptForSecretDomain(encryptor, domain, plaintext)
	return bound, plaintext, true, err
}

func migrateLegacyOrPlaintextSecret(encryptor service.SecretEncryptor, domain, value string) (string, bool, error) {
	if strings.HasPrefix(value, secretDomainCiphertextPrefix) {
		if _, err := service.DecryptForSecretDomain(encryptor, domain, value); err != nil {
			return "", false, err
		}
		return value, false, nil
	}
	plaintext, err := decryptLegacySecretForMigration(encryptor, domain, value)
	if err != nil {
		// backup_s3_config historically stored unmarked plaintext. Treat only this
		// schema's non-v2 value as plaintext, then remove the ambiguity forever.
		plaintext = value
	}
	bound, err := service.EncryptForSecretDomain(encryptor, domain, plaintext)
	return bound, true, err
}

type v2DomainMigrationDecryptor interface {
	decryptV2ForMigration(domain, ciphertext string) (string, error)
}

func decryptLegacySecretForMigration(encryptor service.SecretEncryptor, domain, ciphertext string) (string, error) {
	if strings.HasPrefix(ciphertext, legacySecretDomainCiphertextPrefixV2) {
		migrationDecryptor, ok := encryptor.(v2DomainMigrationDecryptor)
		if !ok {
			return "", errors.New("v2 domain migration decryptor is unavailable")
		}
		return migrationDecryptor.decryptV2ForMigration(domain, ciphertext)
	}
	return encryptor.Decrypt(ciphertext)
}

func normalizeSecretList(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}
