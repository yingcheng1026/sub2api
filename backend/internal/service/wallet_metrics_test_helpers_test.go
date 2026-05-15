//go:build unit

package service

// resetWalletMetricsForTest 单测重置计数器。
func resetWalletMetricsForTest() {
	walletInsufficientTotal.Store(0)
	walletBalanceLowTotal.Store(0)
	walletLedgerDriftTotal.Store(0)
}
