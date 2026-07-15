package repository

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/fstest"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
)

var migrationTrackingDriverSeq atomic.Uint64

func TestApplyMigrationsFS_PinsSingleDatabaseSession(t *testing.T) {
	tracker := &migrationTrackingDriver{}
	driverName := fmt.Sprintf("migration_session_tracker_%d", migrationTrackingDriverSeq.Add(1))
	sql.Register(driverName, tracker)

	db, err := sql.Open(driverName, "")
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	// A DB-level implementation would close/reopen a physical session between
	// calls under this setting, exposing the original advisory-lock bug.
	db.SetMaxIdleConns(0)

	require.NoError(t, applyMigrationsFS(context.Background(), db, fstest.MapFS{}))

	connectionIDs := tracker.connectionIDs()
	require.GreaterOrEqual(t, len(connectionIDs), 6)
	for _, connectionID := range connectionIDs[1:] {
		require.Equal(t, connectionIDs[0], connectionID,
			"lock, migration bootstrap, and unlock must use one physical database session")
	}
}

func TestApplyMigrationsFS_ReturnsUnlockFailure(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	prepareMigrationsBootstrapExpectations(mock)
	mock.ExpectQuery("SELECT pg_advisory_unlock\\(\\$1\\)").
		WithArgs(migrationsAdvisoryLockID).
		WillReturnError(errors.New("unlock failed"))

	err = applyMigrationsFS(context.Background(), db, fstest.MapFS{})
	require.ErrorContains(t, err, "release migrations lock")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPrepareWalletIntegrityIndexesMigration_RejectsMismatchedValidIndex(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	mock.ExpectQuery("SELECT i.indisvalid").
		WithArgs("idx_wallet_ledger_one_activation").
		WillReturnRows(sqlmock.NewRows([]string{"indisvalid", "indisunique", "table", "key_count", "key", "predicate"}).
			AddRow(true, false, "subscription_wallet_ledger", 1, "subscription_id", "(reason = 'activation'::text)"))

	err = prepareWalletIntegrityIndexesMigration(context.Background(), db)
	require.ErrorContains(t, err, "does not match required wallet integrity definition")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPrepareAPIKeyHashIndexMigration_RejectsMismatchedValidIndex(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	mock.ExpectQuery("SELECT i.indisvalid").
		WithArgs("apikey_key_hash").
		WillReturnRows(sqlmock.NewRows([]string{"indisvalid", "indisunique", "table", "key_count", "key", "predicate"}).
			AddRow(true, false, "api_keys", 1, "key_hash", "((deleted_at IS NULL) AND (key_hash IS NOT NULL))"))

	err = prepareNonTransactionalMigration(context.Background(), db, apiKeyHashIndexMigration)
	require.ErrorContains(t, err, "does not match required definition")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPreparePaymentOrderUniqueIndex_RejectsMismatchedValidIndex(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	mock.ExpectQuery("SELECT out_trade_no, COUNT\\(\\*\\) AS duplicate_count FROM payment_orders").
		WillReturnRows(sqlmock.NewRows([]string{"out_trade_no", "duplicate_count"}))
	mock.ExpectQuery("SELECT i.indisvalid").
		WithArgs("paymentorder_out_trade_no_unique").
		WillReturnRows(sqlmock.NewRows([]string{"indisvalid", "indisunique", "table", "key_count", "key", "predicate"}).
			AddRow(true, false, "payment_orders", 1, "out_trade_no", "(out_trade_no <> ''::text)"))

	err = preparePaymentOrdersOutTradeNoUniqueMigration(context.Background(), db)
	require.ErrorContains(t, err, "does not match required definition")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPgAdvisoryUnlock_RejectsSessionThatDoesNotOwnLock(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	mock.ExpectQuery("SELECT pg_advisory_unlock\\(\\$1\\)").
		WithArgs(migrationsAdvisoryLockID).
		WillReturnRows(sqlmock.NewRows([]string{"pg_advisory_unlock"}).AddRow(false))

	err = pgAdvisoryUnlock(context.Background(), db)
	require.ErrorContains(t, err, "does not own the lock")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestNormalizeIndexPredicate_IgnoresPostgresFormattingAndCasts(t *testing.T) {
	expected := "wallet_balance_usd IS NOT NULL AND status = 'active' AND deleted_at IS NULL AND expires_at >= '2099-12-30 23:59:59+00'"

	for _, actual := range []string{
		"((wallet_balance_usd IS NOT NULL) AND ((status)::text = 'active'::text) AND (deleted_at IS NULL) AND (expires_at >= '2099-12-30 23:59:59+00:00'::timestamp with time zone))",
		"((wallet_balance_usd IS NOT NULL) AND ((status)::text = 'active'::text) AND (deleted_at IS NULL) AND (expires_at >= '2099-12-31 07:59:59+08'::timestamp with time zone))",
	} {
		require.Equal(t, normalizeIndexPredicate(expected), normalizeIndexPredicate(actual))
	}
}

func TestNormalizeIndexPredicate_PreservesLiteralSemantics(t *testing.T) {
	expectedReason := normalizeIndexPredicate("reason = 'activation'")
	require.NotEqual(t, expectedReason, normalizeIndexPredicate("reason = 'ACTIVATION'"))
	require.NotEqual(t, expectedReason, normalizeIndexPredicate("reason = 'acti vation'"))

	expectedCutoff := normalizeIndexPredicate("expires_at >= '2099-12-30 23:59:59+00'::timestamp with time zone")
	require.NotEqual(t, expectedCutoff, normalizeIndexPredicate("expires_at >= '2099-12-30 23:59:59.5+00'::timestamp with time zone"))
}

func TestPrepareConcurrentIndexArtifacts_RejectsUnsupportedIndexNameSyntax(t *testing.T) {
	db, _, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	err = prepareConcurrentIndexArtifacts(context.Background(), db,
		`CREATE INDEX CONCURRENTLY IF NOT EXISTS "quoted_idx" ON t(id);`)
	require.ErrorContains(t, err, "unsupported CREATE INDEX CONCURRENTLY syntax")
}

type migrationTrackingDriver struct {
	mu      sync.Mutex
	nextID  int
	usedIDs []int
}

func (d *migrationTrackingDriver) Open(string) (driver.Conn, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.nextID++
	return &migrationTrackingConn{driver: d, id: d.nextID}, nil
}

func (d *migrationTrackingDriver) record(connectionID int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.usedIDs = append(d.usedIDs, connectionID)
}

func (d *migrationTrackingDriver) connectionIDs() []int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]int(nil), d.usedIDs...)
}

type migrationTrackingConn struct {
	driver *migrationTrackingDriver
	id     int
}

func (c *migrationTrackingConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("unexpected prepared statement")
}

func (c *migrationTrackingConn) Close() error { return nil }

func (c *migrationTrackingConn) Begin() (driver.Tx, error) {
	return nil, errors.New("unexpected transaction")
}

func (c *migrationTrackingConn) ExecContext(_ context.Context, _ string, _ []driver.NamedValue) (driver.Result, error) {
	c.driver.record(c.id)
	return driver.RowsAffected(1), nil
}

func (c *migrationTrackingConn) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	c.driver.record(c.id)
	normalized := strings.ToLower(query)
	switch {
	case strings.Contains(normalized, "pg_try_advisory_lock"):
		return &migrationTrackingRows{column: "pg_try_advisory_lock", value: true}, nil
	case strings.Contains(normalized, "pg_advisory_unlock"):
		return &migrationTrackingRows{column: "pg_advisory_unlock", value: true}, nil
	case strings.Contains(normalized, "information_schema.tables"):
		return &migrationTrackingRows{column: "exists", value: true}, nil
	case strings.Contains(normalized, "count(*) from atlas_schema_revisions"):
		return &migrationTrackingRows{column: "count", value: int64(1)}, nil
	default:
		return nil, fmt.Errorf("unexpected query: %s", query)
	}
}

type migrationTrackingRows struct {
	column string
	value  driver.Value
	done   bool
}

func (r *migrationTrackingRows) Columns() []string { return []string{r.column} }
func (r *migrationTrackingRows) Close() error      { return nil }

func (r *migrationTrackingRows) Next(dest []driver.Value) error {
	if r.done {
		return io.EOF
	}
	dest[0] = r.value
	r.done = true
	return nil
}
