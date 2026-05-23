package service

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/stretchr/testify/require"
)

type walletGroupKeyAPIKeyRepoStub struct {
	APIKeyRepository

	keys        []APIKey
	createCalls int
	listCalls   int
	created     []APIKey
	listUserID  int64
	listParams  pagination.PaginationParams
	listFilters APIKeyListFilters
}

func (s *walletGroupKeyAPIKeyRepoStub) ListByUserID(_ context.Context, userID int64, params pagination.PaginationParams, filters APIKeyListFilters) ([]APIKey, *pagination.PaginationResult, error) {
	s.listCalls++
	s.listUserID = userID
	s.listParams = params
	s.listFilters = filters
	out := make([]APIKey, len(s.keys))
	copy(out, s.keys)
	return out, &pagination.PaginationResult{Total: int64(len(out))}, nil
}

func (s *walletGroupKeyAPIKeyRepoStub) Create(_ context.Context, key *APIKey) error {
	s.createCalls++
	key.ID = 100 + int64(s.createCalls)
	s.created = append(s.created, *key)
	// 模拟新建后追加到 keys
	s.keys = append(s.keys, *key)
	return nil
}

type walletGroupKeyUserRepoStub struct {
	UserRepository
}

func (s walletGroupKeyUserRepoStub) GetByID(_ context.Context, id int64) (*User, error) {
	return &User{ID: id, Status: StatusActive}, nil
}

type walletGroupKeyGroupRepoStub struct {
	GroupRepository

	groups map[int64]*Group
}

func (s walletGroupKeyGroupRepoStub) GetByID(_ context.Context, id int64) (*Group, error) {
	if g, ok := s.groups[id]; ok {
		return g, nil
	}
	return nil, nil
}

func (s walletGroupKeyGroupRepoStub) GetAccountCount(_ context.Context, id int64) (int64, int64, error) {
	if _, ok := s.groups[id]; ok {
		return 1, 1, nil
	}
	return 0, 0, nil
}

func newWalletGroupKeyTestService(repo APIKeyRepository, groupRepo GroupRepository) *APIKeyService {
	return NewAPIKeyService(repo, walletGroupKeyUserRepoStub{}, groupRepo, nil, nil, nil, &config.Config{})
}

func TestAPIKeyServiceEnsureWalletGroupKeysCreatesAllForFreshUser(t *testing.T) {
	repo := &walletGroupKeyAPIKeyRepoStub{}
	groupRepo := walletGroupKeyGroupRepoStub{groups: map[int64]*Group{
		2: {ID: 2, Name: "gpt-5", Status: StatusActive},
		3: {ID: 3, Name: "claude-sonnet", Status: StatusActive},
		4: {ID: 4, Name: "gemini-2-pro", Status: StatusActive},
	}}
	svc := newWalletGroupKeyTestService(repo, groupRepo)

	keys, createdCount, err := svc.EnsureWalletGroupKeys(context.Background(), 42, []int64{2, 3, 4})

	require.NoError(t, err)
	require.Equal(t, 3, createdCount)
	require.Len(t, keys, 3)
	require.Equal(t, 3, repo.createCalls)
	require.Equal(t, "钱包-gpt-5", keys[0].Name)
	require.Equal(t, "钱包-claude-sonnet", keys[1].Name)
	require.Equal(t, "钱包-gemini-2-pro", keys[2].Name)
	require.NotNil(t, keys[0].GroupID)
	require.Equal(t, int64(2), *keys[0].GroupID)
	require.Equal(t, int64(3), *keys[1].GroupID)
	require.Equal(t, int64(4), *keys[2].GroupID)
}

func TestAPIKeyServiceEnsureWalletGroupKeysReusesExisting(t *testing.T) {
	gid2 := int64(2)
	gid3 := int64(3)
	repo := &walletGroupKeyAPIKeyRepoStub{
		keys: []APIKey{
			{ID: 7, UserID: 42, Name: "钱包-gpt-5", GroupID: &gid2, Status: StatusAPIKeyActive},
			{ID: 8, UserID: 42, Name: "钱包-claude-sonnet", GroupID: &gid3, Status: StatusAPIKeyActive},
		},
	}
	groupRepo := walletGroupKeyGroupRepoStub{groups: map[int64]*Group{
		2: {ID: 2, Name: "gpt-5", Status: StatusActive},
		3: {ID: 3, Name: "claude-sonnet", Status: StatusActive},
		4: {ID: 4, Name: "gemini-2-pro", Status: StatusActive},
	}}
	svc := newWalletGroupKeyTestService(repo, groupRepo)

	keys, createdCount, err := svc.EnsureWalletGroupKeys(context.Background(), 42, []int64{2, 3, 4})

	require.NoError(t, err)
	require.Equal(t, 1, createdCount)
	require.Len(t, keys, 3)
	require.Equal(t, int64(7), keys[0].ID, "应复用已存在的 GPT 钱包 key")
	require.Equal(t, int64(8), keys[1].ID, "应复用已存在的 Sonnet 钱包 key")
	require.Equal(t, "钱包-gemini-2-pro", keys[2].Name, "Gemini 之前缺，应新建")
}

func TestAPIKeyServiceEnsureWalletGroupKeysIgnoresNonWalletKeys(t *testing.T) {
	// 已存在的 v3 普通 key（不带"钱包-"前缀）不应被复用为钱包 key
	gid2 := int64(2)
	repo := &walletGroupKeyAPIKeyRepoStub{
		keys: []APIKey{
			{ID: 9, UserID: 42, Name: "old-v3-gpt-key", GroupID: &gid2, Status: StatusAPIKeyActive},
		},
	}
	groupRepo := walletGroupKeyGroupRepoStub{groups: map[int64]*Group{
		2: {ID: 2, Name: "gpt-5", Status: StatusActive},
	}}
	svc := newWalletGroupKeyTestService(repo, groupRepo)

	keys, createdCount, err := svc.EnsureWalletGroupKeys(context.Background(), 42, []int64{2})

	require.NoError(t, err)
	require.Equal(t, 1, createdCount)
	require.Len(t, keys, 1)
	require.Equal(t, "钱包-gpt-5", keys[0].Name)
	require.NotEqual(t, int64(9), keys[0].ID, "v3 普通 key 不应被复用")
}

func TestAPIKeyServiceEnsureWalletGroupKeysEmptyInput(t *testing.T) {
	repo := &walletGroupKeyAPIKeyRepoStub{}
	svc := newWalletGroupKeyTestService(repo, walletGroupKeyGroupRepoStub{})

	keys, createdCount, err := svc.EnsureWalletGroupKeys(context.Background(), 42, nil)

	require.NoError(t, err)
	require.Equal(t, 0, createdCount)
	require.Nil(t, keys)
	require.Equal(t, 0, repo.listCalls)
	require.Equal(t, 0, repo.createCalls)
}

// --------------------------------------------------------------------------
// EnsureWalletUniversalKey 测试（5/14 反转决策：单 key 模式回归）
// --------------------------------------------------------------------------

func TestAPIKeyServiceEnsureWalletUniversalKeyCreatesForFreshUser(t *testing.T) {
	repo := &walletGroupKeyAPIKeyRepoStub{}
	svc := newWalletGroupKeyTestService(repo, walletGroupKeyGroupRepoStub{})

	key, created, err := svc.EnsureWalletUniversalKey(context.Background(), 42)

	require.NoError(t, err)
	require.True(t, created, "fresh user 应新建 universal key")
	require.NotNil(t, key)
	require.Equal(t, WalletUniversalAPIKeyName, key.Name)
	require.Nil(t, key.GroupID, "universal key 的 group_id 必须为 NULL")
	require.Equal(t, 1, repo.createCalls)
}

func TestAPIKeyServiceEnsureWalletUniversalKeyReusesExisting(t *testing.T) {
	repo := &walletGroupKeyAPIKeyRepoStub{
		keys: []APIKey{
			{
				ID:      55,
				UserID:  42,
				Name:    WalletUniversalAPIKeyName,
				GroupID: nil,
				Status:  StatusAPIKeyActive,
			},
		},
	}
	svc := newWalletGroupKeyTestService(repo, walletGroupKeyGroupRepoStub{})

	key, created, err := svc.EnsureWalletUniversalKey(context.Background(), 42)

	require.NoError(t, err)
	require.False(t, created, "存在 universal key 时应复用")
	require.NotNil(t, key)
	require.Equal(t, int64(55), key.ID)
	require.Equal(t, 0, repo.createCalls)
}

// TestAPIKeyServiceEnsureWalletUniversalKeyIgnoresGroupBoundKeys 验证：
// 用户名下绑 group 的 key（trial-bonus-auto 等）不会被当作 universal key 复用，
// 系统会新建一把真正的 group_id=NULL key。这正是 5/14 报障用户的场景。
func TestAPIKeyServiceEnsureWalletUniversalKeyIgnoresGroupBoundKeys(t *testing.T) {
	groupID := int64(17) // trial-bonus group
	repo := &walletGroupKeyAPIKeyRepoStub{
		keys: []APIKey{
			{
				ID:      86,
				UserID:  42,
				Name:    "trial-bonus-auto",
				GroupID: &groupID,
				Status:  StatusAPIKeyActive,
			},
		},
	}
	svc := newWalletGroupKeyTestService(repo, walletGroupKeyGroupRepoStub{})

	key, created, err := svc.EnsureWalletUniversalKey(context.Background(), 42)

	require.NoError(t, err)
	require.True(t, created, "绑 group 的老 key 不算 universal，应新建")
	require.NotNil(t, key)
	require.Nil(t, key.GroupID)
	require.Equal(t, WalletUniversalAPIKeyName, key.Name)
	require.Equal(t, 1, repo.createCalls)
}

// --------------------------------------------------------------------------
// GetWalletModelRoutes 测试（B1.5 路由列表，保留作底层能力）
// --------------------------------------------------------------------------

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
	require.Equal(t, 1.5, routes[0].EffectiveRateMultiplier)
	require.Equal(t, "gpt-*", routes[1].Pattern)
	require.Equal(t, int64(2), routes[1].GroupID)
	require.Equal(t, 1.0, routes[1].EffectiveRateMultiplier)
}
