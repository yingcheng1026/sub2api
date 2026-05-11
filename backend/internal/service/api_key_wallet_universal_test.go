package service

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/stretchr/testify/require"
)

type walletUniversalAPIKeyRepoStub struct {
	APIKeyRepository

	keys        []APIKey
	createCalls int
	listCalls   int
	created     *APIKey
	listUserID  int64
	listParams  pagination.PaginationParams
	listFilters APIKeyListFilters
}

func (s *walletUniversalAPIKeyRepoStub) ListByUserID(_ context.Context, userID int64, params pagination.PaginationParams, filters APIKeyListFilters) ([]APIKey, *pagination.PaginationResult, error) {
	s.listCalls++
	s.listUserID = userID
	s.listParams = params
	s.listFilters = filters
	out := make([]APIKey, len(s.keys))
	copy(out, s.keys)
	return out, &pagination.PaginationResult{Total: int64(len(out))}, nil
}

func (s *walletUniversalAPIKeyRepoStub) Create(_ context.Context, key *APIKey) error {
	s.createCalls++
	cp := *key
	cp.ID = 100 + int64(s.createCalls)
	key.ID = cp.ID
	s.created = &cp
	return nil
}

type walletUniversalUserRepoStub struct {
	UserRepository
}

func (s walletUniversalUserRepoStub) GetByID(_ context.Context, id int64) (*User, error) {
	return &User{ID: id, Status: StatusActive}, nil
}

func TestAPIKeyServiceEnsureWalletUniversalKeyCreatesWhenMissing(t *testing.T) {
	repo := &walletUniversalAPIKeyRepoStub{}
	svc := NewAPIKeyService(repo, walletUniversalUserRepoStub{}, nil, nil, nil, nil, &config.Config{})

	key, created, err := svc.EnsureWalletUniversalKey(context.Background(), 42)

	require.NoError(t, err)
	require.True(t, created)
	require.NotNil(t, key)
	require.Equal(t, 1, repo.listCalls)
	require.Equal(t, int64(42), repo.listUserID)
	require.Equal(t, StatusAPIKeyActive, repo.listFilters.Status)
	require.Equal(t, 1, repo.createCalls)
	require.Equal(t, WalletUniversalAPIKeyName, key.Name)
	require.Nil(t, key.GroupID)
	require.Equal(t, StatusActive, key.Status)
	require.NotEmpty(t, key.Key)
}

func TestAPIKeyServiceEnsureWalletUniversalKeyReusesExistingActiveUniversalKey(t *testing.T) {
	repo := &walletUniversalAPIKeyRepoStub{
		keys: []APIKey{
			{ID: 7, UserID: 42, Key: "sk-existing", Name: "existing", GroupID: nil, Status: StatusAPIKeyActive},
		},
	}
	svc := NewAPIKeyService(repo, walletUniversalUserRepoStub{}, nil, nil, nil, nil, &config.Config{})

	key, created, err := svc.EnsureWalletUniversalKey(context.Background(), 42)

	require.NoError(t, err)
	require.False(t, created)
	require.NotNil(t, key)
	require.Equal(t, int64(7), key.ID)
	require.Equal(t, "sk-existing", key.Key)
	require.Equal(t, 1, repo.listCalls)
	require.Equal(t, 0, repo.createCalls)
}

type walletModelRouteGroupRepoStub struct {
	GroupRepository

	groups []Group
}

func (s walletModelRouteGroupRepoStub) ListActive(context.Context) ([]Group, error) {
	out := make([]Group, len(s.groups))
	copy(out, s.groups)
	return out, nil
}

type walletModelRouteUserRateRepoStub struct {
	UserGroupRateRepository

	rates map[int64]float64
}

func (s walletModelRouteUserRateRepoStub) GetByUserID(context.Context, int64) (map[int64]float64, error) {
	out := make(map[int64]float64, len(s.rates))
	for groupID, rate := range s.rates {
		out[groupID] = rate
	}
	return out, nil
}

func TestAPIKeyServiceGetWalletModelRoutesUsesConfiguredRoutesAndGroups(t *testing.T) {
	groupRepo := walletModelRouteGroupRepoStub{groups: []Group{
		{ID: 2, Name: "gpt-5", Platform: PlatformOpenAI, Status: StatusActive, RateMultiplier: 1.0},
		{ID: 3, Name: "claude-sonnet", Platform: PlatformAnthropic, Status: StatusActive, RateMultiplier: 1.5},
	}}
	rateRepo := walletModelRouteUserRateRepoStub{rates: map[int64]float64{3: 1.25}}
	svc := NewAPIKeyService(nil, nil, groupRepo, nil, rateRepo, nil, &config.Config{})

	routes, err := svc.GetWalletModelRoutes(context.Background(), 42, []ModelRoute{
		{Pattern: "claude-sonnet-*", GroupName: "claude-sonnet", ExampleModel: "claude-sonnet-4-6"},
		{Pattern: "gpt-*", GroupName: "gpt-5", ExampleModel: "gpt-5"},
		{Pattern: "missing-*", GroupName: "missing", ExampleModel: "missing-model"},
	})

	require.NoError(t, err)
	require.Len(t, routes, 2)
	require.Equal(t, "claude-sonnet-*", routes[0].Pattern)
	require.Equal(t, "claude-sonnet-4-6", routes[0].ExampleModel)
	require.Equal(t, int64(3), routes[0].GroupID)
	require.Equal(t, "claude-sonnet", routes[0].GroupName)
	require.Equal(t, 1.5, routes[0].RateMultiplier)
	require.Equal(t, 1.25, routes[0].EffectiveRateMultiplier)
	require.Equal(t, "gpt-*", routes[1].Pattern)
	require.Equal(t, int64(2), routes[1].GroupID)
	require.Equal(t, 1.0, routes[1].EffectiveRateMultiplier)
}
