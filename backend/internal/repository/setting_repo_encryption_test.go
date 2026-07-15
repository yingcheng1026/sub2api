package repository

import (
	"context"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/ent/setting"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestSettingRepositoryEncryptsSensitiveValuesAtRest(t *testing.T) {
	ctx := context.Background()
	client := newSecuritySecretTestClient(t)
	encryptor := newSecuritySecretTestEncryptor(t)
	repo := NewSettingRepository(client, encryptor)

	require.NoError(t, repo.Set(ctx, service.SettingKeyAdminAPIKey, "admin-secret-value"))
	raw, err := client.Setting.Query().Where(setting.KeyEQ(service.SettingKeyAdminAPIKey)).Only(ctx)
	require.NoError(t, err)
	require.NotEqual(t, "admin-secret-value", raw.Value)
	require.True(t, strings.HasPrefix(raw.Value, secretDomainCiphertextPrefix))

	got, err := repo.GetValue(ctx, service.SettingKeyAdminAPIKey)
	require.NoError(t, err)
	require.Equal(t, "admin-secret-value", got)
}

func TestSettingRepositoryBatchEncryptionAndNonSensitiveCompatibility(t *testing.T) {
	ctx := context.Background()
	client := newSecuritySecretTestClient(t)
	repo := NewSettingRepository(client, newSecuritySecretTestEncryptor(t))

	require.NoError(t, repo.SetMultiple(ctx, map[string]string{
		service.SettingKeySMTPPassword: "smtp-secret",
		service.SettingKeySiteName:     "public-name",
	}))

	rawSecret, err := client.Setting.Query().Where(setting.KeyEQ(service.SettingKeySMTPPassword)).Only(ctx)
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(rawSecret.Value, secretDomainCiphertextPrefix))
	rawPublic, err := client.Setting.Query().Where(setting.KeyEQ(service.SettingKeySiteName)).Only(ctx)
	require.NoError(t, err)
	require.Equal(t, "public-name", rawPublic.Value)

	values, err := repo.GetMultiple(ctx, []string{service.SettingKeySMTPPassword, service.SettingKeySiteName})
	require.NoError(t, err)
	require.Equal(t, "smtp-secret", values[service.SettingKeySMTPPassword])
	require.Equal(t, "public-name", values[service.SettingKeySiteName])
	all, err := repo.GetAll(ctx)
	require.NoError(t, err)
	require.Equal(t, "smtp-secret", all[service.SettingKeySMTPPassword])
}

func TestSettingRepositorySensitiveEmptyValueAndUnreadableCiphertext(t *testing.T) {
	ctx := context.Background()
	client := newSecuritySecretTestClient(t)
	repo := NewSettingRepository(client, newSecuritySecretTestEncryptor(t))

	require.NoError(t, repo.Set(ctx, service.SettingKeyTurnstileSecretKey, ""))
	rawEmpty, err := client.Setting.Query().Where(setting.KeyEQ(service.SettingKeyTurnstileSecretKey)).Only(ctx)
	require.NoError(t, err)
	require.Empty(t, rawEmpty.Value)
	got, err := repo.GetValue(ctx, service.SettingKeyTurnstileSecretKey)
	require.NoError(t, err)
	require.Empty(t, got)

	require.NoError(t, client.Setting.Create().
		SetKey(service.SettingKeyAdminAPIKey).
		SetValue(secretDomainCiphertextPrefix+"corrupt").
		Exec(ctx))
	_, err = repo.GetValue(ctx, service.SettingKeyAdminAPIKey)
	require.Error(t, err)
	require.Contains(t, err.Error(), "decrypt")
}
