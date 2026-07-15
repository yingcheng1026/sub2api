package repository

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"
	"testing"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/apikey"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

type strictAPIKeyTestProtector struct{}

func (strictAPIKeyTestProtector) EncryptAPIKey(plaintext, associatedData string) (string, error) {
	if plaintext == "" {
		return "", fmt.Errorf("empty plaintext")
	}
	return "cipher:" + base64.RawURLEncoding.EncodeToString([]byte(associatedData)) + "." + base64.RawURLEncoding.EncodeToString([]byte(plaintext)), nil
}

func (strictAPIKeyTestProtector) DecryptAPIKey(ciphertext, associatedData string) (string, error) {
	prefix := "cipher:" + base64.RawURLEncoding.EncodeToString([]byte(associatedData)) + "."
	if !strings.HasPrefix(ciphertext, prefix) {
		return "", fmt.Errorf("invalid ciphertext")
	}
	decoded, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(ciphertext, prefix))
	if err != nil {
		return "", fmt.Errorf("invalid ciphertext: %w", err)
	}
	return string(decoded), nil
}

func (strictAPIKeyTestProtector) LookupLocator(plaintext string) string {
	return service.HashAPIKey("hmac:" + plaintext)
}

type countingAPIKeyTestProtector struct {
	decryptCalls int
}

func (p *countingAPIKeyTestProtector) EncryptAPIKey(plaintext, associatedData string) (string, error) {
	return strictAPIKeyTestProtector{}.EncryptAPIKey(plaintext, associatedData)
}

func (p *countingAPIKeyTestProtector) DecryptAPIKey(ciphertext, associatedData string) (string, error) {
	p.decryptCalls++
	return strictAPIKeyTestProtector{}.DecryptAPIKey(ciphertext, associatedData)
}

func (*countingAPIKeyTestProtector) LookupLocator(plaintext string) string {
	return strictAPIKeyTestProtector{}.LookupLocator(plaintext)
}

func TestAPIKeyRepositoryEncryptsKeyAtRestAndAuthenticatesByHash(t *testing.T) {
	repo, client := newAPIKeyRepoSQLite(t)
	repo.keyProtector = strictAPIKeyTestProtector{}
	ctx := context.Background()
	user := mustCreateAPIKeyRepoUser(t, ctx, client, "api-key-encryption@test.com")

	plaintext := "sk-api-key-encryption"
	key := &service.APIKey{
		UserID: user.ID,
		Key:    plaintext,
		Name:   "encrypted key",
		Status: service.StatusActive,
	}
	require.NoError(t, repo.Create(ctx, key))

	stored, err := client.APIKey.Query().Where(apikey.IDEQ(key.ID)).Only(ctx)
	require.NoError(t, err)
	require.NotEqual(t, plaintext, stored.Key)
	require.True(t, strings.HasPrefix(stored.Key, apiKeyEncryptedStoragePrefix))
	require.Equal(t, repo.APIKeyAuthCacheLocator(plaintext), valueOrEmpty(stored.KeyHash))

	byID, err := repo.GetByID(ctx, key.ID)
	require.NoError(t, err)
	require.Equal(t, plaintext, byID.Key)

	byAuth, err := repo.GetByKeyForAuth(ctx, plaintext)
	require.NoError(t, err)
	require.Equal(t, key.ID, byAuth.ID)
	require.Empty(t, byAuth.Key, "auth repository must not load stored ciphertext as an API key")
}

func TestAPIKeyBulkReadsStayMetadataOnly(t *testing.T) {
	repo, client := newAPIKeyRepoSQLite(t)
	repo.keyProtector = strictAPIKeyTestProtector{}
	ctx := context.Background()
	user := mustCreateAPIKeyRepoUser(t, ctx, client, "api-key-metadata-only@test.com")
	key := &service.APIKey{
		UserID: user.ID, Key: "sk-api-key-metadata-only", Name: service.WalletUniversalAPIKeyName,
		Purpose: service.APIKeyPurposeWalletUniversal, Status: service.StatusActive,
	}
	require.NoError(t, repo.Create(ctx, key))
	require.NoError(t, repo.Create(ctx, &service.APIKey{
		UserID: user.ID, Key: "sk-newer-vip-standard", Name: "metadata vip",
		Purpose: service.APIKeyPurposeStandard, Status: service.StatusActive,
	}))

	listed, _, err := repo.ListByUserID(ctx, user.ID, pagination.PaginationParams{Page: 1, PageSize: 10}, service.APIKeyListFilters{})
	require.NoError(t, err)
	require.Len(t, listed, 2)
	require.Empty(t, listed[0].Key)

	searched, err := repo.SearchAPIKeys(ctx, user.ID, "metadata", 10)
	require.NoError(t, err)
	require.Len(t, searched, 1)
	require.Empty(t, searched[0].Key)

}

func TestAPIKeyEntityStringRedactsCiphertextAndLocator(t *testing.T) {
	locator := strings.Repeat("a", 64)
	entity := &dbent.APIKey{Key: "enc:v1:secret-ciphertext", KeyHash: &locator}
	printed := entity.String()
	require.NotContains(t, printed, "secret-ciphertext")
	require.NotContains(t, printed, locator)
	require.Contains(t, printed, "key=<sensitive>")
	require.Contains(t, printed, "key_hash=<sensitive>")
}

func TestAPIKeyRepositoryCreateFailsClosedWithoutEncryptor(t *testing.T) {
	repo, client := newAPIKeyRepoSQLite(t)
	repo.keyProtector = nil
	ctx := context.Background()
	user := mustCreateAPIKeyRepoUser(t, ctx, client, "api-key-no-encryptor@test.com")

	err := repo.Create(ctx, &service.APIKey{
		UserID: user.ID,
		Key:    "sk-api-key-no-encryptor",
		Name:   "must fail",
		Status: service.StatusActive,
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "protector")
}

func TestAPIKeyRepositoryMigratesLegacyPlaintextAndRuntimeRejectsCorruptCiphertext(t *testing.T) {
	repo, client := newAPIKeyRepoSQLite(t)
	repo.keyProtector = strictAPIKeyTestProtector{}
	ctx := context.Background()
	user := mustCreateAPIKeyRepoUser(t, ctx, client, "api-key-migration@test.com")

	legacyPlaintext := "sk-api-key-legacy"
	legacy, err := client.APIKey.Create().
		SetUserID(user.ID).
		SetKey(legacyPlaintext).
		SetName("legacy plaintext").
		SetStatus(service.StatusActive).
		Save(ctx)
	require.NoError(t, err)

	migrated, err := repo.MigratePlaintextAPIKeysToEncrypted(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, migrated)

	stored, err := client.APIKey.Query().Where(apikey.IDEQ(legacy.ID)).Only(ctx)
	require.NoError(t, err)
	require.NotEqual(t, legacyPlaintext, stored.Key)
	require.Equal(t, repo.APIKeyAuthCacheLocator(legacyPlaintext), valueOrEmpty(stored.KeyHash))
	require.Equal(t, service.APIKeyPrefixForStorage(legacyPlaintext), stored.KeyPrefix)

	loaded, err := repo.GetByID(ctx, legacy.ID)
	require.NoError(t, err)
	require.Equal(t, legacyPlaintext, loaded.Key)

	authenticated, err := repo.GetByKeyForAuth(ctx, legacyPlaintext)
	require.NoError(t, err)
	require.Equal(t, legacy.ID, authenticated.ID)
	require.Equal(t, repo.APIKeyAuthCacheLocator(legacyPlaintext), authenticated.KeyHash)

	_, err = repo.GetByKeyForAuth(ctx, legacyPlaintext+"-wrong")
	require.ErrorIs(t, err, service.ErrAPIKeyNotFound)

	_, err = client.APIKey.UpdateOneID(legacy.ID).
		SetKey(apiKeyEncryptedStoragePrefix + "corrupt").
		Save(ctx)
	require.NoError(t, err)
	migrated, err = repo.MigratePlaintextAPIKeysToEncrypted(ctx)
	require.NoError(t, err)
	require.Zero(t, migrated, "startup migration only scans incomplete storage rows")
	_, err = repo.GetByID(ctx, legacy.ID)
	require.Error(t, err)
	require.Contains(t, err.Error(), "decrypt")
}

func TestAPIKeyStartupMigrationSkipsAlreadyCompleteEncryptedRows(t *testing.T) {
	repo, client := newAPIKeyRepoSQLite(t)
	protector := &countingAPIKeyTestProtector{}
	repo.keyProtector = protector
	ctx := context.Background()
	user := mustCreateAPIKeyRepoUser(t, ctx, client, "api-key-no-full-boot-scan@test.com")
	require.NoError(t, repo.Create(ctx, &service.APIKey{
		UserID: user.ID,
		Key:    "sk-already-encrypted-complete",
		Name:   "complete",
		Status: service.StatusActive,
	}))
	protector.decryptCalls = 0

	migrated, err := repo.MigratePlaintextAPIKeysToEncrypted(ctx)
	require.NoError(t, err)
	require.Zero(t, migrated)
	require.Zero(t, protector.decryptCalls, "normal startup must not decrypt every historical API key")
}

func TestAPIKeyRepositoryRejectsPlaintextFallbackAfterCutover(t *testing.T) {
	repo, client := newAPIKeyRepoSQLite(t)
	repo.keyProtector = strictAPIKeyTestProtector{}
	ctx := context.Background()
	user := mustCreateAPIKeyRepoUser(t, ctx, client, "api-key-plaintext-rejected@test.com")
	legacy, err := client.APIKey.Create().
		SetUserID(user.ID).
		SetKey("sk-plaintext-must-not-be-read").
		SetKeyHash(repo.APIKeyAuthCacheLocator("sk-plaintext-must-not-be-read")).
		SetName("injected plaintext").
		SetStatus(service.StatusActive).
		Save(ctx)
	require.NoError(t, err)

	_, err = repo.GetByID(ctx, legacy.ID)
	require.Error(t, err)
	require.Contains(t, err.Error(), "plaintext API key storage is forbidden")
}

func TestAPIKeyMigrationDoesNotMistakeActiveLegacyKeyForDeleteTombstone(t *testing.T) {
	repo, client := newAPIKeyRepoSQLite(t)
	repo.keyProtector = strictAPIKeyTestProtector{}
	ctx := context.Background()
	user := mustCreateAPIKeyRepoUser(t, ctx, client, "api-key-active-tombstone-prefix@test.com")
	plaintext := "__deleted__active_legacy_key"
	legacy, err := client.APIKey.Create().
		SetUserID(user.ID).
		SetKey(plaintext).
		SetName("active legacy prefix").
		SetStatus(service.StatusActive).
		Save(ctx)
	require.NoError(t, err)

	migrated, err := repo.MigratePlaintextAPIKeysToEncrypted(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, migrated)
	loaded, err := repo.GetByID(ctx, legacy.ID)
	require.NoError(t, err)
	require.Equal(t, plaintext, loaded.Key)
}

func valueOrEmpty(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
