package service

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/stretchr/testify/require"
)

func TestHFCAbuseRiskService_RecordEventNormalizesReviewState(t *testing.T) {
	repo := &hfcAbuseRiskRepoStub{}
	svc := NewHFCAbuseRiskService(repo, nil, nil, nil)

	event, err := svc.RecordEvent(context.Background(), &HFCAbuseRiskEvent{
		Source:   " signup_risk ",
		Severity: "invalid",
		Status:   "invalid",
		Summary:  " registration allowed ",
		Evidence: []HFCAbuseRiskEvidence{
			hfcRiskEvidence("hold_reason", "原因", "duplicate_device_fingerprint"),
			{},
		},
	})

	require.NoError(t, err)
	require.Equal(t, int64(1), event.ID)
	require.Equal(t, HFCAbuseRiskSeverityHigh, repo.created.Severity)
	require.Equal(t, HFCAbuseRiskStatusOpen, repo.created.Status)
	require.Equal(t, "signup_risk", repo.created.Source)
	require.Len(t, repo.created.Evidence, 1)
}

func TestHFCAbuseRiskService_DisableUserAPIKeysInvalidatesEachKey(t *testing.T) {
	userID := int64(42)
	event := &HFCAbuseRiskEvent{ID: 7, UserID: &userID, Status: HFCAbuseRiskStatusOpen}
	repo := &hfcAbuseRiskRepoStub{event: event}
	apiRepo := &hfcAbuseRiskAPIKeyRepoStub{
		keys: []*APIKey{
			{ID: 1, UserID: userID, Key: "sk-one", Name: "one", Status: StatusAPIKeyActive},
			{ID: 2, UserID: userID, Key: "sk-two", Name: "two", Status: StatusAPIKeyActive},
		},
	}
	invalidator := &hfcAbuseRiskCacheInvalidatorStub{}
	svc := NewHFCAbuseRiskService(repo, apiRepo, nil, invalidator)

	result, err := svc.ApplyAction(context.Background(), HFCAbuseRiskActionInput{
		EventID:     event.ID,
		Action:      HFCAbuseRiskActionDisableUserAPIKeys,
		AdminUserID: 9,
	})

	require.NoError(t, err)
	require.Equal(t, 2, result.DisabledAPIKeys)
	require.Equal(t, HFCAbuseRiskStatusResolved, repo.event.Status)
	require.Equal(t, []string{"sk-one", "sk-two"}, invalidator.keys)
	require.Equal(t, StatusAPIKeyDisabled, apiRepo.keys[0].Status)
	require.Equal(t, StatusAPIKeyDisabled, apiRepo.keys[1].Status)
}

func TestHFCAbuseRiskService_FreezeUserRefusesAdmin(t *testing.T) {
	userID := int64(1)
	event := &HFCAbuseRiskEvent{ID: 8, UserID: &userID, Status: HFCAbuseRiskStatusOpen}
	repo := &hfcAbuseRiskRepoStub{event: event}
	userRepo := &hfcAbuseRiskUserRepoStub{user: &User{ID: userID, Role: RoleAdmin, Status: StatusActive}}
	svc := NewHFCAbuseRiskService(repo, nil, userRepo, nil)

	_, err := svc.ApplyAction(context.Background(), HFCAbuseRiskActionInput{
		EventID: event.ID,
		Action:  HFCAbuseRiskActionFreezeUser,
	})

	require.Error(t, err)
	require.Equal(t, StatusActive, userRepo.user.Status)
}

type hfcAbuseRiskRepoStub struct {
	created *HFCAbuseRiskEvent
	event   *HFCAbuseRiskEvent
}

func (r *hfcAbuseRiskRepoStub) CreateEvent(_ context.Context, event *HFCAbuseRiskEvent) error {
	cp := cloneHFCAbuseRiskEvent(event)
	cp.ID = 1
	cp.CreatedAt = time.Now()
	cp.UpdatedAt = cp.CreatedAt
	r.created = cp
	*event = *cloneHFCAbuseRiskEvent(cp)
	return nil
}

func (r *hfcAbuseRiskRepoStub) ListEvents(_ context.Context, _ HFCAbuseRiskEventFilter) ([]HFCAbuseRiskEvent, *pagination.PaginationResult, error) {
	return []HFCAbuseRiskEvent{}, &pagination.PaginationResult{Page: 1, PageSize: 20, Pages: 1}, nil
}

func (r *hfcAbuseRiskRepoStub) GetEvent(_ context.Context, id int64) (*HFCAbuseRiskEvent, error) {
	if r.event == nil || r.event.ID != id {
		return nil, ErrHFCAbuseRiskEventNotFound
	}
	return cloneHFCAbuseRiskEvent(r.event), nil
}

func (r *hfcAbuseRiskRepoStub) UpdateReview(_ context.Context, update HFCAbuseRiskReviewUpdate) error {
	if r.event == nil || r.event.ID != update.EventID {
		return ErrHFCAbuseRiskEventNotFound
	}
	r.event.Status = update.Status
	r.event.ActionTaken = update.ActionTaken
	r.event.ActionNote = update.ActionNote
	r.event.ReviewedBy = update.ReviewedBy
	r.event.ReviewedAt = &update.ReviewedAt
	return nil
}

type hfcAbuseRiskAPIKeyRepoStub struct {
	APIKeyRepository
	keys []*APIKey
}

func (r *hfcAbuseRiskAPIKeyRepoStub) GetByID(_ context.Context, id int64) (*APIKey, error) {
	for _, key := range r.keys {
		if key.ID == id {
			cp := *key
			return &cp, nil
		}
	}
	return nil, ErrAPIKeyNotFound
}

func (r *hfcAbuseRiskAPIKeyRepoStub) Update(_ context.Context, key *APIKey) error {
	for _, item := range r.keys {
		if item.ID == key.ID {
			*item = *key
			return nil
		}
	}
	return ErrAPIKeyNotFound
}

func (r *hfcAbuseRiskAPIKeyRepoStub) ListByUserID(_ context.Context, userID int64, _ pagination.PaginationParams, filters APIKeyListFilters) ([]APIKey, *pagination.PaginationResult, error) {
	out := make([]APIKey, 0)
	for _, key := range r.keys {
		if key.UserID != userID {
			continue
		}
		if filters.Status != "" && key.Status != filters.Status {
			continue
		}
		out = append(out, *key)
	}
	return out, &pagination.PaginationResult{Total: int64(len(out)), Page: 1, PageSize: 200, Pages: 1}, nil
}

type hfcAbuseRiskUserRepoStub struct {
	UserRepository
	user *User
}

func (r *hfcAbuseRiskUserRepoStub) GetByID(_ context.Context, id int64) (*User, error) {
	if r.user == nil || r.user.ID != id {
		return nil, ErrUserNotFound
	}
	cp := *r.user
	return &cp, nil
}

func (r *hfcAbuseRiskUserRepoStub) Update(_ context.Context, user *User) error {
	*r.user = *user
	return nil
}

type hfcAbuseRiskCacheInvalidatorStub struct {
	keys    []string
	userIDs []int64
}

func (s *hfcAbuseRiskCacheInvalidatorStub) InvalidateAuthCacheByKey(_ context.Context, key string) {
	s.keys = append(s.keys, key)
}

func (s *hfcAbuseRiskCacheInvalidatorStub) InvalidateAuthCacheByUserID(_ context.Context, userID int64) {
	s.userIDs = append(s.userIDs, userID)
}

func (s *hfcAbuseRiskCacheInvalidatorStub) InvalidateAuthCacheByGroupID(_ context.Context, _ int64) {}
