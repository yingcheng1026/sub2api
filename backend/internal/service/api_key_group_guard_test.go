//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/stretchr/testify/require"
)

type apiKeyGroupGuardRepo struct {
	APIKeyRepository
	key     *APIKey
	updated *APIKey
	created *APIKey
}

func (r *apiKeyGroupGuardRepo) GetByID(context.Context, int64) (*APIKey, error) {
	clone := *r.key
	return &clone, nil
}

func (r *apiKeyGroupGuardRepo) Update(_ context.Context, key *APIKey) error {
	clone := *key
	r.updated = &clone
	return nil
}

func (r *apiKeyGroupGuardRepo) Create(_ context.Context, key *APIKey) error {
	clone := *key
	r.created = &clone
	return nil
}

type apiKeyGroupGuardUserRepo struct {
	UserRepository
	user *User
}

func (r apiKeyGroupGuardUserRepo) GetByID(context.Context, int64) (*User, error) {
	clone := *r.user
	return &clone, nil
}

type apiKeyGroupGuardGroupRepo struct {
	GroupRepository
	group  *Group
	total  int64
	active int64
}

func (r *apiKeyGroupGuardGroupRepo) GetByID(context.Context, int64) (*Group, error) {
	clone := *r.group
	return &clone, nil
}

func (r *apiKeyGroupGuardGroupRepo) GetAccountCount(context.Context, int64) (int64, int64, error) {
	return r.total, r.active, nil
}

type apiKeyGroupGuardSink struct {
	events []*logger.LogEvent
}

func (s *apiKeyGroupGuardSink) WriteLogEvent(event *logger.LogEvent) {
	s.events = append(s.events, event)
}

func TestAPIKeyServiceUpdateRejectsGroupWithoutAvailableAccounts(t *testing.T) {
	oldGroupID := int64(3)
	targetGroupID := int64(24)
	repo := &apiKeyGroupGuardRepo{
		key: &APIKey{ID: 250, UserID: 39, Key: "sk-test", GroupID: &oldGroupID, Status: StatusActive},
	}
	svc := NewAPIKeyService(
		repo,
		apiKeyGroupGuardUserRepo{user: &User{ID: 39, Role: RoleUser}},
		&apiKeyGroupGuardGroupRepo{
			group:  &Group{ID: targetGroupID, Status: StatusActive, SubscriptionType: SubscriptionTypeStandard},
			total:  0,
			active: 0,
		},
		userSubRepoNoop{},
		nil,
		nil,
		&config.Config{},
	)

	got, err := svc.Update(context.Background(), 250, 39, UpdateAPIKeyRequest{GroupID: &targetGroupID})

	require.Nil(t, got)
	require.Error(t, err)
	require.Contains(t, err.Error(), "no available accounts")
	require.Nil(t, repo.updated)
}

func TestAPIKeyServiceCreateRejectsGroupWithoutAvailableAccounts(t *testing.T) {
	targetGroupID := int64(24)
	repo := &apiKeyGroupGuardRepo{}
	svc := NewAPIKeyService(
		repo,
		apiKeyGroupGuardUserRepo{user: &User{ID: 39, Role: RoleUser}},
		&apiKeyGroupGuardGroupRepo{
			group:  &Group{ID: targetGroupID, Status: StatusActive, SubscriptionType: SubscriptionTypeStandard},
			total:  0,
			active: 0,
		},
		userSubRepoNoop{},
		nil,
		nil,
		&config.Config{},
	)

	got, err := svc.Create(context.Background(), 39, CreateAPIKeyRequest{Name: "test", GroupID: &targetGroupID})

	require.Nil(t, got)
	require.Error(t, err)
	require.Contains(t, err.Error(), "no available accounts")
	require.Nil(t, repo.created)
}

func TestAPIKeyServiceUpdateAuditsSuccessfulGroupChange(t *testing.T) {
	oldGroupID := int64(3)
	targetGroupID := int64(4)
	repo := &apiKeyGroupGuardRepo{
		key: &APIKey{ID: 250, UserID: 39, Key: "sk-test", GroupID: &oldGroupID, Status: StatusActive},
	}
	sink := &apiKeyGroupGuardSink{}
	logger.SetSink(sink)
	defer logger.SetSink(nil)

	svc := NewAPIKeyService(
		repo,
		apiKeyGroupGuardUserRepo{user: &User{ID: 39, Role: RoleUser}},
		&apiKeyGroupGuardGroupRepo{
			group:  &Group{ID: targetGroupID, Status: StatusActive, SubscriptionType: SubscriptionTypeStandard},
			total:  1,
			active: 1,
		},
		userSubRepoNoop{},
		nil,
		nil,
		&config.Config{},
	)

	got, err := svc.Update(context.Background(), 250, 39, UpdateAPIKeyRequest{GroupID: &targetGroupID})

	require.NoError(t, err)
	require.NotNil(t, got)
	require.NotNil(t, repo.updated)
	require.Len(t, sink.events, 1)
	require.Equal(t, "audit.api_key_group_change", sink.events[0].Component)
	require.Equal(t, "api key group changed", sink.events[0].Message)
	require.EqualValues(t, 39, sink.events[0].Fields["user_id"])
	require.EqualValues(t, 250, sink.events[0].Fields["api_key_id"])
	require.Equal(t, &oldGroupID, sink.events[0].Fields["old_group_id"])
	require.Equal(t, &targetGroupID, sink.events[0].Fields["new_group_id"])
}

func (r *apiKeyGroupGuardRepo) GetKeyAndOwnerID(context.Context, int64) (string, int64, error) {
	panic("unexpected GetKeyAndOwnerID call")
}
func (r *apiKeyGroupGuardRepo) GetByKey(context.Context, string) (*APIKey, error) {
	panic("unexpected GetByKey call")
}
func (r *apiKeyGroupGuardRepo) GetByKeyForAuth(context.Context, string) (*APIKey, error) {
	panic("unexpected GetByKeyForAuth call")
}
func (r *apiKeyGroupGuardRepo) Delete(context.Context, int64) error { panic("unexpected Delete call") }
func (r *apiKeyGroupGuardRepo) ListByUserID(context.Context, int64, pagination.PaginationParams, APIKeyListFilters) ([]APIKey, *pagination.PaginationResult, error) {
	panic("unexpected ListByUserID call")
}
func (r *apiKeyGroupGuardRepo) VerifyOwnership(context.Context, int64, []int64) ([]int64, error) {
	panic("unexpected VerifyOwnership call")
}
func (r *apiKeyGroupGuardRepo) CountByUserID(context.Context, int64) (int64, error) {
	panic("unexpected CountByUserID call")
}
func (r *apiKeyGroupGuardRepo) ExistsByKey(context.Context, string) (bool, error) {
	panic("unexpected ExistsByKey call")
}
func (r *apiKeyGroupGuardRepo) ListByGroupID(context.Context, int64, pagination.PaginationParams) ([]APIKey, *pagination.PaginationResult, error) {
	panic("unexpected ListByGroupID call")
}
func (r *apiKeyGroupGuardRepo) SearchAPIKeys(context.Context, int64, string, int) ([]APIKey, error) {
	panic("unexpected SearchAPIKeys call")
}
func (r *apiKeyGroupGuardRepo) ClearGroupIDByGroupID(context.Context, int64) (int64, error) {
	panic("unexpected ClearGroupIDByGroupID call")
}
func (r *apiKeyGroupGuardRepo) UpdateGroupIDByUserAndGroup(context.Context, int64, int64, int64) (int64, error) {
	panic("unexpected UpdateGroupIDByUserAndGroup call")
}
func (r *apiKeyGroupGuardRepo) CountByGroupID(context.Context, int64) (int64, error) {
	panic("unexpected CountByGroupID call")
}
func (r *apiKeyGroupGuardRepo) ListKeysByUserID(context.Context, int64) ([]string, error) {
	panic("unexpected ListKeysByUserID call")
}
func (r *apiKeyGroupGuardRepo) ListKeysByGroupID(context.Context, int64) ([]string, error) {
	panic("unexpected ListKeysByGroupID call")
}
func (r *apiKeyGroupGuardRepo) IncrementQuotaUsed(context.Context, int64, float64) (float64, error) {
	panic("unexpected IncrementQuotaUsed call")
}
func (r *apiKeyGroupGuardRepo) UpdateLastUsed(context.Context, int64, time.Time) error {
	panic("unexpected UpdateLastUsed call")
}
func (r *apiKeyGroupGuardRepo) IncrementRateLimitUsage(context.Context, int64, float64) error {
	panic("unexpected IncrementRateLimitUsage call")
}
func (r *apiKeyGroupGuardRepo) ResetRateLimitWindows(context.Context, int64) error {
	panic("unexpected ResetRateLimitWindows call")
}
func (r *apiKeyGroupGuardRepo) GetRateLimitData(context.Context, int64) (*APIKeyRateLimitData, error) {
	panic("unexpected GetRateLimitData call")
}
