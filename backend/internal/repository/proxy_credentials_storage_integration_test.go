//go:build integration

package repository

import (
	"context"
	"database/sql"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestProxyCredentialStorageConstraintRejectsPlaintext(t *testing.T) {
	tx := testTx(t)

	var (
		dataType         string
		characterMaxLen sql.NullInt64
	)
	err := tx.QueryRowContext(context.Background(), `
SELECT data_type, character_maximum_length
FROM information_schema.columns
WHERE table_schema = 'public'
  AND table_name = 'proxies'
  AND column_name = 'password'
`).Scan(&dataType, &characterMaxLen)
	require.NoError(t, err)
	require.Equal(t, "text", dataType)
	require.False(t, characterMaxLen.Valid)

	var definition string
	err = tx.QueryRowContext(context.Background(), `
SELECT pg_get_constraintdef(c.oid)
FROM pg_constraint c
JOIN pg_class tbl ON tbl.oid = c.conrelid
JOIN pg_namespace ns ON ns.oid = tbl.relnamespace
WHERE ns.nspname = 'public'
  AND tbl.relname = 'proxies'
  AND c.conname = 'chk_proxies_password_encrypted_storage'
`).Scan(&definition)
	require.NoError(t, err)
	require.Contains(t, definition, "password IS NULL")
	require.Contains(t, definition, "sd:v3:%")

	_, err = tx.ExecContext(context.Background(), `
INSERT INTO proxies (name, protocol, host, port, password, status)
VALUES ('plaintext-constraint-probe', 'http', '127.0.0.1', 8080, 'plaintext-secret', 'active')
`)
	require.Error(t, err)
}
