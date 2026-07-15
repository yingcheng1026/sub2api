//go:build integration

package repository

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestAPIKeyStartupMigrationValidatesEncryptedStorageConstraint(t *testing.T) {
	client := testEntClient(t)
	user := mustCreateUser(t, client, &service.User{Email: "storage-constraint@test.com"})
	repo := NewAPIKeyRepository(client, integrationDB, strictAPIKeyTestProtector{}).(*apiKeyRepository)
	_, err := repo.MigratePlaintextAPIKeysToEncrypted(context.Background())
	require.NoError(t, err)

	var validated bool
	require.NoError(t, integrationDB.QueryRowContext(context.Background(), `
		SELECT convalidated FROM pg_constraint
		WHERE conname = 'chk_api_keys_encrypted_storage'
		  AND conrelid = 'api_keys'::regclass
	`).Scan(&validated))
	require.True(t, validated)

	_, err = integrationDB.ExecContext(context.Background(), `
		INSERT INTO api_keys (user_id, key, key_hash, key_prefix, name, purpose, status)
		VALUES ($1, 'sk-plaintext-forbidden', $2, 'sk-plain', 'forbidden', 'standard', 'active')
	`, user.ID, service.HashAPIKey("sk-plaintext-forbidden"))
	require.Error(t, err, "database must reject plaintext API-key storage after cutover")
}
