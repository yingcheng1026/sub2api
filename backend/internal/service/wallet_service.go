package service

import (
	"context"
	"strings"
)

// WalletService 钱包模式 (v4) 服务。
// 薄封装：业务参数校验 + 调 WalletRepository。
//
// 职责边界：
//   - 不负责激活流程（在 SubscriptionActivationService 内调 Adjust(reason=activation)）
//   - 不负责调 BillingService 算 actualCost（gateway 算好后直接传 CostUSD）
//   - 不负责返 HTTP 402（gateway 中间件根据 ErrWalletInsufficient 映射）
type WalletService struct {
	repo WalletRepository
}

func NewWalletService(repo WalletRepository) *WalletService {
	return &WalletService{repo: repo}
}

// Deduct 钱包扣款。CostUSD 必须 > 0；余额不足返 ErrWalletInsufficient。
func (s *WalletService) Deduct(ctx context.Context, subscriptionID int64, costUSD float64, usageLogID *int64) (WalletLedgerEntry, error) {
	if costUSD <= 0 {
		return WalletLedgerEntry{}, ErrWalletNegativeDelta
	}
	return s.repo.Deduct(ctx, WalletDeductCommand{
		SubscriptionID: subscriptionID,
		CostUSD:        costUSD,
		UsageLogID:     usageLogID,
	})
}

// Activate 激活时写一条 reason=activation 的流水。
// 假设 user_subscriptions 行已经先一步在事务里建好且 wallet_balance_usd = initialUSD。
// 本方法仅追加 ledger 记录，不再动余额（balance_after = initialUSD）。
func (s *WalletService) Activate(ctx context.Context, subscriptionID int64, initialUSD float64, operatorID *int64, notes string) (WalletLedgerEntry, error) {
	if initialUSD <= 0 {
		return WalletLedgerEntry{}, ErrWalletNegativeDelta
	}
	return s.repo.Adjust(ctx, WalletAdjustCommand{
		SubscriptionID: subscriptionID,
		DeltaUSD:       initialUSD,
		Reason:         WalletLedgerReasonActivation,
		OperatorID:     operatorID,
		Notes:          strings.TrimSpace(notes),
	})
}

// Adjust 管理员调整。DeltaUSD ≠ 0；Reason 必须合法。
func (s *WalletService) Adjust(ctx context.Context, cmd WalletAdjustCommand) (WalletLedgerEntry, error) {
	if cmd.DeltaUSD == 0 {
		return WalletLedgerEntry{}, ErrWalletNegativeDelta
	}
	if !isValidWalletLedgerReason(cmd.Reason) {
		return WalletLedgerEntry{}, ErrWalletNegativeDelta
	}
	cmd.Notes = strings.TrimSpace(cmd.Notes)
	return s.repo.Adjust(ctx, cmd)
}

// Topup 额度卡叠加充值 (B2.4)。同时 +balance 和 +initial，写一条 reason='topup' 流水。
// DeltaUSD 必须 > 0；非钱包模式订阅返 ErrWalletNotFound。
func (s *WalletService) Topup(ctx context.Context, subscriptionID int64, deltaUSD float64, operatorID *int64, notes string) (WalletLedgerEntry, error) {
	if deltaUSD <= 0 {
		return WalletLedgerEntry{}, ErrWalletNegativeDelta
	}
	return s.repo.Topup(ctx, WalletTopupCommand{
		SubscriptionID: subscriptionID,
		DeltaUSD:       deltaUSD,
		OperatorID:     operatorID,
		Notes:          strings.TrimSpace(notes),
	})
}

// ListLedger 查最近 limit 条流水。limit ≤ 0 默认 50；> 500 截断到 500。
func (s *WalletService) ListLedger(ctx context.Context, subscriptionID int64, limit int) ([]WalletLedgerEntry, error) {
	if limit <= 0 {
		limit = 50
	}
	if limit > 500 {
		limit = 500
	}
	return s.repo.ListLedger(ctx, subscriptionID, limit)
}

func isValidWalletLedgerReason(reason string) bool {
	switch reason {
	case WalletLedgerReasonActivation,
		WalletLedgerReasonUsage,
		WalletLedgerReasonRefund,
		WalletLedgerReasonAdjustment,
		WalletLedgerReasonExpiration,
		WalletLedgerReasonTopup:
		return true
	}
	return false
}
