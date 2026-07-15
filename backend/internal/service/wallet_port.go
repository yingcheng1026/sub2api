package service

import (
	"context"
	"net/http"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

// WalletLedgerReason 流水原因枚举（与 SQL chk_wallet_ledger_reason 约束一致）。
const (
	WalletLedgerReasonActivation = "activation"
	WalletLedgerReasonUsage      = "usage"
	WalletLedgerReasonRefund     = "refund"
	WalletLedgerReasonAdjustment = "adjustment"
	WalletLedgerReasonExpiration = "expiration"
	// WalletLedgerReasonTopup 额度卡叠加 (B2.4)：用户已有 active 钱包订阅时
	// 再买一张额度卡，quota 合入现有钱包，wallet_balance_usd 和 wallet_initial_usd
	// 双双 +delta。migration 154 已扩 CHECK。
	WalletLedgerReasonTopup = "topup"
)

// 钱包模式 (v4) 错误码。
//
// ErrWalletInsufficient 余额不足，扣费失败。映射到 HTTP 402 Payment Required；
// 网关层捕获后给客户端返回 402 + hint 文案，详见 design §2.3。
//
// ErrWalletNotFound 订阅不存在或不是钱包模式（wallet_balance_usd IS NULL）。
//
// ErrWalletNegativeDelta DeductBalance 入参 costUSD ≤ 0；防止误把扣费当退款。
var (
	ErrWalletInsufficient  = infraerrors.New(http.StatusPaymentRequired, "WALLET_INSUFFICIENT", "wallet balance insufficient, please renew at /dashboard")
	ErrWalletAdmissionBusy = infraerrors.TooManyRequests("WALLET_ADMISSION_BUSY", "another wallet request is still being settled; please retry shortly")
	ErrWalletNotFound      = infraerrors.NotFound("WALLET_NOT_FOUND", "wallet subscription not found or not wallet-mode")
	ErrWalletNegativeDelta = infraerrors.BadRequest("WALLET_NEGATIVE_DELTA", "deduct/credit amount must be > 0")
)

// WalletLedgerEntry 钱包流水落库后回读的记录（也用于审计接口）。
type WalletLedgerEntry struct {
	ID             int64
	SubscriptionID int64
	DeltaUSD       float64
	BalanceAfter   float64
	Reason         string
	PaymentOrderID *int64
	UsageLogID     *int64
	OperatorID     *int64
	Notes          string
}

// WalletDeductCommand 扣款入参。
//
// 在事务中执行：
//  1. SELECT wallet_balance_usd FROM user_subscriptions WHERE id=$1 FOR UPDATE
//  2. UsageLogID 非空且已有 usage 流水时返回原流水，不重复扣款
//  3. 如 balance < CostUSD → 回滚返 ErrWalletInsufficient
//  4. UPDATE wallet_balance_usd -= CostUSD
//  5. INSERT subscription_wallet_ledger(reason='usage', delta=-CostUSD, usage_log_id=UsageLogID)
type WalletDeductCommand struct {
	SubscriptionID int64
	CostUSD        float64
	UsageLogID     *int64
	// PostpaidSettlement is set only after an upstream response has already been
	// delivered. In that case the full actual cost must be ledgered even when it
	// takes the wallet negative; ordinary/preflight deductions still fail closed.
	PostpaidSettlement bool
}

// WalletAdjustCommand admin 调整钱包余额（手动退款 / 补偿 / 补充）。
// DeltaUSD > 0 → 加余额；< 0 → 扣余额；= 0 视为参数错误。
// Reason 必须是 WalletLedgerReason* 之一。
type WalletAdjustCommand struct {
	SubscriptionID int64
	DeltaUSD       float64
	Reason         string
	OperatorID     *int64
	Notes          string
}

// WalletActivationCommand records the opening balance of a newly created
// wallet. The subscription row already contains that balance, so activation
// appends the authoritative ledger entry without updating the balance again.
type WalletActivationCommand struct {
	SubscriptionID int64
	InitialUSD     float64
	PaymentOrderID *int64
	OperatorID     *int64
	Notes          string
}

// WalletTopupCommand 额度卡叠加充值入参 (B2.4)。
//
// 在事务中执行：
//  1. SELECT wallet_balance_usd, wallet_initial_usd FROM user_subscriptions
//     WHERE id=$1 FOR UPDATE（must be a wallet-mode subscription）
//  2. UPDATE wallet_balance_usd += DeltaUSD
//     UPDATE wallet_initial_usd += DeltaUSD
//  3. INSERT subscription_wallet_ledger(reason='topup', delta=+DeltaUSD)
//
// DeltaUSD 必须 > 0；非钱包模式订阅返 ErrWalletNotFound。
type WalletTopupCommand struct {
	SubscriptionID int64
	DeltaUSD       float64
	PaymentOrderID *int64
	OperatorID     *int64
	Notes          string
}

// WalletReconcileDrift 对账发现的单条漂移记录。
type WalletReconcileDrift struct {
	SubscriptionID int64
	Cached         float64
	LedgerSum      float64
	Drift          float64
}

// WalletRepository 钱包扣款/审计仓储端口。
//
// 关键不变量：
//   - 所有方法都在自己开的事务里跑，调用方不需要事先 BeginTx。
//   - Deduct 必须使用 SELECT ... FOR UPDATE 锁住 user_subscriptions 行，
//     防止同 user 多 key 并发把余额扣穿。
//   - 同一个非空 UsageLogID 只能产生一条 reason=usage 流水；重放必须返回
//     首次已提交的流水，且不能再次修改余额。UsageLogID=nil 保留逐次扣款语义。
//   - Usage settlement may take the wallet negative after an already delivered
//     response. Pre-upstream admission limits that exposure to one in-flight
//     request per wallet; subsequent requests fail closed until settlement.
type WalletRepository interface {
	// Deduct 钱包扣款。余额不足返 ErrWalletInsufficient。
	Deduct(ctx context.Context, cmd WalletDeductCommand) (WalletLedgerEntry, error)

	// RecordActivation appends exactly one reason=activation entry and must not
	// add InitialUSD to wallet_balance_usd a second time.
	RecordActivation(ctx context.Context, cmd WalletActivationCommand) (WalletLedgerEntry, error)

	// Adjust 余额调整（含初始充值）。
	Adjust(ctx context.Context, cmd WalletAdjustCommand) (WalletLedgerEntry, error)

	// Topup 额度卡叠加 (B2.4)：同时把 wallet_balance_usd 和 wallet_initial_usd
	// +DeltaUSD，写一条 reason='topup' 流水。区别于 Adjust（只动 balance）。
	Topup(ctx context.Context, cmd WalletTopupCommand) (WalletLedgerEntry, error)

	// ListLedger 查询订阅的流水（最近 N 条，倒序）。
	ListLedger(ctx context.Context, subscriptionID int64, limit int) ([]WalletLedgerEntry, error)

	// ReconcileBalances 比对所有钱包模式订阅的 cached wallet_balance_usd 与
	// ledger 累计 SUM(delta_usd)；返回漂移 > tolerance 的条目。tolerance 推荐
	// 0.01（DB CHECK 约束允许的浮点抖动）。
	ReconcileBalances(ctx context.Context, tolerance float64) ([]WalletReconcileDrift, error)
}
