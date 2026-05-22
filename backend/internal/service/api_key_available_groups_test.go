package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

type availableGroupsUserRepoStub struct {
	UserRepository
	user *User
}

func (s availableGroupsUserRepoStub) GetByID(context.Context, int64) (*User, error) {
	return s.user, nil
}

type availableGroupsGroupRepoStub struct {
	GroupRepository
	groups []Group
}

func (s availableGroupsGroupRepoStub) ListActive(context.Context) ([]Group, error) {
	out := make([]Group, len(s.groups))
	copy(out, s.groups)
	return out, nil
}

type availableGroupsSubRepoStub struct {
	UserSubscriptionRepository
	subs []UserSubscription
}

func (s availableGroupsSubRepoStub) ListActiveByUserID(context.Context, int64) ([]UserSubscription, error) {
	out := make([]UserSubscription, len(s.subs))
	copy(out, s.subs)
	return out, nil
}

func TestAPIKeyServiceGetAvailableGroupsHidesEmptyStandardGroups(t *testing.T) {
	subscriptionGroupID := int64(14)
	svc := NewAPIKeyService(
		nil,
		availableGroupsUserRepoStub{user: &User{ID: 48, Status: StatusActive}},
		availableGroupsGroupRepoStub{groups: []Group{
			{
				ID:                  3,
				Name:                "openai-default",
				Status:              StatusActive,
				SubscriptionType:    SubscriptionTypeStandard,
				AccountCountsLoaded: true,
				ActiveAccountCount:  2,
			},
			{
				ID:                  5,
				Name:                "cc-antigravity",
				Status:              StatusActive,
				SubscriptionType:    SubscriptionTypeStandard,
				AccountCountsLoaded: true,
				ActiveAccountCount:  0,
			},
			{
				ID:               subscriptionGroupID,
				Name:             "paid-standard-v3",
				Status:           StatusActive,
				SubscriptionType: SubscriptionTypeSubscription,
			},
		}},
		availableGroupsSubRepoStub{subs: []UserSubscription{{GroupID: &subscriptionGroupID}}},
		nil,
		nil,
		nil,
	)

	groups, err := svc.GetAvailableGroups(context.Background(), 48)

	require.NoError(t, err)
	require.Equal(t, []int64{3, subscriptionGroupID}, groupIDs(groups))
}

func TestAPIKeyServiceGetAvailableGroupsKeepsStandardGroupsWhenAccountCountsMissing(t *testing.T) {
	svc := NewAPIKeyService(
		nil,
		availableGroupsUserRepoStub{user: &User{ID: 48, Status: StatusActive}},
		availableGroupsGroupRepoStub{groups: []Group{
			{
				ID:               3,
				Name:             "openai-default",
				Status:           StatusActive,
				SubscriptionType: SubscriptionTypeStandard,
			},
		}},
		availableGroupsSubRepoStub{},
		nil,
		nil,
		nil,
	)

	groups, err := svc.GetAvailableGroups(context.Background(), 48)

	require.NoError(t, err)
	require.Equal(t, []int64{3}, groupIDs(groups))
}

func groupIDs(groups []Group) []int64 {
	out := make([]int64, 0, len(groups))
	for _, group := range groups {
		out = append(out, group.ID)
	}
	return out
}
