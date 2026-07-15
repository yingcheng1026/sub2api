//go:build unit

package repository

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestSafeDateFormat(t *testing.T) {
	tests := []struct {
		name        string
		granularity string
		expected    string
	}{
		// 合法值
		{"hour", "hour", "YYYY-MM-DD HH24:00"},
		{"day", "day", "YYYY-MM-DD"},
		{"week", "week", "IYYY-IW"},
		{"month", "month", "YYYY-MM"},

		// 非法值回退到默认
		{"空字符串", "", "YYYY-MM-DD"},
		{"未知粒度 year", "year", "YYYY-MM-DD"},
		{"未知粒度 minute", "minute", "YYYY-MM-DD"},

		// 恶意字符串
		{"SQL 注入尝试", "'; DROP TABLE users; --", "YYYY-MM-DD"},
		{"带引号", "day'", "YYYY-MM-DD"},
		{"带括号", "day)", "YYYY-MM-DD"},
		{"Unicode", "日", "YYYY-MM-DD"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := safeDateFormat(tc.granularity)
			require.Equal(t, tc.expected, got, "safeDateFormat(%q)", tc.granularity)
		})
	}
}

func TestBuildUsageLogBatchInsertQuery_UsesConflictDoNothing(t *testing.T) {
	log := &service.UsageLog{
		UserID:       1,
		APIKeyID:     2,
		AccountID:    3,
		RequestID:    "req-batch-no-update",
		Model:        "gpt-5",
		InputTokens:  10,
		OutputTokens: 5,
		TotalCost:    1.2,
		ActualCost:   1.2,
		CreatedAt:    time.Now().UTC(),
	}
	prepared := prepareUsageLogInsert(log)

	query, _ := buildUsageLogBatchInsertQuery([]string{usageLogBatchKey(log.RequestID, log.APIKeyID)}, map[string]usageLogInsertPrepared{
		usageLogBatchKey(log.RequestID, log.APIKeyID): prepared,
	})

	require.Contains(t, query, "ON CONFLICT (request_id, api_key_id) DO NOTHING")
	require.NotContains(t, strings.ToUpper(query), "DO UPDATE")
}

func TestUsageLogRepositoryCreateBestEffort_QueuePressureWaitsForDrain(t *testing.T) {
	db, _, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	repo := newUsageLogRepositoryWithSQL(nil, db)
	repo.bestEffortBatchCh = make(chan usageLogBestEffortRequest, 1)
	repo.bestEffortBatchCh <- usageLogBestEffortRequest{}

	drainStarted := make(chan struct{})
	go func() {
		time.Sleep(50 * time.Millisecond)
		close(drainStarted)
		<-repo.bestEffortBatchCh
		req := <-repo.bestEffortBatchCh
		sendUsageLogBestEffortResult(req.resultCh, nil)
	}()

	startedAt := time.Now()
	err = repo.CreateBestEffort(context.Background(), &service.UsageLog{
		UserID: 1, APIKeyID: 2, AccountID: 3, RequestID: "queue-pressure", Model: "gpt-5.6-sol",
		InputTokens: 1, CreatedAt: time.Now().UTC(),
	})
	require.NoError(t, err)
	<-drainStarted
	require.GreaterOrEqual(t, time.Since(startedAt), 40*time.Millisecond)
}

func TestUsageLogRepositoryBillingModelPersistenceContract(t *testing.T) {
	billingModel := "gpt-5.4"
	log := &service.UsageLog{
		UserID:       1,
		APIKeyID:     2,
		AccountID:    3,
		RequestID:    "req-billing-model-contract",
		Model:        "gpt-5.4",
		BillingModel: &billingModel,
		InputTokens:  10,
		OutputTokens: 5,
		CreatedAt:    time.Now().UTC(),
	}
	prepared := prepareUsageLogInsert(log)

	require.Contains(t, usageLogSelectColumns, "requested_model, upstream_model, billing_model, pricing_source, pricing_revision, pricing_hash, group_id")
	require.Len(t, prepared.args, len(usageLogInsertArgTypes))
	require.Equal(t, "text", usageLogInsertArgTypes[7])
	require.Equal(t, sql.NullString{String: billingModel, Valid: true}, prepared.args[7])

	key := usageLogBatchKey(log.RequestID, log.APIKeyID)
	batchQuery, _ := buildUsageLogBatchInsertQuery([]string{key}, map[string]usageLogInsertPrepared{key: prepared})
	bestEffortQuery, _ := buildUsageLogBestEffortInsertQuery([]usageLogInsertPrepared{prepared})
	for _, query := range []string{batchQuery, bestEffortQuery} {
		require.Contains(t, query, "requested_model,\n\t\t\tupstream_model,\n\t\t\tbilling_model,\n\t\t\tpricing_source,\n\t\t\tpricing_revision,\n\t\t\tpricing_hash,")
		require.Contains(t, query, "billing_model")
	}
}

func TestUsageLogRepositoryPricingEvidencePersistenceContract(t *testing.T) {
	pricingSource := service.PricingSourceBuiltinGPT56
	pricingRevision := service.GPT56PricingRevision
	pricingHash := strings.Repeat("a", 64)
	log := &service.UsageLog{
		UserID: 1, APIKeyID: 2, AccountID: 3,
		RequestID: "req-pricing-evidence", Model: "gpt-5.6-terra",
		PricingSource: &pricingSource, PricingRevision: &pricingRevision, PricingHash: &pricingHash,
		CreatedAt: time.Now().UTC(),
	}
	prepared := prepareUsageLogInsert(log)

	require.Contains(t, usageLogSelectColumns, "billing_model, pricing_source, pricing_revision, pricing_hash, group_id")
	require.Len(t, prepared.args, len(usageLogInsertArgTypes))
	require.Equal(t, sql.NullString{String: pricingSource, Valid: true}, prepared.args[8])
	require.Equal(t, sql.NullString{String: pricingRevision, Valid: true}, prepared.args[9])
	require.Equal(t, sql.NullString{String: pricingHash, Valid: true}, prepared.args[10])

	key := usageLogBatchKey(log.RequestID, log.APIKeyID)
	batchQuery, _ := buildUsageLogBatchInsertQuery([]string{key}, map[string]usageLogInsertPrepared{key: prepared})
	bestEffortQuery, _ := buildUsageLogBestEffortInsertQuery([]usageLogInsertPrepared{prepared})
	for _, query := range []string{batchQuery, bestEffortQuery} {
		require.Contains(t, query, "pricing_source")
		require.Contains(t, query, "pricing_revision")
		require.Contains(t, query, "pricing_hash")
	}
}

func TestUsageLogRepositorySingleInsertPathsIncludeBillingModel(t *testing.T) {
	billingModel := "gpt-5.6-terra"
	log := &service.UsageLog{
		UserID:       1,
		APIKeyID:     2,
		AccountID:    3,
		RequestID:    "req-billing-model-single",
		Model:        billingModel,
		BillingModel: &billingModel,
		CreatedAt:    time.Now().UTC(),
	}
	queryPattern := `(?s)INSERT INTO usage_logs \(.*requested_model,\s*upstream_model,\s*billing_model,.*VALUES`

	t.Run("returning insert", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		require.NoError(t, err)
		t.Cleanup(func() { _ = db.Close() })
		mock.ExpectQuery(queryPattern).
			WillReturnRows(sqlmock.NewRows([]string{"id", "created_at"}).AddRow(int64(9), log.CreatedAt))

		repo := newUsageLogRepositoryWithSQL(nil, db)
		inserted, err := repo.createSingle(context.Background(), db, log)
		require.NoError(t, err)
		require.True(t, inserted)
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("no-result insert", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		require.NoError(t, err)
		t.Cleanup(func() { _ = db.Close() })
		mock.ExpectExec(queryPattern).WillReturnResult(sqlmock.NewResult(0, 1))

		err = execUsageLogInsertNoResult(context.Background(), db, prepareUsageLogInsert(log))
		require.NoError(t, err)
		require.NoError(t, mock.ExpectationsWereMet())
	})
}

func TestScanUsageLogBillingModelPreservesValueAndHistoricalNull(t *testing.T) {
	now := time.Now().UTC()
	values := []any{
		int64(1), int64(10), int64(20), int64(30),
		sql.NullString{Valid: true, String: "req-scan-billing"},
		"gpt-5.4",
		sql.NullString{Valid: true, String: "claude-sonnet-4-6"},
		sql.NullString{Valid: true, String: "gpt-5.4"},
		sql.NullString{Valid: true, String: "gpt-5.4"},
		sql.NullString{}, sql.NullString{}, sql.NullString{},
		sql.NullInt64{}, sql.NullInt64{},
		1, 2, 3, 4, 5, 6,
		0, 0.0,
		0.1, 0.2, 0.3, 0.4, 1.0, 0.9, 1.0,
		sql.NullFloat64{},
		int16(service.BillingTypeBalance), int16(service.RequestTypeSync),
		false, false,
		sql.NullInt64{}, sql.NullInt64{},
		sql.NullString{}, sql.NullString{},
		0, sql.NullString{},
		sql.NullString{}, sql.NullString{}, sql.NullString{}, sql.NullString{},
		false,
		sql.NullInt64{}, sql.NullString{}, sql.NullString{}, sql.NullString{},
		sql.NullFloat64{},
		now,
	}

	log, err := scanUsageLog(usageLogScannerStub{values: values})
	require.NoError(t, err)
	require.NotNil(t, log.BillingModel)
	require.Equal(t, "gpt-5.4", *log.BillingModel)

	values[8] = sql.NullString{}
	historical, err := scanUsageLog(usageLogScannerStub{values: values})
	require.NoError(t, err)
	require.Nil(t, historical.BillingModel)
}
