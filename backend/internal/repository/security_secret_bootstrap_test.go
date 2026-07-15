package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/enttest"
	"github.com/Wei-Shaw/sub2api/ent/securitysecret"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	_ "modernc.org/sqlite"
)

func newSecuritySecretTestClient(t *testing.T) *dbent.Client {
	t.Helper()
	name := strings.ReplaceAll(t.Name(), "/", "_")
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared&_fk=1", name)

	db, err := sql.Open("sqlite", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	_, err = db.Exec("PRAGMA foreign_keys = ON")
	require.NoError(t, err)

	drv := entsql.OpenDB(dialect.SQLite, db)
	client := enttest.NewClient(t, enttest.WithOptions(dbent.Driver(drv)))
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func TestEnsureBootstrapSecretsNilInputs(t *testing.T) {
	encryptor := newSecuritySecretTestEncryptor(t)
	err := ensureBootstrapSecrets(context.Background(), nil, &config.Config{}, encryptor)
	require.Error(t, err)
	require.Contains(t, err.Error(), "nil ent client")

	client := newSecuritySecretTestClient(t)
	err = ensureBootstrapSecrets(context.Background(), client, nil, encryptor)
	require.Error(t, err)
	require.Contains(t, err.Error(), "nil config")

	err = ensureBootstrapSecrets(context.Background(), client, &config.Config{}, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "encryptor")
}

func TestEnsureBootstrapSecretsRejectsMissingJWTSecret(t *testing.T) {
	client := newSecuritySecretTestClient(t)
	cfg := &config.Config{}
	encryptor := newSecuritySecretTestEncryptor(t)

	err := ensureBootstrapSecrets(context.Background(), client, cfg, encryptor)
	require.Error(t, err)
	require.Contains(t, err.Error(), "JWT_SECRET is required")
	require.Empty(t, cfg.JWT.Secret)
	count, countErr := client.SecuritySecret.Query().Where(securitysecret.KeyEQ(securitySecretKeyJWT)).Count(context.Background())
	require.NoError(t, countErr)
	require.Zero(t, count)
}

func TestEnsureBootstrapSecretsLoadExistingJWTSecret(t *testing.T) {
	client := newSecuritySecretTestClient(t)
	_, err := client.SecuritySecret.Create().SetKey(securitySecretKeyJWT).SetValue("existing-jwt-secret-32bytes-long!!!!").Save(context.Background())
	require.NoError(t, err)

	cfg := &config.Config{}
	encryptor := newSecuritySecretTestEncryptor(t)
	err = ensureBootstrapSecrets(context.Background(), client, cfg, encryptor)
	require.NoError(t, err)
	require.Equal(t, "existing-jwt-secret-32bytes-long!!!!", cfg.JWT.Secret)
	stored, err := client.SecuritySecret.Query().Where(securitysecret.KeyEQ(securitySecretKeyJWT)).Only(context.Background())
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(stored.Value, secretDomainCiphertextPrefix))
}

func TestEnsureBootstrapSecretsRejectInvalidStoredSecret(t *testing.T) {
	client := newSecuritySecretTestClient(t)
	_, err := client.SecuritySecret.Create().SetKey(securitySecretKeyJWT).SetValue("too-short").Save(context.Background())
	require.NoError(t, err)

	cfg := &config.Config{}
	err = ensureBootstrapSecrets(context.Background(), client, cfg, newSecuritySecretTestEncryptor(t))
	require.Error(t, err)
	require.Contains(t, err.Error(), "at least 32 bytes")
}

func TestEnsureBootstrapSecretsPersistConfiguredJWTSecret(t *testing.T) {
	client := newSecuritySecretTestClient(t)
	cfg := &config.Config{
		JWT: config.JWTConfig{Secret: "configured-jwt-secret-32bytes-long!!"},
	}

	encryptor := newSecuritySecretTestEncryptor(t)
	err := ensureBootstrapSecrets(context.Background(), client, cfg, encryptor)
	require.NoError(t, err)

	stored, err := client.SecuritySecret.Query().Where(securitysecret.KeyEQ(securitySecretKeyJWT)).Only(context.Background())
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(stored.Value, secretDomainCiphertextPrefix))
	plaintext, err := service.DecryptForSecretDomain(encryptor, service.SecretDomainJWTHMAC, stored.Value)
	require.NoError(t, err)
	require.Equal(t, "configured-jwt-secret-32bytes-long!!", plaintext)
}

func TestEnsureBootstrapSecretsEncryptedSecondBootstrapIsIdempotent(t *testing.T) {
	client := newSecuritySecretTestClient(t)
	encryptor := newSecuritySecretTestEncryptor(t)
	first := &config.Config{JWT: config.JWTConfig{Secret: "configured-jwt-secret-32bytes-long!!"}}
	require.NoError(t, ensureBootstrapSecrets(context.Background(), client, first, encryptor))
	storedBefore, err := client.SecuritySecret.Query().Where(securitysecret.KeyEQ(securitySecretKeyJWT)).Only(context.Background())
	require.NoError(t, err)

	second := &config.Config{}
	require.NoError(t, ensureBootstrapSecrets(context.Background(), client, second, encryptor))
	require.Equal(t, first.JWT.Secret, second.JWT.Secret)
	storedAfter, err := client.SecuritySecret.Query().Where(securitysecret.KeyEQ(securitySecretKeyJWT)).Only(context.Background())
	require.NoError(t, err)
	require.Equal(t, storedBefore.Value, storedAfter.Value)
}

func TestEnsureBootstrapSecretsRejectsWrongRootAndWrongDomain(t *testing.T) {
	t.Run("wrong root", func(t *testing.T) {
		client := newSecuritySecretTestClient(t)
		firstCfg := domainKeyTestConfig()
		firstCfg.JWT.Secret = "configured-jwt-secret-32bytes-long!!"
		firstEncryptor, err := NewAESEncryptor(firstCfg)
		require.NoError(t, err)
		require.NoError(t, ensureBootstrapSecrets(context.Background(), client, firstCfg, firstEncryptor))

		secondCfg := domainKeyTestConfig()
		secondCfg.SecretEncryption.JWTHMACKey = strings.Repeat("e", 64)
		secondEncryptor, err := NewAESEncryptor(secondCfg)
		require.NoError(t, err)
		err = ensureBootstrapSecrets(context.Background(), client, secondCfg, secondEncryptor)
		require.Error(t, err)
		require.Contains(t, err.Error(), "decrypt")
	})

	t.Run("wrong domain", func(t *testing.T) {
		client := newSecuritySecretTestClient(t)
		encryptor := newSecuritySecretTestEncryptor(t)
		wrongDomainCiphertext, err := service.EncryptForSecretDomain(encryptor, service.SecretDomainTOTP, "configured-jwt-secret-32bytes-long!!")
		require.NoError(t, err)
		_, err = client.SecuritySecret.Create().
			SetKey(securitySecretKeyJWT).
			SetValue(wrongDomainCiphertext).
			Save(context.Background())
		require.NoError(t, err)

		err = ensureBootstrapSecrets(context.Background(), client, &config.Config{}, encryptor)
		require.Error(t, err)
		require.Contains(t, err.Error(), "decrypt")
	})
}

func TestEnsureBootstrapSecretsConfiguredSecretTooShort(t *testing.T) {
	client := newSecuritySecretTestClient(t)
	cfg := &config.Config{JWT: config.JWTConfig{Secret: "short"}}

	err := ensureBootstrapSecrets(context.Background(), client, cfg, newSecuritySecretTestEncryptor(t))
	require.Error(t, err)
	require.Contains(t, err.Error(), "at least 32 bytes")
}

func TestEnsureBootstrapSecretsConfiguredSecretMismatchFailsClosed(t *testing.T) {
	client := newSecuritySecretTestClient(t)
	_, err := client.SecuritySecret.Create().
		SetKey(securitySecretKeyJWT).
		SetValue("existing-jwt-secret-32bytes-long!!!!").
		Save(context.Background())
	require.NoError(t, err)

	cfg := &config.Config{JWT: config.JWTConfig{Secret: "another-configured-jwt-secret-32!!!!"}}
	err = ensureBootstrapSecrets(context.Background(), client, cfg, newSecuritySecretTestEncryptor(t))
	require.Error(t, err)
	require.Contains(t, err.Error(), "mismatches persisted")

	stored, err := client.SecuritySecret.Query().Where(securitysecret.KeyEQ(securitySecretKeyJWT)).Only(context.Background())
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(stored.Value, secretDomainCiphertextPrefix))
	require.Equal(t, "another-configured-jwt-secret-32!!!!", cfg.JWT.Secret)
}

func TestEnsureBootstrapSecretsRejectsUnreadableCiphertext(t *testing.T) {
	client := newSecuritySecretTestClient(t)
	_, err := client.SecuritySecret.Create().
		SetKey(securitySecretKeyJWT).
		SetValue(secretDomainCiphertextPrefix + "corrupt").
		Save(context.Background())
	require.NoError(t, err)

	err = ensureBootstrapSecrets(context.Background(), client, &config.Config{}, newSecuritySecretTestEncryptor(t))
	require.Error(t, err)
	require.Contains(t, err.Error(), "decrypt")
}

func TestEnsureBootstrapSecretsRejectsPersistedSigningSecretReusedAsEncryptionRoot(t *testing.T) {
	client := newSecuritySecretTestClient(t)
	cfg := domainKeyTestConfig()
	cfg.JWT.Secret = ""
	_, err := client.SecuritySecret.Create().
		SetKey(securitySecretKeyJWT).
		SetValue(cfg.SecretEncryption.JWTHMACKey).
		Save(context.Background())
	require.NoError(t, err)

	encryptor, err := NewAESEncryptor(cfg)
	require.NoError(t, err)
	err = ensureBootstrapSecrets(context.Background(), client, cfg, encryptor)
	require.Error(t, err)
	require.Contains(t, err.Error(), "must be distinct")
}

func newSecuritySecretTestEncryptor(t *testing.T) service.SecretEncryptor {
	t.Helper()
	encryptor, err := NewAESEncryptor(domainKeyTestConfig())
	require.NoError(t, err)
	return encryptor
}

func TestQuerySecuritySecretWithRetrySuccess(t *testing.T) {
	client := newSecuritySecretTestClient(t)
	created, err := client.SecuritySecret.Create().
		SetKey("retry_success_key").
		SetValue("retry-success-jwt-secret-value-32!!").
		Save(context.Background())
	require.NoError(t, err)

	got, err := querySecuritySecretWithRetry(context.Background(), client, "retry_success_key")
	require.NoError(t, err)
	require.Equal(t, created.ID, got.ID)
	require.Equal(t, "retry-success-jwt-secret-value-32!!", got.Value)
}

func TestQuerySecuritySecretWithRetryExhausted(t *testing.T) {
	client := newSecuritySecretTestClient(t)

	_, err := querySecuritySecretWithRetry(context.Background(), client, "retry_missing_key")
	require.Error(t, err)
	require.True(t, isSecretNotFoundError(err))
}

func TestQuerySecuritySecretWithRetryContextCanceled(t *testing.T) {
	client := newSecuritySecretTestClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), securitySecretReadRetryWait/2)
	defer cancel()

	_, err := querySecuritySecretWithRetry(ctx, client, "retry_ctx_cancel_key")
	require.Error(t, err)
	require.ErrorIs(t, err, context.DeadlineExceeded)
}

func TestQuerySecuritySecretWithRetryNonNotFoundError(t *testing.T) {
	client := newSecuritySecretTestClient(t)
	require.NoError(t, client.Close())

	_, err := querySecuritySecretWithRetry(context.Background(), client, "retry_closed_client_key")
	require.Error(t, err)
	require.False(t, isSecretNotFoundError(err))
}

func TestSecretNotFoundHelpers(t *testing.T) {
	require.False(t, isSecretNotFoundError(nil))
	require.False(t, isSQLNoRowsError(nil))

	require.True(t, isSQLNoRowsError(sql.ErrNoRows))
	require.True(t, isSQLNoRowsError(fmt.Errorf("wrapped: %w", sql.ErrNoRows)))
	require.True(t, isSQLNoRowsError(errors.New("sql: no rows in result set")))

	require.True(t, isSecretNotFoundError(sql.ErrNoRows))
	require.True(t, isSecretNotFoundError(errors.New("sql: no rows in result set")))
	require.False(t, isSecretNotFoundError(errors.New("some other error")))
}
