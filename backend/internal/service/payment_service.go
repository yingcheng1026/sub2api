package service

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/paymentproviderinstance"
	"github.com/Wei-Shaw/sub2api/internal/payment"
	"github.com/Wei-Shaw/sub2api/internal/payment/provider"
)

// --- Order Status Constants ---

const (
	OrderStatusPending           = payment.OrderStatusPending
	OrderStatusPaid              = payment.OrderStatusPaid
	OrderStatusRecharging        = payment.OrderStatusRecharging
	OrderStatusCompleted         = payment.OrderStatusCompleted
	OrderStatusExpired           = payment.OrderStatusExpired
	OrderStatusCancelled         = payment.OrderStatusCancelled
	OrderStatusFailed            = payment.OrderStatusFailed
	OrderStatusRefundRequested   = payment.OrderStatusRefundRequested
	OrderStatusRefunding         = payment.OrderStatusRefunding
	OrderStatusPartiallyRefunded = payment.OrderStatusPartiallyRefunded
	OrderStatusRefunded          = payment.OrderStatusRefunded
	OrderStatusRefundFailed      = payment.OrderStatusRefundFailed
)

const (
	// defaultMaxPendingOrders and defaultOrderTimeoutMin are defined in
	// payment_config_service.go alongside other payment configuration defaults.
	// paymentGraceMinutes protects recent expired/cancelled rows from plan or
	// group deletion while settlement is still likely. Trusted paid evidence is
	// accepted after this window too; late fulfillment may then require admin
	// remediation if its historical target was removed.
	paymentGraceMinutes = 5
	// Read-only status endpoints may reconcile a missed webhook, but must not
	// hammer the provider on every UI poll.
	paymentStatusQueryCooldown = 5 * time.Second

	defaultPageSize    = 20
	maxPageSize        = 100
	topUsersLimit      = 10
	amountToleranceCNY = 0.01

	// A RECHARGING row is a lightweight fulfillment lease. Fulfillment is
	// expected to be local database work; after this window another worker may
	// safely reclaim it using a compare-and-swap on status + updated_at.
	paymentFulfillmentLeaseTimeout = 5 * time.Minute
	defaultStaleRecoveryBatchSize  = 100
	maxStaleRecoveryBatchSize      = 500
	paymentCreateFailureReason     = "payment_create_failed"
	fulfillmentFailureReasonPrefix = "fulfillment_failed: "

	orderIDPrefix = "sub2_"
)

const (
	paymentResumeSigningKeyEnv        = "PAYMENT_RESUME_SIGNING_KEY"
	paymentResumeLegacyVerifyUntilEnv = "PAYMENT_RESUME_LEGACY_VERIFY_UNTIL"
	paymentResumeLegacyMaxWindow      = 24 * time.Hour
)

// --- Types ---

// generateOutTradeNo creates a 128-bit, non-guessable provider order ID. The
// 32 hexadecimal characters fit WeChat Pay's strict out_trade_no limit.
func generateOutTradeNo() (string, error) {
	randomBytes := make([]byte, 16)
	if _, err := rand.Read(randomBytes); err != nil {
		return "", fmt.Errorf("generate out_trade_no randomness: %w", err)
	}
	return hex.EncodeToString(randomBytes), nil
}

type CreateOrderRequest struct {
	UserID             int64
	ResumeTokenUserID  int64
	ResumeTokenJTI     string
	Amount             float64
	PaymentType        string
	OpenID             string
	ClientIP           string
	IsMobile           bool
	IsWeChatBrowser    bool
	SrcHost            string
	SrcURL             string
	ReturnURL          string
	TrustedFrontendURL string
	PaymentSource      string
	OrderType          string
	PlanID             int64
}

type CreateOrderResponse struct {
	OrderID      int64                           `json:"order_id"`
	Amount       float64                         `json:"amount"`
	PayAmount    float64                         `json:"pay_amount"`
	FeeRate      float64                         `json:"fee_rate"`
	Status       string                          `json:"status"`
	ResultType   payment.CreatePaymentResultType `json:"result_type,omitempty"`
	PaymentType  string                          `json:"payment_type"`
	OutTradeNo   string                          `json:"out_trade_no,omitempty"`
	PayURL       string                          `json:"pay_url,omitempty"`
	QRCode       string                          `json:"qr_code,omitempty"`
	ClientSecret string                          `json:"client_secret,omitempty"`
	OAuth        *payment.WechatOAuthInfo        `json:"oauth,omitempty"`
	JSAPI        *payment.WechatJSAPIPayload     `json:"jsapi,omitempty"`
	JSAPIPayload *payment.WechatJSAPIPayload     `json:"jsapi_payload,omitempty"`
	ExpiresAt    time.Time                       `json:"expires_at"`
	PaymentMode  string                          `json:"payment_mode,omitempty"`
	ResumeToken  string                          `json:"resume_token,omitempty"`
}

type OrderListParams struct {
	Page        int
	PageSize    int
	Status      string
	OrderType   string
	PaymentType string
	Keyword     string
}

type RefundPlan struct {
	OrderID                  int64
	Order                    *dbent.PaymentOrder
	RefundAmount             float64
	GatewayAmount            float64
	Reason                   string
	Force                    bool
	DeductBalance            bool
	DeductionType            string
	BalanceToDeduct          float64
	SubDaysToDeduct          int
	SubscriptionID           int64
	WalletCreditToDeduct     float64
	WalletFulfillmentAuditID int64
	WalletFulfilledAt        time.Time
	RefundLeaseUpdatedAt     time.Time
	WalletRefundRecovered    bool
}

type RefundResult struct {
	Success              bool    `json:"success"`
	Warning              string  `json:"warning,omitempty"`
	RequireForce         bool    `json:"require_force,omitempty"`
	BalanceDeducted      float64 `json:"balance_deducted,omitempty"`
	SubDaysDeducted      int     `json:"subscription_days_deducted,omitempty"`
	WalletCreditDeducted float64 `json:"wallet_credit_deducted,omitempty"`
}

type DashboardStats struct {
	TodayAmount   float64 `json:"today_amount"`
	TotalAmount   float64 `json:"total_amount"`
	TodayCount    int     `json:"today_count"`
	TotalCount    int     `json:"total_count"`
	AvgAmount     float64 `json:"avg_amount"`
	PendingOrders int     `json:"pending_orders"`

	DailySeries    []DailyStats        `json:"daily_series"`
	PaymentMethods []PaymentMethodStat `json:"payment_methods"`
	TopUsers       []TopUserStat       `json:"top_users"`
}

type DailyStats struct {
	Date   string  `json:"date"`
	Amount float64 `json:"amount"`
	Count  int     `json:"count"`
}

type PaymentMethodStat struct {
	Type   string  `json:"type"`
	Amount float64 `json:"amount"`
	Count  int     `json:"count"`
}

type TopUserStat struct {
	UserID int64   `json:"user_id"`
	Email  string  `json:"email"`
	Amount float64 `json:"amount"`
}

// --- Service ---

type PaymentService struct {
	providerMu       sync.Mutex
	statusQueryMu    sync.Mutex
	lastStatusQuery  map[int64]time.Time
	providersLoaded  bool
	entClient        *dbent.Client
	registry         *payment.Registry
	loadBalancer     payment.LoadBalancer
	redeemService    *RedeemService
	subscriptionSvc  *SubscriptionService
	configService    *PaymentConfigService
	userRepo         UserRepository
	groupRepo        GroupRepository
	resumeService    *PaymentResumeService
	affiliateService *AffiliateService
}

func NewPaymentService(entClient *dbent.Client, registry *payment.Registry, loadBalancer payment.LoadBalancer, redeemService *RedeemService, subscriptionSvc *SubscriptionService, configService *PaymentConfigService, userRepo UserRepository, groupRepo GroupRepository, affiliateService *AffiliateService) *PaymentService {
	svc := &PaymentService{entClient: entClient, registry: registry, loadBalancer: newVisibleMethodLoadBalancer(loadBalancer, configService), redeemService: redeemService, subscriptionSvc: subscriptionSvc, configService: configService, userRepo: userRepo, groupRepo: groupRepo, affiliateService: affiliateService}
	svc.resumeService = psNewPaymentResumeService(configService)
	return svc
}

// --- Provider Registry ---

// EnsureProviders lazily initializes the provider registry on first call.
func (s *PaymentService) EnsureProviders(ctx context.Context) {
	s.providerMu.Lock()
	defer s.providerMu.Unlock()
	if !s.providersLoaded {
		s.loadProviders(ctx)
		s.providersLoaded = true
	}
}

// RefreshProviders clears and re-registers all providers from the database.
func (s *PaymentService) RefreshProviders(ctx context.Context) {
	s.providerMu.Lock()
	defer s.providerMu.Unlock()
	s.registry.Clear()
	s.loadProviders(ctx)
	s.providersLoaded = true
}

func (s *PaymentService) loadProviders(ctx context.Context) {
	instances, err := s.entClient.PaymentProviderInstance.Query().
		Where(paymentproviderinstance.EnabledEQ(true)).
		All(ctx)
	if err != nil {
		slog.Error("[PaymentService] failed to query provider instances", "error", err)
		return
	}
	for _, inst := range instances {
		cfg, err := s.loadBalancer.GetInstanceConfig(ctx, int64(inst.ID))
		if err != nil {
			slog.Warn("[PaymentService] failed to decrypt config for instance", "instanceID", inst.ID, "error", err)
			continue
		}
		if inst.PaymentMode != "" {
			cfg["paymentMode"] = inst.PaymentMode
		}
		instID := fmt.Sprintf("%d", inst.ID)
		p, err := provider.CreateProvider(inst.ProviderKey, instID, cfg)
		if err != nil {
			slog.Warn("[PaymentService] failed to create provider for instance", "instanceID", inst.ID, "key", inst.ProviderKey, "error", err)
			continue
		}
		s.registry.Register(p)
	}
}

// --- Helpers ---

func psIsRefundStatus(s string) bool {
	switch s {
	case OrderStatusRefundRequested, OrderStatusRefunding, OrderStatusPartiallyRefunded, OrderStatusRefunded, OrderStatusRefundFailed:
		return true
	}
	return false
}

func psErrMsg(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func psNilIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func (s *PaymentService) paymentResume() *PaymentResumeService {
	if s.resumeService != nil {
		return s.resumeService
	}
	return psNewPaymentResumeService(s.configService)
}

func NewLegacyAwarePaymentResumeService(legacyKey []byte) *PaymentResumeService {
	return newLegacyAwarePaymentResumeService(legacyKey)
}

func psNewPaymentResumeService(configService *PaymentConfigService) *PaymentResumeService {
	return newLegacyAwarePaymentResumeService(psResumeLegacyVerificationKey(configService))
}

func newLegacyAwarePaymentResumeService(legacyKey []byte) *PaymentResumeService {
	signingKey, verifyFallbacks, err := resolvePaymentResumeSigningKeys(legacyKey, time.Now())
	if err != nil {
		slog.Error("payment resume signing configuration is invalid", "error", err)
		return NewPaymentResumeService(nil)
	}
	return NewPaymentResumeService(signingKey, verifyFallbacks...)
}

func psResumeLegacyVerificationKey(configService *PaymentConfigService) []byte {
	if configService == nil {
		return nil
	}
	return configService.encryptionKey
}

func resolvePaymentResumeSigningKeys(legacyKey []byte, now time.Time) ([]byte, [][]byte, error) {
	signingKey, err := parsePaymentResumeSigningKey(os.Getenv(paymentResumeSigningKeyEnv))
	if err != nil {
		return nil, nil, err
	}
	if len(signingKey) == 0 {
		if len(legacyKey) == 0 {
			return nil, nil, nil
		}
		if len(legacyKey) < paymentResumeMinSigningKeyBytes {
			return nil, nil, fmt.Errorf("legacy payment resume key must be at least %d bytes", paymentResumeMinSigningKeyBytes)
		}
		return legacyKey, nil, nil
	}
	if len(legacyKey) == 0 || bytes.Equal(legacyKey, signingKey) {
		return signingKey, nil, nil
	}
	legacyAllowed, err := paymentResumeLegacyVerificationAllowed(os.Getenv(paymentResumeLegacyVerifyUntilEnv), now)
	if err != nil {
		return nil, nil, err
	}
	if !legacyAllowed {
		return signingKey, nil, nil
	}
	if len(legacyKey) < paymentResumeMinSigningKeyBytes {
		return nil, nil, fmt.Errorf("legacy payment resume verification key must be at least %d bytes", paymentResumeMinSigningKeyBytes)
	}
	return signingKey, [][]byte{legacyKey}, nil
}

func parsePaymentResumeSigningKey(raw string) ([]byte, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	if len(raw) >= 64 && len(raw)%2 == 0 {
		if decoded, err := hex.DecodeString(raw); err == nil {
			if len(decoded) < paymentResumeMinSigningKeyBytes {
				return nil, fmt.Errorf("payment resume signing key must be at least %d bytes", paymentResumeMinSigningKeyBytes)
			}
			return decoded, nil
		}
	}
	if len([]byte(raw)) < paymentResumeMinSigningKeyBytes {
		return nil, fmt.Errorf("payment resume signing key must be at least %d bytes", paymentResumeMinSigningKeyBytes)
	}
	return []byte(raw), nil
}

func paymentResumeLegacyVerificationAllowed(raw string, now time.Time) (bool, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return false, nil
	}
	deadline, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return false, fmt.Errorf("%s must be an RFC3339 timestamp", paymentResumeLegacyVerifyUntilEnv)
	}
	if !deadline.After(now) {
		return false, nil
	}
	if deadline.After(now.Add(paymentResumeLegacyMaxWindow)) {
		return false, fmt.Errorf("%s must be no more than %s in the future", paymentResumeLegacyVerifyUntilEnv, paymentResumeLegacyMaxWindow)
	}
	return true, nil
}

func psSliceContains(sl []string, s string) bool {
	for _, v := range sl {
		if v == s {
			return true
		}
	}
	return false
}

// Subscription validity period unit constants.
const (
	validityUnitWeek  = "week"
	validityUnitMonth = "month"
)

func psComputeValidityDays(days int, unit string) int {
	switch unit {
	case validityUnitWeek:
		return days * 7
	case validityUnitMonth:
		return days * 30
	default:
		return days
	}
}

func psStartOfDayUTC(t time.Time) time.Time {
	y, m, d := t.UTC().Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

func applyPagination(pageSize, page int) (size, pg int) {
	size = pageSize
	if size <= 0 {
		size = defaultPageSize
	}
	if size > maxPageSize {
		size = maxPageSize
	}
	pg = page
	if pg < 1 {
		pg = 1
	}
	return size, pg
}
