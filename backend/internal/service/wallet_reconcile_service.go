package service

import (
	"context"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"go.uber.org/zap"
)

// WalletReconcileService 周期性比对 user_subscriptions.wallet_balance_usd
// 与 subscription_wallet_ledger.SUM(delta_usd) 的一致性。任何漂移 > tolerance
// 都告警 + 累计到 walletLedgerDriftTotal 指标，由 ops 决定后续动作（人工
// Adjust 修正或回滚）。
//
// 设计同 SubscriptionExpiryService：单 ticker、Start/Stop、首次启动立即跑一次。
// 间隔 5 分钟够用——钱包扣款是热路径，但 ledger 写入与 balance 更新在同一事务里，
// 漂移只可能源于直接 SQL 改动（admin 手贱）或代码 bug，5 分钟足够发现。
type WalletReconcileService struct {
	repo      WalletRepository
	interval  time.Duration
	tolerance float64
	stopCh    chan struct{}
	stopOnce  sync.Once
	wg        sync.WaitGroup
}

const walletReconcileTimeout = 30 * time.Second

// NewWalletReconcileService 创建对账服务。interval ≤ 0 → 永不跑（用于测试 /
// 关闭功能）；tolerance ≤ 0 → 默认 $0.01。
func NewWalletReconcileService(repo WalletRepository, interval time.Duration, tolerance float64) *WalletReconcileService {
	return &WalletReconcileService{
		repo:      repo,
		interval:  interval,
		tolerance: tolerance,
		stopCh:    make(chan struct{}),
	}
}

func (s *WalletReconcileService) Start() {
	if s == nil || s.repo == nil || s.interval <= 0 {
		return
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		ticker := time.NewTicker(s.interval)
		defer ticker.Stop()

		s.runOnce()
		for {
			select {
			case <-ticker.C:
				s.runOnce()
			case <-s.stopCh:
				return
			}
		}
	}()
}

func (s *WalletReconcileService) Stop() {
	if s == nil {
		return
	}
	s.stopOnce.Do(func() {
		close(s.stopCh)
	})
	s.wg.Wait()
}

// runOnce 执行一次对账；外部测试也走这个入口。
func (s *WalletReconcileService) runOnce() {
	ctx, cancel := context.WithTimeout(context.Background(), walletReconcileTimeout)
	defer cancel()

	drifts, err := s.repo.ReconcileBalances(ctx, s.tolerance)
	if err != nil {
		logger.L().With(
			zap.String("component", "service.wallet_reconcile"),
			zap.Error(err),
		).Warn("wallet.reconcile_query_failed")
		return
	}
	if len(drifts) == 0 {
		return
	}
	recordWalletLedgerDrift(int64(len(drifts)))
	for _, d := range drifts {
		logger.L().With(
			zap.String("component", "service.wallet_reconcile"),
			zap.Int64("subscription_id", d.SubscriptionID),
			zap.Float64("cached_balance_usd", d.Cached),
			zap.Float64("ledger_sum_usd", d.LedgerSum),
			zap.Float64("drift_usd", d.Drift),
		).Error("wallet.reconcile_drift_detected")
	}
}
