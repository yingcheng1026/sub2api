//go:build unit

package service

import (
	"errors"
	"testing"
)

// TestCheckWalletEligibility v4 钱包模式预检：余额 > 0 放行，<=0 直接
// ErrWalletInsufficient（gateway_handler 映射 HTTP 402）。
func TestCheckWalletEligibility(t *testing.T) {
	t.Parallel()

	pos := 1.5
	zero := 0.0
	neg := -0.01

	tests := []struct {
		name    string
		sub     *UserSubscription
		wantErr error
	}{
		{name: "nil subscription is no-op", sub: nil, wantErr: nil},
		{name: "non-wallet subscription is no-op", sub: &UserSubscription{ID: 1}, wantErr: nil},
		{name: "positive balance passes", sub: &UserSubscription{ID: 1, WalletBalanceUSD: &pos}, wantErr: nil},
		{name: "zero balance is 402", sub: &UserSubscription{ID: 1, WalletBalanceUSD: &zero}, wantErr: ErrWalletInsufficient},
		{name: "negative balance is 402", sub: &UserSubscription{ID: 1, WalletBalanceUSD: &neg}, wantErr: ErrWalletInsufficient},
	}

	s := &BillingCacheService{}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := s.checkWalletEligibility(tt.sub)
			if tt.wantErr == nil {
				if err != nil {
					t.Fatalf("expected nil err, got %v", err)
				}
				return
			}
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("expected %v, got %v", tt.wantErr, err)
			}
		})
	}
}
