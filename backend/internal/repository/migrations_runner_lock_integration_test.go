//go:build integration

package repository

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/stretchr/testify/require"
)

// This regression test disables idle pooling so a DB-level advisory-lock query
// immediately gives its session back. A correct runner pins one *sql.Conn from
// lock acquisition through both transactional and non-transactional migration
// execution; otherwise both runners can observe the migration as missing and
// race the same DDL.
func TestApplyMigrationsFS_AdvisoryLockStaysBoundToMigrationSession(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	suffix := time.Now().UnixNano()
	filename := fmt.Sprintf("900_lock_binding_%d.sql", suffix)
	tableName := fmt.Sprintf("migration_lock_binding_%d", suffix)
	fsys := fstest.MapFS{
		filename: &fstest.MapFile{Data: []byte(fmt.Sprintf(`
SELECT pg_sleep(0.75);
CREATE TABLE %s (id BIGINT PRIMARY KEY);
`, tableName))},
	}

	_, err := integrationDB.ExecContext(ctx, fmt.Sprintf("DROP TABLE IF EXISTS %s", tableName))
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, "DELETE FROM schema_migrations WHERE filename = $1", filename)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(), fmt.Sprintf("DROP TABLE IF EXISTS %s", tableName))
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM schema_migrations WHERE filename = $1", filename)
	})

	integrationDB.SetMaxIdleConns(0)
	t.Cleanup(func() { integrationDB.SetMaxIdleConns(2) })

	start := make(chan struct{})
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			errs <- applyMigrationsFS(ctx, integrationDB, fsys)
		}()
	}
	close(start)
	wg.Wait()
	close(errs)

	for runErr := range errs {
		require.NoError(t, runErr)
	}

	var applied int
	err = integrationDB.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM schema_migrations WHERE filename = $1", filename,
	).Scan(&applied)
	require.NoError(t, err)
	require.Equal(t, 1, applied)
}
