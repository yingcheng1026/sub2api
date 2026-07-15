package migrations

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMigration190ConstrainsProxyPasswordsToDomainBoundCiphertext(t *testing.T) {
	content, err := FS.ReadFile("190_encrypt_proxy_credentials_at_rest.sql")
	require.NoError(t, err)

	sql := string(content)
	require.Contains(t, sql, "ALTER COLUMN password TYPE text")
	require.Contains(t, sql, "chk_proxies_password_encrypted_storage")
	require.Contains(t, sql, "password LIKE 'sd:v3:%'")
	require.Contains(t, sql, "NOT VALID")
	require.NotContains(t, sql, "UPDATE proxies")
}
