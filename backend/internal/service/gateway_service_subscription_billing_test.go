//go:build unit

package service

import (
	"testing"
)

// TestBuildUsageBillingCommand_SubscriptionAppliesRateMultiplier locks in the fix
// that subscription-mode billing honours the group (and any user-specific) rate
// multiplier — i.e. cmd.SubscriptionCost tracks ActualCost (= TotalCost *
// RateMultiplier), not raw TotalCost.
func TestBuildUsageBillingCommand_SubscriptionAppliesRateMultiplier(t *testing.T) {
	t.Parallel()

	groupID := int64(7)
	subID := int64(42)

	tests := []struct {
		name           string
		totalCost      float64
		actualCost     float64
		isSubscription bool
		wantSub        float64
		wantBalance    float64
	}{
		{
			name:           "subscription with 2x multiplier consumes 2x quota",
			totalCost:      1.0,
			actualCost:     2.0,
			isSubscription: true,
			wantSub:        2.0,
			wantBalance:    0,
		},
		{
			name:           "subscription with 0.5x multiplier consumes 0.5x quota",
			totalCost:      1.0,
			actualCost:     0.5,
			isSubscription: true,
			wantSub:        0.5,
			wantBalance:    0,
		},
		{
			name:           "free subscription (multiplier 0) consumes no quota",
			totalCost:      1.0,
			actualCost:     0,
			isSubscription: true,
			wantSub:        0,
			wantBalance:    0,
		},
		{
			name:           "balance billing keeps using ActualCost (regression)",
			totalCost:      1.0,
			actualCost:     2.0,
			isSubscription: false,
			wantSub:        0,
			wantBalance:    2.0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			p := &postUsageBillingParams{
				Cost:               &CostBreakdown{TotalCost: tt.totalCost, ActualCost: tt.actualCost},
				User:               &User{ID: 1},
				APIKey:             &APIKey{ID: 2, GroupID: &groupID},
				Account:            &Account{ID: 3},
				Subscription:       &UserSubscription{ID: subID},
				IsSubscriptionBill: tt.isSubscription,
			}

			cmd := buildUsageBillingCommand("req-1", nil, p)
			if cmd == nil {
				t.Fatal("buildUsageBillingCommand returned nil")
			}
			if cmd.SubscriptionCost != tt.wantSub {
				t.Errorf("SubscriptionCost = %v, want %v", cmd.SubscriptionCost, tt.wantSub)
			}
			if cmd.BalanceCost != tt.wantBalance {
				t.Errorf("BalanceCost = %v, want %v", cmd.BalanceCost, tt.wantBalance)
			}
		})
	}
}

// TestBuildUsageBillingCommand_WalletModeRoutesToWalletCost 锁定 v4 钱包订阅
// 走 cmd.WalletCost（而不是 SubscriptionCost）— 这俩字段在事务内必须互斥。
func TestBuildUsageBillingCommand_WalletModeRoutesToWalletCost(t *testing.T) {
	t.Parallel()

	groupID := int64(7)
	subID := int64(42)
	walletBal := 100.0

	t.Run("wallet mode subscription deducts WalletCost", func(t *testing.T) {
		t.Parallel()
		p := &postUsageBillingParams{
			Cost:   &CostBreakdown{TotalCost: 1.0, ActualCost: 2.5},
			User:   &User{ID: 1},
			APIKey: &APIKey{ID: 2, GroupID: &groupID},
			Account: &Account{ID: 3},
			Subscription: &UserSubscription{
				ID: subID,
				// WalletBalanceUSD != nil → IsWalletMode() == true
				WalletBalanceUSD: &walletBal,
			},
			IsSubscriptionBill: true,
		}

		cmd := buildUsageBillingCommand("req-w1", nil, p)
		if cmd == nil {
			t.Fatal("buildUsageBillingCommand returned nil")
		}
		if cmd.WalletCost != 2.5 {
			t.Errorf("WalletCost = %v, want 2.5", cmd.WalletCost)
		}
		if cmd.SubscriptionCost != 0 {
			t.Errorf("SubscriptionCost must be 0 in wallet mode, got %v", cmd.SubscriptionCost)
		}
		if cmd.BalanceCost != 0 {
			t.Errorf("BalanceCost must be 0 in wallet mode, got %v", cmd.BalanceCost)
		}
		if cmd.SubscriptionID == nil || *cmd.SubscriptionID != subID {
			t.Errorf("SubscriptionID must be set to %d, got %v", subID, cmd.SubscriptionID)
		}
	})

	t.Run("v3 group subscription still uses SubscriptionCost", func(t *testing.T) {
		t.Parallel()
		p := &postUsageBillingParams{
			Cost:    &CostBreakdown{TotalCost: 1.0, ActualCost: 2.5},
			User:    &User{ID: 1},
			APIKey:  &APIKey{ID: 2, GroupID: &groupID},
			Account: &Account{ID: 3},
			Subscription: &UserSubscription{
				ID:      subID,
				GroupID: &groupID,
				// WalletBalanceUSD == nil → IsWalletMode() == false
			},
			IsSubscriptionBill: true,
		}

		cmd := buildUsageBillingCommand("req-w2", nil, p)
		if cmd.SubscriptionCost != 2.5 {
			t.Errorf("SubscriptionCost = %v, want 2.5", cmd.SubscriptionCost)
		}
		if cmd.WalletCost != 0 {
			t.Errorf("WalletCost must be 0 in v3 mode, got %v", cmd.WalletCost)
		}
	})
}
