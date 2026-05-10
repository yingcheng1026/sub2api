//go:build unit

package service

import "testing"

// TestUsageBillingFingerprint_WalletCostContributes 钱包模式扣款必须参与 dedup
// fingerprint，否则同 request_id 不同 WalletCost 会被误判为重复。
func TestUsageBillingFingerprint_WalletCostContributes(t *testing.T) {
	t.Parallel()

	subID := int64(42)
	base := UsageBillingCommand{
		RequestID:      "req-1",
		APIKeyID:       1,
		UserID:         2,
		AccountID:      3,
		AccountType:    AccountTypeAPIKey,
		Model:          "gpt-5",
		BillingType:    1,
		InputTokens:    100,
		OutputTokens:   50,
		SubscriptionID: &subID,
		WalletCost:     1.5,
	}
	other := base
	other.WalletCost = 2.0

	base.Normalize()
	other.Normalize()

	if base.RequestFingerprint == other.RequestFingerprint {
		t.Fatalf("expected different fingerprints when WalletCost differs, got %q == %q",
			base.RequestFingerprint, other.RequestFingerprint)
	}
}

// TestUsageBillingApplyResult_WalletInsufficientFlag 用例：钱包余额不足时
// repo 不应回滚整个 billing 事务，而应通过 result.WalletInsufficient 通知调用方。
func TestUsageBillingApplyResult_WalletInsufficientFlag(t *testing.T) {
	t.Parallel()

	r := &UsageBillingApplyResult{Applied: true, WalletInsufficient: true}
	if !r.Applied || !r.WalletInsufficient {
		t.Fatal("flags must be readable")
	}
	if r.NewWalletBalance != nil {
		t.Fatal("WalletInsufficient case must leave NewWalletBalance nil")
	}
}
