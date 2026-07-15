package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	dbaccount "github.com/Wei-Shaw/sub2api/ent/account"
	dbpredicate "github.com/Wei-Shaw/sub2api/ent/predicate"
	"github.com/Wei-Shaw/sub2api/ent/schema/mixins"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

// AccountCredentialsEncryptionResult summarizes a one-shot credentials encryption pass.
type AccountCredentialsEncryptionResult struct {
	Scanned         int
	NeedsEncryption int
	Updated         int
}

type DomainSecretMigrationResult struct {
	AccountCredentials       int
	TOTPSecrets              int
	ChannelMonitorKeys       int
	ChannelMonitorPayloads   int
	BackupS3Configs          int
	ContentModerationConfigs int
	ProxyCredentials         int
	SettingSecrets           int
}

// EncryptPlaintextAccountCredentials rewrites legacy plaintext account credentials
// through the same encryption envelope used by accountRepository writes.
func EncryptPlaintextAccountCredentials(ctx context.Context, client *dbent.Client, encryptor service.SecretEncryptor, dryRun bool) (AccountCredentialsEncryptionResult, error) {
	if client == nil {
		return AccountCredentialsEncryptionResult{}, errors.New("nil ent client")
	}
	if encryptor == nil {
		return AccountCredentialsEncryptionResult{}, errors.New("nil credential encryptor")
	}

	// Include soft-deleted accounts: their credentials remain recoverable data
	// at rest and must not retain the relocatable legacy envelope.
	migrationCtx := mixins.SkipSoftDelete(ctx)
	accounts, err := client.Account.Query().All(migrationCtx)
	if err != nil {
		return AccountCredentialsEncryptionResult{}, err
	}

	result := AccountCredentialsEncryptionResult{Scanned: len(accounts)}
	for _, account := range accounts {
		encrypted, err := migrateAccountCredentials(account.Credentials, encryptor)
		if err != nil {
			return result, err
		}
		if reflect.DeepEqual(encrypted, account.Credentials) {
			continue
		}

		result.NeedsEncryption++
		if dryRun {
			continue
		}
		credentialsPredicate, err := accountCredentialsEqual(account.Credentials)
		if err != nil {
			return result, err
		}
		if _, err := client.Account.UpdateOneID(account.ID).
			Where(credentialsPredicate).
			SetCredentials(encrypted).
			Save(migrationCtx); err != nil {
			return result, fmt.Errorf("update account %d credentials with concurrency guard: %w", account.ID, err)
		}
		result.Updated++
	}

	return result, nil
}

func accountCredentialsEqual(credentials map[string]any) (dbpredicate.Account, error) {
	raw, err := json.Marshal(credentials)
	if err != nil {
		return nil, fmt.Errorf("marshal account credentials concurrency guard: %w", err)
	}
	return func(selector *entsql.Selector) {
		selector.Where(entsql.P(func(builder *entsql.Builder) {
			builder.Ident(selector.C(dbaccount.FieldCredentials)).WriteOp(entsql.OpEQ)
			if builder.Dialect() == dialect.Postgres {
				builder.Arg(string(raw)).WriteString("::jsonb")
			} else {
				builder.Arg(raw)
			}
		}))
	}, nil
}

func migrateAccountCredentials(in map[string]any, encryptor service.SecretEncryptor) (map[string]any, error) {
	if in == nil {
		return nil, nil
	}
	out := make(map[string]any, len(in))
	for key, value := range in {
		migrated, err := migrateCredentialValue(key, value, encryptor)
		if err != nil {
			return nil, fmt.Errorf("migrate credential %q: %w", key, err)
		}
		out[key] = migrated
	}
	return out, nil
}

func migrateCredentialValue(key string, value any, encryptor service.SecretEncryptor) (any, error) {
	alg, ciphertext, envelope, err := parseEncryptedCredentialEnvelope(value)
	if err != nil {
		return nil, err
	}
	if envelope {
		return migrateCredentialEnvelope(value, alg, ciphertext, encryptor)
	}
	if isSensitiveCredentialKey(key) {
		return encryptCredentialJSON(value, encryptor)
	}
	switch typed := value.(type) {
	case map[string]any:
		return migrateAccountCredentials(typed, encryptor)
	case []any:
		return migrateCredentialList(typed, encryptor)
	default:
		return copyJSONValue(value), nil
	}
}

func migrateCredentialEnvelope(value any, alg, ciphertext string, encryptor service.SecretEncryptor) (any, error) {
	if alg == encryptedCredentialAlgV3 {
		if _, err := service.DecryptForSecretDomain(encryptor, service.SecretDomainAccountCredential, ciphertext); err != nil {
			return nil, fmt.Errorf("validate domain-bound credential: %w", err)
		}
		return copyJSONValue(value), nil
	}
	plaintext, err := decryptCredentialForMigration(alg, ciphertext, encryptor)
	if err != nil {
		return nil, fmt.Errorf("decrypt legacy credential: %w", err)
	}
	var decoded any
	if err := json.Unmarshal([]byte(plaintext), &decoded); err != nil {
		return nil, fmt.Errorf("decode legacy credential: %w", err)
	}
	return encryptCredentialJSON(decoded, encryptor)
}

func decryptCredentialForMigration(alg, ciphertext string, encryptor service.SecretEncryptor) (string, error) {
	if alg == encryptedCredentialAlgV2 {
		_, plaintext, _, err := migrateLegacyCiphertextWithPlaintext(
			encryptor,
			service.SecretDomainAccountCredential,
			ciphertext,
		)
		return plaintext, err
	}
	return encryptor.Decrypt(ciphertext)
}

func migrateCredentialList(in []any, encryptor service.SecretEncryptor) ([]any, error) {
	out := make([]any, len(in))
	for i, item := range in {
		migrated, err := migrateCredentialValue("", item, encryptor)
		if err != nil {
			return nil, err
		}
		out[i] = migrated
	}
	return out, nil
}
