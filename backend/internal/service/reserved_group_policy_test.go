//go:build unit

package service

import (
	"context"
	"testing"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/stretchr/testify/require"
)

type reservedGroupRepoStub struct {
	group              *Group
	createCalls        int
	updateCalls        int
	deleteCalls        int
	deleteCascadeCalls int
}

func (s *reservedGroupRepoStub) Create(_ context.Context, group *Group) error {
	s.createCalls++
	s.group = group
	return nil
}

func (s *reservedGroupRepoStub) GetByID(_ context.Context, _ int64) (*Group, error) {
	if s.group == nil {
		return nil, ErrGroupNotFound
	}
	return s.group, nil
}

func (s *reservedGroupRepoStub) GetByIDLite(ctx context.Context, id int64) (*Group, error) {
	return s.GetByID(ctx, id)
}

func (s *reservedGroupRepoStub) Update(_ context.Context, group *Group) error {
	s.updateCalls++
	s.group = group
	return nil
}

func (s *reservedGroupRepoStub) Delete(_ context.Context, _ int64) error {
	s.deleteCalls++
	return nil
}

func (s *reservedGroupRepoStub) DeleteCascade(_ context.Context, _ int64) ([]int64, error) {
	s.deleteCascadeCalls++
	return nil, nil
}

func (s *reservedGroupRepoStub) List(context.Context, pagination.PaginationParams) ([]Group, *pagination.PaginationResult, error) {
	panic("unexpected List call")
}

func (s *reservedGroupRepoStub) ListWithFilters(context.Context, pagination.PaginationParams, string, string, string, *bool) ([]Group, *pagination.PaginationResult, error) {
	panic("unexpected ListWithFilters call")
}

func (s *reservedGroupRepoStub) ListActive(context.Context) ([]Group, error) {
	panic("unexpected ListActive call")
}

func (s *reservedGroupRepoStub) ListActiveByPlatform(context.Context, string) ([]Group, error) {
	panic("unexpected ListActiveByPlatform call")
}

func (s *reservedGroupRepoStub) ExistsByName(context.Context, string) (bool, error) {
	return false, nil
}

func (s *reservedGroupRepoStub) GetAccountCount(context.Context, int64) (int64, int64, error) {
	return 0, 0, nil
}

func (s *reservedGroupRepoStub) DeleteAccountGroupsByGroupID(context.Context, int64) (int64, error) {
	panic("unexpected DeleteAccountGroupsByGroupID call")
}

func (s *reservedGroupRepoStub) GetAccountIDsByGroupIDs(context.Context, []int64) ([]int64, error) {
	panic("unexpected GetAccountIDsByGroupIDs call")
}

func (s *reservedGroupRepoStub) BindAccountsToGroup(context.Context, int64, []int64) error {
	panic("unexpected BindAccountsToGroup call")
}

func (s *reservedGroupRepoStub) UpdateSortOrders(context.Context, []GroupSortOrderUpdate) error {
	panic("unexpected UpdateSortOrders call")
}

func validReservedGroup(name string) *Group {
	group := &Group{
		ID:               3,
		Name:             name,
		Status:           StatusActive,
		Hydrated:         true,
		SubscriptionType: SubscriptionTypeStandard,
		RateMultiplier:   1,
	}
	if name == WalletDefaultVIPGroupName {
		group.ID = 22
		group.Platform = PlatformAnthropic
		group.IsExclusive = true
	} else {
		group.Platform = PlatformOpenAI
	}
	return group
}

func requireReservedGroupPolicyError(t *testing.T, err error, detail string) {
	t.Helper()
	require.Error(t, err)
	require.Equal(t, "RESERVED_GROUP_POLICY_VIOLATION", infraerrors.Reason(err))
	require.Contains(t, infraerrors.Message(err), detail)
}

func TestAdminService_CreateGroup_RejectsReservedPolicyDrift(t *testing.T) {
	fallbackID := int64(99)
	tests := []struct {
		name   string
		input  *CreateGroupInput
		detail string
	}{
		{
			name: "openai default wrong platform",
			input: &CreateGroupInput{
				Name: WalletDefaultOpenAIGroupName, Platform: PlatformAnthropic,
				RateMultiplier: 1, SubscriptionType: SubscriptionTypeStandard,
			},
			detail: "platform must be openai",
		},
		{
			name: "openai default exclusive",
			input: &CreateGroupInput{
				Name: WalletDefaultOpenAIGroupName, Platform: PlatformOpenAI,
				RateMultiplier: 1, SubscriptionType: SubscriptionTypeStandard, IsExclusive: true,
			},
			detail: "is_exclusive must be false",
		},
		{
			name: "openai default subscription",
			input: &CreateGroupInput{
				Name: WalletDefaultOpenAIGroupName, Platform: PlatformOpenAI,
				RateMultiplier: 1, SubscriptionType: SubscriptionTypeSubscription,
			},
			detail: "subscription_type must be standard",
		},
		{
			name: "vip wrong platform",
			input: &CreateGroupInput{
				Name: WalletDefaultVIPGroupName, Platform: PlatformOpenAI,
				RateMultiplier: 1, SubscriptionType: SubscriptionTypeStandard, IsExclusive: true,
			},
			detail: "platform must be anthropic",
		},
		{
			name: "vip not exclusive",
			input: &CreateGroupInput{
				Name: WalletDefaultVIPGroupName, Platform: PlatformAnthropic,
				RateMultiplier: 1, SubscriptionType: SubscriptionTypeStandard,
			},
			detail: "is_exclusive must be true",
		},
		{
			name: "vip subscription",
			input: &CreateGroupInput{
				Name: WalletDefaultVIPGroupName, Platform: PlatformAnthropic,
				RateMultiplier: 1, SubscriptionType: SubscriptionTypeSubscription, IsExclusive: true,
			},
			detail: "subscription_type must be standard",
		},
		{
			name: "openai default claude code only",
			input: &CreateGroupInput{
				Name: WalletDefaultOpenAIGroupName, Platform: PlatformOpenAI,
				RateMultiplier: 1, SubscriptionType: SubscriptionTypeStandard, ClaudeCodeOnly: true,
			},
			detail: "claude_code_only must be false",
		},
		{
			name: "vip client fallback",
			input: &CreateGroupInput{
				Name: WalletDefaultVIPGroupName, Platform: PlatformAnthropic,
				RateMultiplier: 1, SubscriptionType: SubscriptionTypeStandard, IsExclusive: true,
				FallbackGroupID: &fallbackID,
			},
			detail: "fallback_group_id must be unset",
		},
		{
			name: "vip invalid request fallback",
			input: &CreateGroupInput{
				Name: WalletDefaultVIPGroupName, Platform: PlatformAnthropic,
				RateMultiplier: 1, SubscriptionType: SubscriptionTypeStandard, IsExclusive: true,
				FallbackGroupIDOnInvalidRequest: &fallbackID,
			},
			detail: "fallback_group_id_on_invalid_request must be unset",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := &reservedGroupRepoStub{}
			svc := &adminServiceImpl{groupRepo: repo}

			_, err := svc.CreateGroup(context.Background(), tt.input)

			requireReservedGroupPolicyError(t, err, tt.detail)
			require.Zero(t, repo.createCalls)
		})
	}
}

func TestAdminService_CreateGroup_AllowsExactReservedPolicies(t *testing.T) {
	tests := []struct {
		name      string
		platform  string
		exclusive bool
	}{
		{name: WalletDefaultOpenAIGroupName, platform: PlatformOpenAI, exclusive: false},
		{name: WalletDefaultVIPGroupName, platform: PlatformAnthropic, exclusive: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := &reservedGroupRepoStub{}
			svc := &adminServiceImpl{groupRepo: repo}

			_, err := svc.CreateGroup(context.Background(), &CreateGroupInput{
				Name: tt.name, Platform: tt.platform, RateMultiplier: 1,
				SubscriptionType: SubscriptionTypeStandard, IsExclusive: tt.exclusive,
			})

			require.NoError(t, err)
			require.Equal(t, 1, repo.createCalls)
		})
	}
}

func TestAdminService_UpdateGroup_RejectsReservedPolicyDrift(t *testing.T) {
	wrongPlatform := PlatformAnthropic
	wrongVIPPlatform := PlatformOpenAI
	wrongSubscriptionType := SubscriptionTypeSubscription
	disabled := StatusDisabled
	exclusive := true
	nonExclusive := false
	claudeCodeOnly := true
	fallbackID := int64(99)

	tests := []struct {
		name      string
		groupName string
		input     *UpdateGroupInput
		detail    string
	}{
		{name: "rename openai default", groupName: WalletDefaultOpenAIGroupName, input: &UpdateGroupInput{Name: "renamed"}, detail: "cannot be renamed"},
		{name: "disable openai default", groupName: WalletDefaultOpenAIGroupName, input: &UpdateGroupInput{Status: disabled}, detail: "status must be active"},
		{name: "change openai default platform", groupName: WalletDefaultOpenAIGroupName, input: &UpdateGroupInput{Platform: wrongPlatform}, detail: "platform must be openai"},
		{name: "change openai default subscription type", groupName: WalletDefaultOpenAIGroupName, input: &UpdateGroupInput{SubscriptionType: wrongSubscriptionType}, detail: "subscription_type must be standard"},
		{name: "make openai default exclusive", groupName: WalletDefaultOpenAIGroupName, input: &UpdateGroupInput{IsExclusive: &exclusive}, detail: "is_exclusive must be false"},
		{name: "rename vip", groupName: WalletDefaultVIPGroupName, input: &UpdateGroupInput{Name: "renamed"}, detail: "cannot be renamed"},
		{name: "disable vip", groupName: WalletDefaultVIPGroupName, input: &UpdateGroupInput{Status: disabled}, detail: "status must be active"},
		{name: "change vip platform", groupName: WalletDefaultVIPGroupName, input: &UpdateGroupInput{Platform: wrongVIPPlatform}, detail: "platform must be anthropic"},
		{name: "change vip subscription type", groupName: WalletDefaultVIPGroupName, input: &UpdateGroupInput{SubscriptionType: wrongSubscriptionType}, detail: "subscription_type must be standard"},
		{name: "make vip nonexclusive", groupName: WalletDefaultVIPGroupName, input: &UpdateGroupInput{IsExclusive: &nonExclusive}, detail: "is_exclusive must be true"},
		{name: "make openai default claude code only", groupName: WalletDefaultOpenAIGroupName, input: &UpdateGroupInput{ClaudeCodeOnly: &claudeCodeOnly}, detail: "claude_code_only must be false"},
		{name: "set vip client fallback", groupName: WalletDefaultVIPGroupName, input: &UpdateGroupInput{FallbackGroupID: &fallbackID}, detail: "fallback_group_id must be unset"},
		{name: "set vip invalid request fallback", groupName: WalletDefaultVIPGroupName, input: &UpdateGroupInput{FallbackGroupIDOnInvalidRequest: &fallbackID}, detail: "fallback_group_id_on_invalid_request must be unset"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := &reservedGroupRepoStub{group: validReservedGroup(tt.groupName)}
			svc := &adminServiceImpl{groupRepo: repo}

			_, err := svc.UpdateGroup(context.Background(), repo.group.ID, tt.input)

			requireReservedGroupPolicyError(t, err, tt.detail)
			require.Zero(t, repo.updateCalls)
		})
	}
}

func TestAdminService_UpdateGroup_RejectsAnyRenameIntoReservedName(t *testing.T) {
	tests := []struct {
		name        string
		newName     string
		platform    string
		isExclusive bool
	}{
		{name: "rename anthropic group into openai default", newName: WalletDefaultOpenAIGroupName, platform: PlatformAnthropic},
		{name: "rename nonexclusive group into vip", newName: WalletDefaultVIPGroupName, platform: PlatformAnthropic},
		{name: "rename otherwise compliant group into openai default", newName: WalletDefaultOpenAIGroupName, platform: PlatformOpenAI},
		{name: "rename otherwise compliant group into vip", newName: WalletDefaultVIPGroupName, platform: PlatformAnthropic, isExclusive: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := &reservedGroupRepoStub{group: &Group{
				ID:               99,
				Name:             "ordinary-group",
				Platform:         tt.platform,
				IsExclusive:      tt.isExclusive,
				Status:           StatusActive,
				Hydrated:         true,
				SubscriptionType: SubscriptionTypeStandard,
				RateMultiplier:   1,
			}}
			svc := &adminServiceImpl{groupRepo: repo}

			_, err := svc.UpdateGroup(context.Background(), repo.group.ID, &UpdateGroupInput{Name: tt.newName})

			requireReservedGroupPolicyError(t, err, "cannot be assigned by renaming another group")
			require.Zero(t, repo.updateCalls)
		})
	}
}

func TestAdminService_UpdateGroup_RejectsUnhydratedReservedGroup(t *testing.T) {
	group := validReservedGroup(WalletDefaultVIPGroupName)
	group.Hydrated = false
	repo := &reservedGroupRepoStub{group: group}
	svc := &adminServiceImpl{groupRepo: repo}

	_, err := svc.UpdateGroup(context.Background(), group.ID, &UpdateGroupInput{Description: "safe metadata change"})

	requireReservedGroupPolicyError(t, err, "hydrated must be true")
	require.Zero(t, repo.updateCalls)
}

func TestAdminService_UpdateGroup_AllowsSafeReservedMetadataChange(t *testing.T) {
	group := validReservedGroup(WalletDefaultVIPGroupName)
	repo := &reservedGroupRepoStub{group: group}
	svc := &adminServiceImpl{groupRepo: repo}

	updated, err := svc.UpdateGroup(context.Background(), group.ID, &UpdateGroupInput{Description: "operator note"})

	require.NoError(t, err)
	require.Equal(t, 1, repo.updateCalls)
	require.Equal(t, "operator note", updated.Description)
}

func TestAdminService_UpdateGroup_AllowsRepairingReservedFallbackDrift(t *testing.T) {
	fallbackID := int64(99)
	group := validReservedGroup(WalletDefaultVIPGroupName)
	group.ClaudeCodeOnly = true
	group.FallbackGroupID = &fallbackID
	group.FallbackGroupIDOnInvalidRequest = &fallbackID
	repo := &reservedGroupRepoStub{group: group}
	svc := &adminServiceImpl{groupRepo: repo}
	clear := int64(0)
	claudeCodeOnly := false

	updated, err := svc.UpdateGroup(context.Background(), group.ID, &UpdateGroupInput{
		ClaudeCodeOnly:                  &claudeCodeOnly,
		FallbackGroupID:                 &clear,
		FallbackGroupIDOnInvalidRequest: &clear,
	})

	require.NoError(t, err)
	require.Equal(t, 1, repo.updateCalls)
	require.False(t, updated.ClaudeCodeOnly)
	require.Nil(t, updated.FallbackGroupID)
	require.Nil(t, updated.FallbackGroupIDOnInvalidRequest)
}

func TestAdminService_DeleteGroup_RejectsReservedGroups(t *testing.T) {
	for _, name := range []string{WalletDefaultOpenAIGroupName, WalletDefaultVIPGroupName} {
		t.Run(name, func(t *testing.T) {
			repo := &reservedGroupRepoStub{group: validReservedGroup(name)}
			svc := &adminServiceImpl{groupRepo: repo}

			err := svc.DeleteGroup(context.Background(), repo.group.ID)

			requireReservedGroupPolicyError(t, err, "cannot be deleted")
			require.Zero(t, repo.deleteCascadeCalls)
		})
	}
}

func TestGroupService_RejectsReservedGroupCreateUpdateAndDeleteDrift(t *testing.T) {
	t.Run("create openai-default through anthropic-only legacy path", func(t *testing.T) {
		repo := &reservedGroupRepoStub{}
		svc := NewGroupService(repo, nil)

		_, err := svc.Create(context.Background(), CreateGroupRequest{
			Name: WalletDefaultOpenAIGroupName, RateMultiplier: 1,
		})

		requireReservedGroupPolicyError(t, err, "platform must be openai")
		require.Zero(t, repo.createCalls)
	})

	t.Run("disable vip", func(t *testing.T) {
		repo := &reservedGroupRepoStub{group: validReservedGroup(WalletDefaultVIPGroupName)}
		svc := NewGroupService(repo, nil)

		status := StatusDisabled
		_, err := svc.Update(context.Background(), repo.group.ID, UpdateGroupRequest{Status: &status})

		requireReservedGroupPolicyError(t, err, "status must be active")
		require.Zero(t, repo.updateCalls)
	})

	t.Run("delete vip", func(t *testing.T) {
		repo := &reservedGroupRepoStub{group: validReservedGroup(WalletDefaultVIPGroupName)}
		svc := NewGroupService(repo, nil)

		err := svc.Delete(context.Background(), repo.group.ID)

		requireReservedGroupPolicyError(t, err, "cannot be deleted")
		require.Zero(t, repo.deleteCalls)
	})
}
