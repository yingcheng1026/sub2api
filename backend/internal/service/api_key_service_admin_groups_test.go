//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

type noActiveAPIKeySubscriptionRepo struct {
	UserSubscriptionRepository
}

func (r noActiveAPIKeySubscriptionRepo) GetActiveByUserIDAndGroupID(context.Context, int64, int64) (*UserSubscription, error) {
	return nil, ErrSubscriptionNotFound
}

func TestAPIKeyServiceAdminCanBindAnyGroup(t *testing.T) {
	svc := &APIKeyService{userSubRepo: noActiveAPIKeySubscriptionRepo{}}
	admin := &User{ID: 1, Role: RoleAdmin, Status: StatusActive}
	subscribedGroupIDs := map[int64]bool{}

	groups := []Group{
		{
			ID:               7,
			Name:             "exclusive-standard",
			IsExclusive:      true,
			SubscriptionType: SubscriptionTypeStandard,
			Status:           StatusActive,
		},
		{
			ID:               13,
			Name:             "subscription",
			IsExclusive:      false,
			SubscriptionType: SubscriptionTypeSubscription,
			Status:           StatusActive,
		},
	}

	for i := range groups {
		group := &groups[i]
		require.True(t, svc.canUserBindGroup(context.Background(), admin, group), group.Name)
		require.True(t, svc.canUserBindGroupInternal(admin, group, subscribedGroupIDs), group.Name)
	}
}
