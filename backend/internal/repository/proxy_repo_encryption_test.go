package repository

import (
	"context"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestProxyRepositoryEncryptsPasswordAtRestAndDecryptsOnRead(t *testing.T) {
	ctx := context.Background()
	client := newCredentialEncryptionTestClient(t)
	encryptor := newDomainMigrationTestEncryptor(t)
	repo := newProxyRepositoryWithSQL(client, nil, encryptor)
	input := &service.Proxy{
		Name: "encrypted-proxy", Protocol: "http", Host: "proxy.example.test",
		Port: 8080, Username: "proxy-user", Password: "proxy-secret", Status: service.StatusActive,
	}

	require.NoError(t, repo.Create(ctx, input))
	raw, err := client.Proxy.Get(ctx, input.ID)
	require.NoError(t, err)
	require.NotNil(t, raw.Password)
	require.NotEqual(t, "proxy-secret", *raw.Password)
	plaintext, err := service.DecryptForSecretDomain(encryptor, service.SecretDomainProxyCredential, *raw.Password)
	require.NoError(t, err)
	require.Equal(t, "proxy-secret", plaintext)
	_, err = service.DecryptForSecretDomain(encryptor, service.SecretDomainAccountCredential, *raw.Password)
	require.Error(t, err)

	got, err := repo.GetByID(ctx, input.ID)
	require.NoError(t, err)
	require.Equal(t, "proxy-secret", got.Password)
	byIDs, err := repo.ListByIDs(ctx, []int64{input.ID})
	require.NoError(t, err)
	require.Len(t, byIDs, 1)
	require.Equal(t, "proxy-secret", byIDs[0].Password)
	listed, _, err := repo.List(ctx, pagination.PaginationParams{Page: 1, PageSize: 10})
	require.NoError(t, err)
	require.Len(t, listed, 1)
	require.Equal(t, "proxy-secret", listed[0].Password)
	active, err := repo.ListActive(ctx)
	require.NoError(t, err)
	require.Len(t, active, 1)
	require.Equal(t, "proxy-secret", active[0].Password)

	exists, err := repo.ExistsByHostPortAuth(ctx, input.Host, input.Port, input.Username, input.Password)
	require.NoError(t, err)
	require.True(t, exists)
	exists, err = repo.ExistsByHostPortAuth(ctx, input.Host, input.Port, input.Username, "wrong-secret")
	require.NoError(t, err)
	require.False(t, exists)

	input.Password = "rotated-secret"
	require.NoError(t, repo.Update(ctx, input))
	updatedRaw, err := client.Proxy.Get(ctx, input.ID)
	require.NoError(t, err)
	require.NotNil(t, updatedRaw.Password)
	require.NotEqual(t, "rotated-secret", *updatedRaw.Password)
	plaintext, err = service.DecryptForSecretDomain(encryptor, service.SecretDomainProxyCredential, *updatedRaw.Password)
	require.NoError(t, err)
	require.Equal(t, "rotated-secret", plaintext)
}

func TestProxyRepositoryFailsClosedOnUnreadablePasswordCiphertext(t *testing.T) {
	ctx := context.Background()
	client := newCredentialEncryptionTestClient(t)
	repo := newProxyRepositoryWithSQL(client, nil, newDomainMigrationTestEncryptor(t))
	raw, err := client.Proxy.Create().
		SetName("corrupt-proxy").
		SetProtocol("http").
		SetHost("proxy.example.test").
		SetPort(8080).
		SetPassword("sd:v3:not-valid").
		SetStatus(service.StatusActive).
		Save(ctx)
	require.NoError(t, err)

	got, err := repo.GetByID(ctx, raw.ID)

	require.Nil(t, got)
	require.ErrorContains(t, err, "decrypt proxy credential")
}

func TestProxyRepositoryPreservesPlaintextPasswordLimitBeforeEncryption(t *testing.T) {
	ctx := context.Background()
	client := newCredentialEncryptionTestClient(t)
	repo := newProxyRepositoryWithSQL(client, nil, newDomainMigrationTestEncryptor(t))
	input := &service.Proxy{
		Name: "unicode-proxy", Protocol: "http", Host: "unicode-proxy.example.test",
		Port: 8080, Password: strings.Repeat("😀", maxProxyPasswordRunes), Status: service.StatusActive,
	}

	require.NoError(t, repo.Create(ctx, input))
	raw, err := client.Proxy.Get(ctx, input.ID)
	require.NoError(t, err)
	require.NotNil(t, raw.Password)
	require.Greater(t, len(*raw.Password), 512)

	tooLong := &service.Proxy{
		Name: "too-long-proxy", Protocol: "http", Host: "too-long-proxy.example.test",
		Port: 8081, Password: strings.Repeat("😀", maxProxyPasswordRunes+1), Status: service.StatusActive,
	}
	err = repo.Create(ctx, tooLong)
	require.ErrorContains(t, err, "proxy password must be at most 100 characters")
	require.Zero(t, tooLong.ID)
}
