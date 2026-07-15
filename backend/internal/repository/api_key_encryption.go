package repository

import (
	"context"
	"crypto/subtle"
	"fmt"
	"strings"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/apikey"
	"github.com/Wei-Shaw/sub2api/ent/schema/mixins"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

const (
	apiKeyEncryptedStoragePrefix = "enc:v1:"
	apiKeyMigrationBatchSize     = 500
)

func (r *apiKeyRepository) lookupAPIKeyLocator(plaintext string) (string, error) {
	if r == nil || r.keyProtector == nil {
		return "", fmt.Errorf("API key protector is not configured")
	}
	if strings.TrimSpace(plaintext) == "" {
		return "", fmt.Errorf("API key plaintext is empty")
	}
	locator := strings.ToLower(strings.TrimSpace(r.keyProtector.LookupLocator(plaintext)))
	if len(locator) != 64 {
		return "", fmt.Errorf("API key protector returned invalid lookup locator")
	}
	return locator, nil
}

func (r *apiKeyRepository) encryptAPIKeyForStorage(plaintext string, userID int64, purpose, locator string) (string, error) {
	if r == nil || r.keyProtector == nil {
		return "", fmt.Errorf("API key protector is not configured")
	}
	if strings.TrimSpace(plaintext) == "" {
		return "", fmt.Errorf("API key plaintext is empty")
	}
	ciphertext, err := r.keyProtector.EncryptAPIKey(plaintext, apiKeyAssociatedData(userID, purpose, locator))
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(ciphertext) == "" {
		return "", fmt.Errorf("API key encryptor returned empty ciphertext")
	}
	return apiKeyEncryptedStoragePrefix + ciphertext, nil
}

func (r *apiKeyRepository) decryptAPIKeyFromStorage(stored string, userID int64, purpose, locator string) (string, error) {
	if !strings.HasPrefix(stored, apiKeyEncryptedStoragePrefix) {
		return "", fmt.Errorf("plaintext API key storage is forbidden")
	}
	if r == nil || r.keyProtector == nil {
		return "", fmt.Errorf("API key protector is not configured")
	}
	ciphertext := strings.TrimPrefix(stored, apiKeyEncryptedStoragePrefix)
	if ciphertext == "" {
		return "", fmt.Errorf("API key ciphertext is empty")
	}
	if strings.TrimSpace(locator) == "" {
		return "", fmt.Errorf("API key lookup locator is empty")
	}
	plaintext, err := r.keyProtector.DecryptAPIKey(ciphertext, apiKeyAssociatedData(userID, purpose, locator))
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(plaintext) == "" {
		return "", fmt.Errorf("decrypted API key is empty")
	}
	return plaintext, nil
}

func (r *apiKeyRepository) apiKeyEntityToService(m *dbent.APIKey) (*service.APIKey, error) {
	out := apiKeyEntityToService(m)
	if out == nil {
		return nil, nil
	}
	plaintext, err := r.decryptAPIKeyFromStorage(m.Key, m.UserID, m.Purpose, derefString(m.KeyHash))
	if err != nil {
		return nil, fmt.Errorf("decrypt API key %d: %w", m.ID, err)
	}
	out.Key = plaintext
	return out, nil
}

// MigratePlaintextAPIKeysToEncrypted rewrites legacy plaintext API keys before
// the HTTP server starts. Rows are processed in bounded batches and updated
// with compare-and-swap so a concurrent change cannot be silently overwritten.
// Only incomplete storage rows are scanned on normal startup. Complete
// ciphertext is decrypted and authenticated when that specific key is read,
// avoiding an O(all historical keys) restart path while remaining fail-closed.
func (r *apiKeyRepository) MigratePlaintextAPIKeysToEncrypted(ctx context.Context) (int, error) {
	if r == nil || r.client == nil || r.keyProtector == nil {
		return 0, fmt.Errorf("API key plaintext migration requires a database client and protector")
	}

	migrationCtx := mixins.SkipSoftDelete(ctx)
	lastID := int64(0)
	migrated := 0
	for {
		rows, err := r.client.APIKey.Query().
			Where(
				apikey.IDGT(lastID),
				apikey.Or(
					apikey.Not(apikey.KeyHasPrefix(apiKeyEncryptedStoragePrefix)),
					apikey.KeyHashIsNil(),
					apikey.KeyHashEQ(""),
					apikey.KeyPrefixEQ(""),
				),
			).
			Order(dbent.Asc(apikey.FieldID)).
			Limit(apiKeyMigrationBatchSize).
			All(migrationCtx)
		if err != nil {
			return migrated, fmt.Errorf("list API keys for encryption migration: %w", err)
		}
		if len(rows) == 0 {
			if r.validateStorageConstraint {
				if err := r.validateEncryptedStorageConstraint(ctx); err != nil {
					return migrated, err
				}
			}
			return migrated, nil
		}

		for _, row := range rows {
			lastID = row.ID
			if row.DeletedAt != nil && strings.HasPrefix(row.Key, "__deleted__") {
				continue
			}
			legacyPlaintext := !strings.HasPrefix(row.Key, apiKeyEncryptedStoragePrefix)
			plaintext := row.Key
			if !legacyPlaintext {
				plaintext, err = r.decryptAPIKeyFromStorage(row.Key, row.UserID, row.Purpose, derefString(row.KeyHash))
				if err != nil {
					return migrated, fmt.Errorf("API key %d ciphertext is unreadable: %w", row.ID, err)
				}
			}
			expectedHash := r.keyProtector.LookupLocator(plaintext)
			if !legacyPlaintext && (row.KeyHash == nil || !strings.EqualFold(strings.TrimSpace(*row.KeyHash), expectedHash)) {
				return migrated, fmt.Errorf("API key %d hash does not match stored secret", row.ID)
			}
			expectedPrefix := service.APIKeyPrefixForStorage(plaintext)
			storedKey := row.Key
			needsUpdate := !strings.HasPrefix(row.Key, apiKeyEncryptedStoragePrefix) ||
				row.KeyHash == nil || strings.TrimSpace(*row.KeyHash) == "" ||
				row.KeyPrefix != expectedPrefix
			if !needsUpdate {
				continue
			}
			if legacyPlaintext {
				storedKey, err = r.encryptAPIKeyForStorage(plaintext, row.UserID, row.Purpose, expectedHash)
				if err != nil {
					return migrated, fmt.Errorf("encrypt API key %d: %w", row.ID, err)
				}
			}
			updated, err := r.client.APIKey.Update().
				Where(apikey.IDEQ(row.ID), apikey.KeyEQ(row.Key)).
				SetKey(storedKey).
				SetKeyHash(expectedHash).
				SetKeyPrefix(expectedPrefix).
				Save(migrationCtx)
			if err != nil {
				return migrated, fmt.Errorf("persist encrypted API key %d: %w", row.ID, err)
			}
			if updated != 1 {
				if err := r.verifyConcurrentAPIKeyMigration(migrationCtx, row.ID, plaintext, expectedHash, expectedPrefix); err != nil {
					return migrated, err
				}
				continue
			}
			migrated++
		}
	}
}

func (r *apiKeyRepository) validateEncryptedStorageConstraint(ctx context.Context) error {
	if r.sql == nil {
		return fmt.Errorf("validate API key encrypted storage: database is unavailable")
	}
	if _, err := r.sql.ExecContext(ctx, `ALTER TABLE api_keys VALIDATE CONSTRAINT chk_api_keys_encrypted_storage`); err != nil {
		return fmt.Errorf("validate API key encrypted storage constraint: %w", err)
	}
	rows, err := r.sql.QueryContext(ctx, `
		SELECT COUNT(*) FROM api_keys
		WHERE NOT (
			(deleted_at IS NULL AND key LIKE 'enc:v1:%')
			OR
			(deleted_at IS NOT NULL AND (key LIKE 'enc:v1:%' OR key LIKE '__deleted__%'))
		)`)
	if err != nil {
		return fmt.Errorf("verify API key encrypted storage rows: %w", err)
	}
	defer rows.Close()
	var invalid int
	if !rows.Next() {
		return fmt.Errorf("verify API key encrypted storage rows: count row is missing")
	}
	if err := rows.Scan(&invalid); err != nil {
		return fmt.Errorf("verify API key encrypted storage rows: %w", err)
	}
	if invalid != 0 {
		return fmt.Errorf("verify API key encrypted storage rows: %d invalid row(s)", invalid)
	}
	return nil
}

func (r *apiKeyRepository) verifyConcurrentAPIKeyMigration(ctx context.Context, id int64, plaintext, expectedHash, expectedPrefix string) error {
	current, err := r.client.APIKey.Query().Where(apikey.IDEQ(id)).Only(ctx)
	if err != nil {
		return fmt.Errorf("API key %d changed concurrently and cannot be reloaded: %w", id, err)
	}
	if current.DeletedAt != nil && strings.HasPrefix(current.Key, "__deleted__") {
		return nil
	}
	if !strings.HasPrefix(current.Key, apiKeyEncryptedStoragePrefix) || current.KeyHash == nil ||
		!strings.EqualFold(strings.TrimSpace(*current.KeyHash), expectedHash) || current.KeyPrefix != expectedPrefix {
		return fmt.Errorf("API key %d changed concurrently to an unexpected state", id)
	}
	decrypted, err := r.decryptAPIKeyFromStorage(current.Key, current.UserID, current.Purpose, derefString(current.KeyHash))
	if err != nil {
		return fmt.Errorf("API key %d concurrent ciphertext is unreadable: %w", id, err)
	}
	if subtle.ConstantTimeCompare([]byte(decrypted), []byte(plaintext)) != 1 {
		return fmt.Errorf("API key %d changed concurrently to a different secret", id)
	}
	return nil
}

func apiKeyAssociatedData(userID int64, purpose, locator string) string {
	return fmt.Sprintf("user=%d\npurpose=%s\nlocator=%s", userID, strings.TrimSpace(purpose), strings.ToLower(strings.TrimSpace(locator)))
}
