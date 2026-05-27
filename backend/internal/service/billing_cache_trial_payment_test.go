//go:build unit

package service

import (
	"errors"
	"testing"
)

func TestCheckTrialBonusPaymentBinding(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		user      *User
		group     *Group
		subData   *subscriptionCacheData
		wantError bool
	}{
		{
			name:    "non trial group passes",
			user:    &User{ID: 1, Role: RoleUser},
			group:   &Group{ID: 1, Name: "standard"},
			subData: &subscriptionCacheData{MonthlyUsage: 10},
		},
		{
			name:    "trial group below threshold passes",
			user:    &User{ID: 1, Role: RoleUser},
			group:   &Group{ID: trialBonusGroupID},
			subData: &subscriptionCacheData{MonthlyUsage: trialBonusPaymentBindingThresholdUSD - 0.01},
		},
		{
			name:      "trial group at threshold requires payment binding",
			user:      &User{ID: 1, Role: RoleUser},
			group:     &Group{ID: trialBonusGroupID},
			subData:   &subscriptionCacheData{MonthlyUsage: trialBonusPaymentBindingThresholdUSD},
			wantError: true,
		},
		{
			name:      "trial group by name requires payment binding",
			user:      &User{ID: 1, Role: RoleUser},
			group:     &Group{ID: 99, Name: trialBonusGroupName},
			subData:   &subscriptionCacheData{WeeklyUsage: trialBonusPaymentBindingThresholdUSD},
			wantError: true,
		},
		{
			name:    "positive recharge is accepted as binding proof",
			user:    &User{ID: 1, Role: RoleUser, TotalRecharged: 0.01},
			group:   &Group{ID: trialBonusGroupID},
			subData: &subscriptionCacheData{MonthlyUsage: trialBonusPaymentBindingThresholdUSD},
		},
		{
			name:    "admin role passes",
			user:    &User{ID: 1, Role: RoleAdmin},
			group:   &Group{ID: trialBonusGroupID},
			subData: &subscriptionCacheData{MonthlyUsage: trialBonusPaymentBindingThresholdUSD},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := checkTrialBonusPaymentBinding(tt.user, tt.group, tt.subData)
			if tt.wantError {
				if !errors.Is(err, ErrTrialPaymentBindingRequired) {
					t.Fatalf("expected ErrTrialPaymentBindingRequired, got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("expected nil err, got %v", err)
			}
		})
	}
}

func TestMaxSubscriptionUsageUSD(t *testing.T) {
	t.Parallel()

	got := maxSubscriptionUsageUSD(&subscriptionCacheData{
		DailyUsage:   1,
		WeeklyUsage:  2,
		MonthlyUsage: 3,
	})
	if got != 3 {
		t.Fatalf("expected monthly max usage 3, got %v", got)
	}
}
