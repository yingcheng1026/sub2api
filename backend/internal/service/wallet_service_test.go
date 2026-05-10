package service

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

type fakeWalletRepo struct {
	deductCalls  []WalletDeductCommand
	adjustCalls  []WalletAdjustCommand
	deductResult WalletLedgerEntry
	deductErr    error
	adjustResult WalletLedgerEntry
	adjustErr    error
}

func (f *fakeWalletRepo) Deduct(_ context.Context, cmd WalletDeductCommand) (WalletLedgerEntry, error) {
	f.deductCalls = append(f.deductCalls, cmd)
	return f.deductResult, f.deductErr
}

func (f *fakeWalletRepo) Adjust(_ context.Context, cmd WalletAdjustCommand) (WalletLedgerEntry, error) {
	f.adjustCalls = append(f.adjustCalls, cmd)
	return f.adjustResult, f.adjustErr
}

func (f *fakeWalletRepo) ListLedger(_ context.Context, _ int64, _ int) ([]WalletLedgerEntry, error) {
	return nil, nil
}

func (f *fakeWalletRepo) ReconcileBalances(_ context.Context, _ float64) ([]WalletReconcileDrift, error) {
	return nil, nil
}

func TestWalletService_Deduct_RejectsNonPositive(t *testing.T) {
	repo := &fakeWalletRepo{}
	svc := NewWalletService(repo)

	_, err := svc.Deduct(context.Background(), 1, 0, nil)
	require.ErrorIs(t, err, ErrWalletNegativeDelta)

	_, err = svc.Deduct(context.Background(), 1, -1, nil)
	require.ErrorIs(t, err, ErrWalletNegativeDelta)

	require.Empty(t, repo.deductCalls, "non-positive deduct must short-circuit")
}

func TestWalletService_Deduct_PassesThroughInsufficient(t *testing.T) {
	repo := &fakeWalletRepo{deductErr: ErrWalletInsufficient}
	svc := NewWalletService(repo)

	_, err := svc.Deduct(context.Background(), 1, 5, nil)
	require.ErrorIs(t, err, ErrWalletInsufficient)
	require.Len(t, repo.deductCalls, 1)
}

func TestWalletService_Deduct_ForwardsUsageLogID(t *testing.T) {
	repo := &fakeWalletRepo{deductResult: WalletLedgerEntry{ID: 7}}
	svc := NewWalletService(repo)

	logID := int64(123)
	entry, err := svc.Deduct(context.Background(), 1, 5, &logID)
	require.NoError(t, err)
	require.Equal(t, int64(7), entry.ID)
	require.Len(t, repo.deductCalls, 1)
	require.NotNil(t, repo.deductCalls[0].UsageLogID)
	require.Equal(t, int64(123), *repo.deductCalls[0].UsageLogID)
}

func TestWalletService_Activate_WritesActivationReason(t *testing.T) {
	repo := &fakeWalletRepo{adjustResult: WalletLedgerEntry{ID: 11}}
	svc := NewWalletService(repo)

	op := int64(99)
	entry, err := svc.Activate(context.Background(), 1, 1500, &op, "  initial recharge  ")
	require.NoError(t, err)
	require.Equal(t, int64(11), entry.ID)
	require.Len(t, repo.adjustCalls, 1)
	got := repo.adjustCalls[0]
	require.Equal(t, "activation", got.Reason)
	require.Equal(t, 1500.0, got.DeltaUSD)
	require.Equal(t, "initial recharge", got.Notes, "notes must be trimmed")
}

func TestWalletService_Activate_RejectsNonPositive(t *testing.T) {
	repo := &fakeWalletRepo{}
	svc := NewWalletService(repo)
	_, err := svc.Activate(context.Background(), 1, 0, nil, "")
	require.ErrorIs(t, err, ErrWalletNegativeDelta)
	require.Empty(t, repo.adjustCalls)
}

func TestWalletService_Adjust_RejectsInvalidReason(t *testing.T) {
	repo := &fakeWalletRepo{}
	svc := NewWalletService(repo)
	_, err := svc.Adjust(context.Background(), WalletAdjustCommand{
		SubscriptionID: 1,
		DeltaUSD:       5,
		Reason:         "bogus",
	})
	require.ErrorIs(t, err, ErrWalletNegativeDelta)
	require.Empty(t, repo.adjustCalls)
}

func TestWalletService_Adjust_RejectsZeroDelta(t *testing.T) {
	repo := &fakeWalletRepo{}
	svc := NewWalletService(repo)
	_, err := svc.Adjust(context.Background(), WalletAdjustCommand{
		SubscriptionID: 1,
		DeltaUSD:       0,
		Reason:         WalletLedgerReasonAdjustment,
	})
	require.ErrorIs(t, err, ErrWalletNegativeDelta)
	require.Empty(t, repo.adjustCalls)
}

func TestWalletService_Adjust_PassesThroughRepoError(t *testing.T) {
	stub := errors.New("db down")
	repo := &fakeWalletRepo{adjustErr: stub}
	svc := NewWalletService(repo)
	_, err := svc.Adjust(context.Background(), WalletAdjustCommand{
		SubscriptionID: 1,
		DeltaUSD:       5,
		Reason:         WalletLedgerReasonAdjustment,
	})
	require.ErrorIs(t, err, stub)
}
