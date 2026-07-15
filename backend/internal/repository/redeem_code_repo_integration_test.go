//go:build integration

package repository

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
)

type RedeemCodeRepoSuite struct {
	suite.Suite
	ctx    context.Context
	client *dbent.Client
	repo   *redeemCodeRepository
}

func (s *RedeemCodeRepoSuite) SetupTest() {
	s.ctx = context.Background()
	tx := testEntTx(s.T())
	s.client = tx.Client()
	s.repo = NewRedeemCodeRepository(s.client).(*redeemCodeRepository)
	_, err := tx.ExecContext(s.ctx, `
		SET LOCAL session_replication_role = 'replica';
		DELETE FROM redeem_codes;
		SET LOCAL session_replication_role = 'origin';
	`)
	s.Require().NoError(err, "isolate redeem-code repository test transaction")
}

func TestRedeemCodeRepoSuite(t *testing.T) {
	suite.Run(t, new(RedeemCodeRepoSuite))
}

func (s *RedeemCodeRepoSuite) createUser(email string) *dbent.User {
	u, err := s.client.User.Create().
		SetEmail(email).
		SetPasswordHash("test-password-hash").
		Save(s.ctx)
	s.Require().NoError(err, "create user")
	return u
}

func (s *RedeemCodeRepoSuite) createGroup(name string) *dbent.Group {
	g, err := s.client.Group.Create().
		SetName(name).
		Save(s.ctx)
	s.Require().NoError(err, "create group")
	return g
}

// --- Create / CreateBatch / GetByID / GetByCode ---

func (s *RedeemCodeRepoSuite) TestCreate() {
	code := &service.RedeemCode{
		Code:   "TEST-CREATE",
		Type:   service.RedeemTypeBalance,
		Value:  100,
		Status: service.StatusUnused,
	}

	err := s.repo.Create(s.ctx, code)
	s.Require().NoError(err, "Create")
	s.Require().NotZero(code.ID, "expected ID to be set")

	got, err := s.repo.GetByID(s.ctx, code.ID)
	s.Require().NoError(err, "GetByID")
	s.Require().Equal("TEST-CREATE", got.Code)
}

func (s *RedeemCodeRepoSuite) TestCreateBatch() {
	codes := []service.RedeemCode{
		{Code: "BATCH-1", Type: service.RedeemTypeBalance, Value: 10, Status: service.StatusUnused},
		{Code: "BATCH-2", Type: service.RedeemTypeBalance, Value: 20, Status: service.StatusUnused},
	}

	err := s.repo.CreateBatch(s.ctx, codes)
	s.Require().NoError(err, "CreateBatch")

	got1, err := s.repo.GetByCode(s.ctx, "BATCH-1")
	s.Require().NoError(err)
	s.Require().Equal(float64(10), got1.Value)

	got2, err := s.repo.GetByCode(s.ctx, "BATCH-2")
	s.Require().NoError(err)
	s.Require().Equal(float64(20), got2.Value)
}

func (s *RedeemCodeRepoSuite) TestGetByID_NotFound() {
	_, err := s.repo.GetByID(s.ctx, 999999)
	s.Require().Error(err, "expected error for non-existent ID")
	s.Require().ErrorIs(err, service.ErrRedeemCodeNotFound)
}

func (s *RedeemCodeRepoSuite) TestGetByCode() {
	_, err := s.client.RedeemCode.Create().
		SetCode("GET-BY-CODE").
		SetType(service.RedeemTypeBalance).
		SetStatus(service.StatusUnused).
		SetValue(0).
		SetNotes("").
		SetValidityDays(30).
		Save(s.ctx)
	s.Require().NoError(err, "seed redeem code")

	got, err := s.repo.GetByCode(s.ctx, "GET-BY-CODE")
	s.Require().NoError(err, "GetByCode")
	s.Require().Equal("GET-BY-CODE", got.Code)
}

func (s *RedeemCodeRepoSuite) TestGetByCode_NotFound() {
	_, err := s.repo.GetByCode(s.ctx, "NON-EXISTENT")
	s.Require().Error(err, "expected error for non-existent code")
	s.Require().ErrorIs(err, service.ErrRedeemCodeNotFound)
}

// --- Delete ---

func (s *RedeemCodeRepoSuite) TestDeleteIfUnused() {
	created, err := s.client.RedeemCode.Create().
		SetCode("TO-DELETE").
		SetType(service.RedeemTypeBalance).
		SetStatus(service.StatusUnused).
		SetValue(0).
		SetNotes("").
		SetValidityDays(30).
		Save(s.ctx)
	s.Require().NoError(err)

	deleted, err := s.repo.DeleteIfUnused(s.ctx, created.ID)
	s.Require().NoError(err, "DeleteIfUnused")
	s.Require().True(deleted)

	_, err = s.repo.GetByID(s.ctx, created.ID)
	s.Require().Error(err, "expected error after delete")
	s.Require().ErrorIs(err, service.ErrRedeemCodeNotFound)
}

func (s *RedeemCodeRepoSuite) TestDeleteIfUnusedProtectsUsedCode() {
	user := s.createUser(uniqueTestValue(s.T(), "delete-used") + "@example.com")
	usedAt := time.Now().UTC().Truncate(time.Second)
	created, err := s.client.RedeemCode.Create().
		SetCode("DO-NOT-DELETE-USED").
		SetType(service.RedeemTypeBalance).
		SetStatus(service.StatusUsed).
		SetValue(10).
		SetUsedBy(user.ID).
		SetUsedAt(usedAt).
		Save(s.ctx)
	s.Require().NoError(err)
	ledgerKey := fmt.Sprintf("redeem-delete-guard-%d", created.ID)
	_, err = s.client.ExecContext(s.ctx, `
		INSERT INTO ledger_transactions (
			transaction_type, idempotency_key, source_type, source_id,
			user_id, redeem_code_id, amount_usd, description
		) VALUES ('redeem', $1, 'redeem_code', $2, $3, $2, 10, 'delete guard')
	`, ledgerKey, created.ID, user.ID)
	s.Require().NoError(err)
	_, err = s.client.ExecContext(s.ctx, `
		INSERT INTO hfc_abuse_risk_events (source, user_id, redeem_code_id, summary)
		VALUES ('redeem-delete-guard', $1, $2, 'delete guard')
	`, user.ID, created.ID)
	s.Require().NoError(err)

	deleted, err := s.repo.DeleteIfUnused(s.ctx, created.ID)
	s.Require().NoError(err)
	s.Require().False(deleted)

	got, err := s.repo.GetByID(s.ctx, created.ID)
	s.Require().NoError(err)
	s.Require().Equal(service.StatusUsed, got.Status)
	s.Require().NotNil(got.UsedBy)
	s.Require().Equal(user.ID, *got.UsedBy)
	s.Require().NotNil(got.UsedAt)
	s.Require().WithinDuration(usedAt, *got.UsedAt, time.Second)
	ledgerRefs, abuseRefs := queryRedeemAuditReferenceCounts(s.T(), s.ctx, s.client, ledgerKey, created.ID)
	s.Require().Equal(1, ledgerRefs)
	s.Require().Equal(1, abuseRefs)
}

func (s *RedeemCodeRepoSuite) TestDeleteIfUnusedPreservesExpiredCode() {
	created, err := s.client.RedeemCode.Create().
		SetCode("PRESERVE-EXPIRED").
		SetType(service.RedeemTypeBalance).
		SetStatus(service.StatusExpired).
		SetValue(10).
		Save(s.ctx)
	s.Require().NoError(err)

	deleted, err := s.repo.DeleteIfUnused(s.ctx, created.ID)
	s.Require().NoError(err)
	s.Require().False(deleted)

	got, err := s.repo.GetByID(s.ctx, created.ID)
	s.Require().NoError(err)
	s.Require().Equal(service.StatusExpired, got.Status)
}

func (s *RedeemCodeRepoSuite) TestExpireIfUnusedProtectsUsedCode() {
	user := s.createUser(uniqueTestValue(s.T(), "expire-used") + "@example.com")
	usedAt := time.Now().UTC().Truncate(time.Second)
	created, err := s.client.RedeemCode.Create().
		SetCode("DO-NOT-EXPIRE-USED").
		SetType(service.RedeemTypeBalance).
		SetStatus(service.StatusUsed).
		SetValue(10).
		SetUsedBy(user.ID).
		SetUsedAt(usedAt).
		Save(s.ctx)
	s.Require().NoError(err)

	expired, err := s.repo.ExpireIfUnused(s.ctx, created.ID)
	s.Require().NoError(err)
	s.Require().False(expired)

	got, err := s.repo.GetByID(s.ctx, created.ID)
	s.Require().NoError(err)
	s.Require().Equal(service.StatusUsed, got.Status)
	s.Require().NotNil(got.UsedBy)
	s.Require().Equal(user.ID, *got.UsedBy)
	s.Require().NotNil(got.UsedAt)
	s.Require().WithinDuration(usedAt, *got.UsedAt, time.Second)
}

func TestRedeemCodeExpireAndUseRaceHasSingleWinner(t *testing.T) {
	ctx, repo, userID, codeID := newConcurrentRedeemFixture(t, "expire-use")
	start := make(chan struct{})
	var useErr, expireErr error
	var expired bool
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		useErr = repo.Use(ctx, codeID, userID)
	}()
	go func() {
		defer wg.Done()
		<-start
		expired, expireErr = repo.ExpireIfUnused(ctx, codeID)
	}()
	close(start)
	wg.Wait()

	require.NoError(t, expireErr)
	require.NotEqual(t, expired, useErr == nil, "exactly one unused-state CAS must win")
	got, err := repo.GetByID(ctx, codeID)
	require.NoError(t, err)
	if expired {
		require.Equal(t, service.StatusExpired, got.Status)
		require.Nil(t, got.UsedBy)
		require.Nil(t, got.UsedAt)
		return
	}
	require.Equal(t, service.StatusUsed, got.Status)
	require.NotNil(t, got.UsedBy)
	require.Equal(t, userID, *got.UsedBy)
	require.NotNil(t, got.UsedAt)
}

func TestRedeemCodeDeleteAndUseRaceCannotEraseUsedWinner(t *testing.T) {
	ctx, repo, userID, codeID := newConcurrentRedeemFixture(t, "delete-use")
	start := make(chan struct{})
	var useErr, deleteErr error
	var deleted bool
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		useErr = repo.Use(ctx, codeID, userID)
	}()
	go func() {
		defer wg.Done()
		<-start
		deleted, deleteErr = repo.DeleteIfUnused(ctx, codeID)
	}()
	close(start)
	wg.Wait()

	require.NoError(t, deleteErr)
	require.NotEqual(t, deleted, useErr == nil, "exactly one unused-state CAS must win")
	got, err := repo.GetByID(ctx, codeID)
	if deleted {
		require.ErrorIs(t, err, service.ErrRedeemCodeNotFound)
		return
	}
	require.NoError(t, err)
	require.Equal(t, service.StatusUsed, got.Status)
	require.NotNil(t, got.UsedBy)
	require.Equal(t, userID, *got.UsedBy)
	require.NotNil(t, got.UsedAt)
}

func newConcurrentRedeemFixture(t *testing.T, prefix string) (context.Context, *redeemCodeRepository, int64, int64) {
	t.Helper()
	ctx := context.Background()
	client := testEntClient(t)
	repo := NewRedeemCodeRepository(client).(*redeemCodeRepository)
	suffix := time.Now().UnixNano()
	user, err := client.User.Create().
		SetEmail(fmt.Sprintf("redeem-%s-%d@example.test", prefix, suffix)).
		SetPasswordHash("test-password-hash").
		Save(ctx)
	require.NoError(t, err)
	code, err := client.RedeemCode.Create().
		SetCode(fmt.Sprintf("REDEEM-%s-%d", prefix, suffix)).
		SetType(service.RedeemTypeBalance).
		SetStatus(service.StatusUnused).
		SetValue(1).
		Save(ctx)
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = client.RedeemCode.DeleteOneID(code.ID).Exec(ctx)
		_ = client.User.DeleteOneID(user.ID).Exec(ctx)
	})
	return ctx, repo, user.ID, code.ID
}

func queryRedeemAuditReferenceCounts(t *testing.T, ctx context.Context, client *dbent.Client, ledgerKey string, codeID int64) (int, int) {
	t.Helper()
	rows, err := client.QueryContext(ctx, `
		SELECT
			(SELECT COUNT(*) FROM ledger_transactions WHERE idempotency_key = $1 AND redeem_code_id = $2),
			(SELECT COUNT(*) FROM hfc_abuse_risk_events WHERE source = 'redeem-delete-guard' AND redeem_code_id = $2)
	`, ledgerKey, codeID)
	require.NoError(t, err)
	defer func() { require.NoError(t, rows.Close()) }()
	require.True(t, rows.Next())
	var ledgerRefs, abuseRefs int
	require.NoError(t, rows.Scan(&ledgerRefs, &abuseRefs))
	require.NoError(t, rows.Err())
	return ledgerRefs, abuseRefs
}

// --- List / ListWithFilters ---

func (s *RedeemCodeRepoSuite) TestList() {
	_, basePage, err := s.repo.List(s.ctx, pagination.PaginationParams{Page: 1, PageSize: 100})
	s.Require().NoError(err, "List base")
	s.Require().NoError(s.repo.Create(s.ctx, &service.RedeemCode{Code: "LIST-1", Type: service.RedeemTypeBalance, Value: 0, Status: service.StatusUnused}))
	s.Require().NoError(s.repo.Create(s.ctx, &service.RedeemCode{Code: "LIST-2", Type: service.RedeemTypeBalance, Value: 0, Status: service.StatusUnused}))

	codes, page, err := s.repo.List(s.ctx, pagination.PaginationParams{Page: 1, PageSize: 100})
	s.Require().NoError(err, "List")
	s.Require().Len(codes, int(basePage.Total)+2)
	s.Require().Equal(basePage.Total+2, page.Total)
}

func (s *RedeemCodeRepoSuite) TestListWithFilters_Type() {
	_, basePage, err := s.repo.ListWithFilters(s.ctx, pagination.PaginationParams{Page: 1, PageSize: 100}, service.RedeemTypeSubscription, "", "")
	s.Require().NoError(err)
	s.Require().NoError(s.repo.Create(s.ctx, &service.RedeemCode{Code: "TYPE-BAL", Type: service.RedeemTypeBalance, Value: 0, Status: service.StatusUnused}))
	s.Require().NoError(s.repo.Create(s.ctx, &service.RedeemCode{Code: "TYPE-SUB", Type: service.RedeemTypeSubscription, Value: 0, Status: service.StatusUnused}))

	codes, page, err := s.repo.ListWithFilters(s.ctx, pagination.PaginationParams{Page: 1, PageSize: 100}, service.RedeemTypeSubscription, "", "")
	s.Require().NoError(err)
	s.Require().Equal(basePage.Total+1, page.Total)
	var found bool
	for _, code := range codes {
		if code.Code == "TYPE-SUB" {
			found = true
			s.Require().Equal(service.RedeemTypeSubscription, code.Type)
		}
	}
	s.Require().True(found)
}

func (s *RedeemCodeRepoSuite) TestListWithFilters_Status() {
	_, basePage, err := s.repo.ListWithFilters(s.ctx, pagination.PaginationParams{Page: 1, PageSize: 100}, "", service.StatusUsed, "")
	s.Require().NoError(err)
	s.Require().NoError(s.repo.Create(s.ctx, &service.RedeemCode{Code: "STAT-UNUSED", Type: service.RedeemTypeBalance, Value: 0, Status: service.StatusUnused}))
	s.Require().NoError(s.repo.Create(s.ctx, &service.RedeemCode{Code: "STAT-USED", Type: service.RedeemTypeBalance, Value: 0, Status: service.StatusUsed}))

	codes, page, err := s.repo.ListWithFilters(s.ctx, pagination.PaginationParams{Page: 1, PageSize: 100}, "", service.StatusUsed, "")
	s.Require().NoError(err)
	s.Require().Equal(basePage.Total+1, page.Total)
	var found bool
	for _, code := range codes {
		if code.Code == "STAT-USED" {
			found = true
			s.Require().Equal(service.StatusUsed, code.Status)
		}
	}
	s.Require().True(found)
}

func (s *RedeemCodeRepoSuite) TestListWithFilters_Search() {
	s.Require().NoError(s.repo.Create(s.ctx, &service.RedeemCode{Code: "ALPHA-CODE", Type: service.RedeemTypeBalance, Value: 0, Status: service.StatusUnused}))
	s.Require().NoError(s.repo.Create(s.ctx, &service.RedeemCode{Code: "BETA-CODE", Type: service.RedeemTypeBalance, Value: 0, Status: service.StatusUnused}))

	codes, _, err := s.repo.ListWithFilters(s.ctx, pagination.PaginationParams{Page: 1, PageSize: 10}, "", "", "alpha")
	s.Require().NoError(err)
	s.Require().Len(codes, 1)
	s.Require().Contains(codes[0].Code, "ALPHA")
}

func (s *RedeemCodeRepoSuite) TestListWithFilters_GroupPreload() {
	group := s.createGroup(uniqueTestValue(s.T(), "g-preload"))
	_, err := s.client.RedeemCode.Create().
		SetCode("WITH-GROUP").
		SetType(service.RedeemTypeSubscription).
		SetStatus(service.StatusUnused).
		SetValue(0).
		SetNotes("").
		SetValidityDays(30).
		SetGroupID(group.ID).
		Save(s.ctx)
	s.Require().NoError(err)

	codes, _, err := s.repo.ListWithFilters(s.ctx, pagination.PaginationParams{Page: 1, PageSize: 10}, "", "", "WITH-GROUP")
	s.Require().NoError(err)
	s.Require().Len(codes, 1)
	s.Require().NotNil(codes[0].Group, "expected Group preload")
	s.Require().Equal(group.ID, codes[0].Group.ID)
}

// --- Update ---

func (s *RedeemCodeRepoSuite) TestUpdate() {
	code := &service.RedeemCode{
		Code:   "UPDATE-ME",
		Type:   service.RedeemTypeBalance,
		Value:  10,
		Status: service.StatusUnused,
	}
	s.Require().NoError(s.repo.Create(s.ctx, code))

	code.Value = 50
	err := s.repo.Update(s.ctx, code)
	s.Require().NoError(err, "Update")

	got, err := s.repo.GetByID(s.ctx, code.ID)
	s.Require().NoError(err)
	s.Require().Equal(float64(50), got.Value)
}

// --- Use ---

func (s *RedeemCodeRepoSuite) TestUse() {
	user := s.createUser(uniqueTestValue(s.T(), "use") + "@example.com")
	code := &service.RedeemCode{Code: "USE-ME", Type: service.RedeemTypeBalance, Value: 0, Status: service.StatusUnused}
	s.Require().NoError(s.repo.Create(s.ctx, code))

	err := s.repo.Use(s.ctx, code.ID, user.ID)
	s.Require().NoError(err, "Use")

	got, err := s.repo.GetByID(s.ctx, code.ID)
	s.Require().NoError(err)
	s.Require().Equal(service.StatusUsed, got.Status)
	s.Require().NotNil(got.UsedBy)
	s.Require().Equal(user.ID, *got.UsedBy)
	s.Require().NotNil(got.UsedAt)
}

func (s *RedeemCodeRepoSuite) TestUse_Idempotency() {
	user := s.createUser(uniqueTestValue(s.T(), "idem") + "@example.com")
	code := &service.RedeemCode{Code: "IDEM-CODE", Type: service.RedeemTypeBalance, Value: 0, Status: service.StatusUnused}
	s.Require().NoError(s.repo.Create(s.ctx, code))

	err := s.repo.Use(s.ctx, code.ID, user.ID)
	s.Require().NoError(err, "Use first time")

	// Second use should fail
	err = s.repo.Use(s.ctx, code.ID, user.ID)
	s.Require().Error(err, "Use expected error on second call")
	s.Require().ErrorIs(err, service.ErrRedeemCodeUsed)
}

func (s *RedeemCodeRepoSuite) TestUse_AlreadyUsed() {
	user := s.createUser(uniqueTestValue(s.T(), "already") + "@example.com")
	code := &service.RedeemCode{Code: "ALREADY-USED", Type: service.RedeemTypeBalance, Value: 0, Status: service.StatusUsed}
	s.Require().NoError(s.repo.Create(s.ctx, code))

	err := s.repo.Use(s.ctx, code.ID, user.ID)
	s.Require().Error(err, "expected error for already used code")
	s.Require().ErrorIs(err, service.ErrRedeemCodeUsed)
}

// --- ListByUser ---

func (s *RedeemCodeRepoSuite) TestListByUser() {
	user := s.createUser(uniqueTestValue(s.T(), "listby") + "@example.com")
	base := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)

	usedAt1 := base
	_, err := s.client.RedeemCode.Create().
		SetCode("USER-1").
		SetType(service.RedeemTypeBalance).
		SetStatus(service.StatusUsed).
		SetValue(0).
		SetNotes("").
		SetValidityDays(30).
		SetUsedBy(user.ID).
		SetUsedAt(usedAt1).
		Save(s.ctx)
	s.Require().NoError(err)

	usedAt2 := base.Add(1 * time.Hour)
	_, err = s.client.RedeemCode.Create().
		SetCode("USER-2").
		SetType(service.RedeemTypeBalance).
		SetStatus(service.StatusUsed).
		SetValue(0).
		SetNotes("").
		SetValidityDays(30).
		SetUsedBy(user.ID).
		SetUsedAt(usedAt2).
		Save(s.ctx)
	s.Require().NoError(err)

	codes, err := s.repo.ListByUser(s.ctx, user.ID, 10)
	s.Require().NoError(err, "ListByUser")
	s.Require().Len(codes, 2)
	// Ordered by used_at DESC, so USER-2 first
	s.Require().Equal("USER-2", codes[0].Code)
	s.Require().Equal("USER-1", codes[1].Code)
}

func (s *RedeemCodeRepoSuite) TestListByUser_WithGroupPreload() {
	user := s.createUser(uniqueTestValue(s.T(), "grp") + "@example.com")
	group := s.createGroup(uniqueTestValue(s.T(), "g-listby"))

	_, err := s.client.RedeemCode.Create().
		SetCode("WITH-GRP").
		SetType(service.RedeemTypeSubscription).
		SetStatus(service.StatusUsed).
		SetValue(0).
		SetNotes("").
		SetValidityDays(30).
		SetUsedBy(user.ID).
		SetUsedAt(time.Now()).
		SetGroupID(group.ID).
		Save(s.ctx)
	s.Require().NoError(err)

	codes, err := s.repo.ListByUser(s.ctx, user.ID, 10)
	s.Require().NoError(err)
	s.Require().Len(codes, 1)
	s.Require().NotNil(codes[0].Group)
	s.Require().Equal(group.ID, codes[0].Group.ID)
}

func (s *RedeemCodeRepoSuite) TestListByUser_DefaultLimit() {
	user := s.createUser(uniqueTestValue(s.T(), "deflimit") + "@example.com")
	_, err := s.client.RedeemCode.Create().
		SetCode("DEF-LIM").
		SetType(service.RedeemTypeBalance).
		SetStatus(service.StatusUsed).
		SetValue(0).
		SetNotes("").
		SetValidityDays(30).
		SetUsedBy(user.ID).
		SetUsedAt(time.Now()).
		Save(s.ctx)
	s.Require().NoError(err)

	// limit <= 0 should default to 10
	codes, err := s.repo.ListByUser(s.ctx, user.ID, 0)
	s.Require().NoError(err)
	s.Require().Len(codes, 1)
}

// --- Combined original test ---

func (s *RedeemCodeRepoSuite) TestCreateBatch_Filters_Use_Idempotency_ListByUser() {
	user := s.createUser(uniqueTestValue(s.T(), "rc") + "@example.com")
	group := s.createGroup(uniqueTestValue(s.T(), "g-rc"))
	groupID := group.ID

	codes := []service.RedeemCode{
		{Code: "CODEA", Type: service.RedeemTypeBalance, Value: 1, Status: service.StatusUnused, Notes: ""},
		{Code: "CODEB", Type: service.RedeemTypeSubscription, Value: 0, Status: service.StatusUnused, Notes: "", GroupID: &groupID, ValidityDays: 7},
	}
	s.Require().NoError(s.repo.CreateBatch(s.ctx, codes), "CreateBatch")

	list, page, err := s.repo.ListWithFilters(s.ctx, pagination.PaginationParams{Page: 1, PageSize: 10}, service.RedeemTypeSubscription, service.StatusUnused, "code")
	s.Require().NoError(err, "ListWithFilters")
	s.Require().Equal(int64(1), page.Total)
	s.Require().Len(list, 1)
	s.Require().NotNil(list[0].Group, "expected Group preload")
	s.Require().Equal(group.ID, list[0].Group.ID)

	codeB, err := s.repo.GetByCode(s.ctx, "CODEB")
	s.Require().NoError(err, "GetByCode")
	s.Require().NoError(s.repo.Use(s.ctx, codeB.ID, user.ID), "Use")
	err = s.repo.Use(s.ctx, codeB.ID, user.ID)
	s.Require().Error(err, "Use expected error on second call")
	s.Require().ErrorIs(err, service.ErrRedeemCodeUsed)

	codeA, err := s.repo.GetByCode(s.ctx, "CODEA")
	s.Require().NoError(err, "GetByCode")

	// Use fixed time instead of time.Sleep for deterministic ordering.
	_, err = s.client.RedeemCode.UpdateOneID(codeB.ID).
		SetUsedAt(time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)).
		Save(s.ctx)
	s.Require().NoError(err)
	s.Require().NoError(s.repo.Use(s.ctx, codeA.ID, user.ID), "Use codeA")
	_, err = s.client.RedeemCode.UpdateOneID(codeA.ID).
		SetUsedAt(time.Date(2025, 1, 1, 13, 0, 0, 0, time.UTC)).
		Save(s.ctx)
	s.Require().NoError(err)

	used, err := s.repo.ListByUser(s.ctx, user.ID, 10)
	s.Require().NoError(err, "ListByUser")
	s.Require().Len(used, 2, "expected 2 used codes")
	s.Require().Equal("CODEA", used[0].Code, "expected newest used code first")
}
