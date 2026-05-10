package service

import "sync/atomic"

// 钱包模式 (v4) 运行时指标。原则同 OpenAI WS v2：
// 不引 prometheus 客户端，atomic 计数器 + 周期日志即可，前端 / ops 通过
// SnapshotWalletMetrics 拉快照。
//
// 三个核心信号：
//   - WalletInsufficientTotal  — 用户撞 402 的次数（gateway pre-check 或
//     billing 后置阶段命中 ErrWalletInsufficient 都会 +1）
//   - WalletBalanceLowTotal    — 预检命中「余额低于阈值」的次数，用于
//     提醒前端弹「即将耗尽」横幅；阈值由 walletBalanceLowThresholdUSD 控制
//   - WalletLedgerDriftTotal   — 对账 cron 发现 cached vs ledger SUM 漂移
//     >$0.01 的订阅条数；任何 >0 都该告警

const walletBalanceLowThresholdUSD = 150.0

var (
	walletInsufficientTotal atomic.Int64
	walletBalanceLowTotal   atomic.Int64
	walletLedgerDriftTotal  atomic.Int64
)

// WalletMetricsSnapshot 钱包指标快照。
type WalletMetricsSnapshot struct {
	InsufficientTotal int64 `json:"insufficient_total"`
	BalanceLowTotal   int64 `json:"balance_low_total"`
	LedgerDriftTotal  int64 `json:"ledger_drift_total"`
}

// SnapshotWalletMetrics 返回当前钱包指标快照。
func SnapshotWalletMetrics() WalletMetricsSnapshot {
	return WalletMetricsSnapshot{
		InsufficientTotal: walletInsufficientTotal.Load(),
		BalanceLowTotal:   walletBalanceLowTotal.Load(),
		LedgerDriftTotal:  walletLedgerDriftTotal.Load(),
	}
}

// IncWalletInsufficientTotal handler 层 HTTP 402 边界统一调用一次。
// 暴露给 handler 包；service 内部 (checkWalletEligibility) 不再单独 +1，
// 避免双计。
func IncWalletInsufficientTotal() {
	walletInsufficientTotal.Add(1)
}

// recordWalletBalanceLow 用户预检通过但余额已经低于阈值时调用。
func recordWalletBalanceLow() {
	walletBalanceLowTotal.Add(1)
}

// recordWalletLedgerDrift 对账 cron 每发现一条漂移订阅 +1。
func recordWalletLedgerDrift(n int64) {
	if n <= 0 {
		return
	}
	walletLedgerDriftTotal.Add(n)
}

// resetWalletMetricsForTest 仅供单测重置计数器；非测试代码不要调用。
func resetWalletMetricsForTest() {
	walletInsufficientTotal.Store(0)
	walletBalanceLowTotal.Store(0)
	walletLedgerDriftTotal.Store(0)
}
