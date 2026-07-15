package service

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"entgo.io/ent/dialect"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/paymentorder"
	"github.com/Wei-Shaw/sub2api/ent/paymentproviderinstance"
	"github.com/Wei-Shaw/sub2api/internal/payment"
	"github.com/Wei-Shaw/sub2api/internal/payment/provider"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

// validateProviderConfig runs the provider's constructor to surface config-level
// errors at save time (e.g. wxpay missing certSerial), instead of only failing
// when an order is created. Returns the structured ApplicationError from the
// constructor so the frontend i18n layer can localize it.
//
// Only validates enabled instances — a disabled instance may be a half-filled
// draft the admin will complete later.
func (s *PaymentConfigService) validateProviderConfig(providerKey string, config map[string]string) error {
	_, err := provider.CreateProvider(providerKey, "_validate_", config)
	return err
}

// --- Provider Instance CRUD ---

func (s *PaymentConfigService) ListProviderInstances(ctx context.Context) ([]*dbent.PaymentProviderInstance, error) {
	return s.entClient.PaymentProviderInstance.Query().Order(paymentproviderinstance.BySortOrder()).All(ctx)
}

// ProviderInstanceResponse is the API response for a provider instance.
type ProviderInstanceResponse struct {
	ID              int64             `json:"id"`
	ProviderKey     string            `json:"provider_key"`
	Name            string            `json:"name"`
	Config          map[string]string `json:"config"`
	SupportedTypes  []string          `json:"supported_types"`
	Limits          string            `json:"limits"`
	Enabled         bool              `json:"enabled"`
	RefundEnabled   bool              `json:"refund_enabled"`
	AllowUserRefund bool              `json:"allow_user_refund"`
	SortOrder       int               `json:"sort_order"`
	PaymentMode     string            `json:"payment_mode"`
}

// ListProviderInstancesWithConfig returns provider instances with decrypted config.
func (s *PaymentConfigService) ListProviderInstancesWithConfig(ctx context.Context) ([]ProviderInstanceResponse, error) {
	instances, err := s.entClient.PaymentProviderInstance.Query().
		Order(paymentproviderinstance.BySortOrder()).All(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]ProviderInstanceResponse, 0, len(instances))
	for _, inst := range instances {
		resp := ProviderInstanceResponse{
			ID: int64(inst.ID), ProviderKey: inst.ProviderKey, Name: inst.Name,
			SupportedTypes: splitTypes(inst.SupportedTypes), Limits: inst.Limits,
			Enabled: inst.Enabled, RefundEnabled: inst.RefundEnabled, AllowUserRefund: inst.AllowUserRefund,
			SortOrder: inst.SortOrder, PaymentMode: inst.PaymentMode,
		}
		resp.Config, err = s.decryptAndMaskConfig(inst.ProviderKey, inst.Config)
		if err != nil {
			return nil, fmt.Errorf("decrypt config for instance %d: %w", inst.ID, err)
		}
		result = append(result, resp)
	}
	return result, nil
}

// decryptAndMaskConfig returns the stored config with sensitive fields omitted.
// Admin UIs display masked placeholders for these; the raw values never leave
// the server. Callers that need the full config (e.g. payment runtime) must
// use decryptConfig directly.
func (s *PaymentConfigService) decryptAndMaskConfig(providerKey, encrypted string) (map[string]string, error) {
	cfg, err := s.decryptConfig(encrypted)
	if err != nil {
		return nil, err
	}
	if cfg == nil {
		return nil, nil
	}
	masked := make(map[string]string, len(cfg))
	for k, v := range cfg {
		if isSensitiveProviderConfigField(providerKey, k) {
			continue
		}
		masked[k] = v
	}
	return masked, nil
}

// pendingOrderStatuses are order statuses considered "in progress".
var pendingOrderStatuses = []string{
	payment.OrderStatusPending,
	payment.OrderStatusPaid,
	payment.OrderStatusRecharging,
}

// providerSettlementProtectedOrderStatuses includes every state that can still
// accept trusted provider payment evidence. Rotating or deleting the pinned
// provider while one of these orders exists can make a real late payment
// unverifiable or unfulfillable.
var providerSettlementProtectedOrderStatuses = []string{
	payment.OrderStatusPending,
	payment.OrderStatusPaid,
	payment.OrderStatusRecharging,
	payment.OrderStatusFailed,
	payment.OrderStatusCancelled,
	payment.OrderStatusExpired,
}

// providerSensitiveConfigFields is the authoritative list of config keys that
// are treated as secrets per provider. Must stay in sync with the frontend
// definition at frontend/src/components/payment/providerConfig.ts
// (PROVIDER_CONFIG_FIELDS, fields with sensitive: true).
//
// Key matching is case-insensitive. Non-listed keys (e.g. appId, notifyUrl,
// stripe publishableKey) are returned in plaintext by the admin GET API.
var providerSensitiveConfigFields = map[string]map[string]struct{}{
	payment.TypeEasyPay: {"pkey": {}},
	payment.TypeAlipay:  {"privatekey": {}, "publickey": {}, "alipaypublickey": {}},
	payment.TypeWxpay:   {"privatekey": {}, "apiv3key": {}, "publickey": {}},
	payment.TypeStripe:  {"secretkey": {}, "webhooksecret": {}},
}

// providerPendingOrderProtectedConfigFields lists config keys that cannot be
// changed while the instance has in-progress orders. This includes secrets plus
// all provider identity fields that are snapshotted into orders or used by
// webhook/refund verification.
var providerPendingOrderProtectedConfigFields = map[string]map[string]struct{}{
	payment.TypeEasyPay: {"pkey": {}, "pid": {}},
	payment.TypeAlipay:  {"privatekey": {}, "publickey": {}, "alipaypublickey": {}, "appid": {}},
	payment.TypeWxpay:   {"privatekey": {}, "apiv3key": {}, "publickey": {}, "appid": {}, "mpappid": {}, "mchid": {}, "publickeyid": {}, "certserial": {}},
	payment.TypeStripe:  {"secretkey": {}, "webhooksecret": {}},
}

func isSensitiveProviderConfigField(providerKey, fieldName string) bool {
	fields, ok := providerSensitiveConfigFields[providerKey]
	if !ok {
		return false
	}
	_, found := fields[strings.ToLower(fieldName)]
	return found
}

func hasPendingOrderProtectedConfigChange(providerKey string, currentConfig, nextConfig map[string]string) bool {
	fields, ok := providerPendingOrderProtectedConfigFields[providerKey]
	if !ok {
		return false
	}
	for fieldName := range fields {
		if providerConfigFieldValue(currentConfig, fieldName) != providerConfigFieldValue(nextConfig, fieldName) {
			return true
		}
	}
	return false
}

func providerConfigFieldValue(config map[string]string, fieldName string) string {
	for key, value := range config {
		if strings.EqualFold(key, fieldName) {
			return value
		}
	}
	return ""
}

// validateEasyPayEndpointSecretBinding prevents an endpoint-only patch from
// silently reusing the stored merchant key against a newly selected server.
// The admin must explicitly submit the canonical pkey field whenever apiBase
// changes; masked or blank values retain the old key and do not qualify.
func validateEasyPayEndpointSecretBinding(providerKey string, currentConfig, patchConfig, nextConfig map[string]string) error {
	if providerKey != payment.TypeEasyPay {
		return nil
	}
	currentBase := strings.TrimRight(strings.TrimSpace(providerConfigFieldValue(currentConfig, "apiBase")), "/")
	nextBase := strings.TrimRight(strings.TrimSpace(providerConfigFieldValue(nextConfig, "apiBase")), "/")
	if currentBase == nextBase {
		return nil
	}
	if strings.TrimSpace(patchConfig["pkey"]) == "" {
		return infraerrors.BadRequest(
			"EASYPAY_PKEY_REENTRY_REQUIRED",
			"easypay pkey must be re-entered when apiBase changes",
		)
	}
	return nil
}

func countProviderSettlementProtectedOrders(ctx context.Context, client *dbent.Client, providerInstanceID int64) (int, error) {
	if client == nil {
		return 0, fmt.Errorf("provider order protection requires a database client")
	}
	return client.PaymentOrder.Query().
		Where(
			paymentorder.ProviderInstanceIDEQ(strconv.FormatInt(providerInstanceID, 10)),
			paymentorder.StatusIn(providerSettlementProtectedOrderStatuses...),
		).Count(ctx)
}

// lockPaymentProviderMutation serializes admin mutation with order admission.
// CreateOrder locks the same provider row before inserting its reservation, so
// the protected-order count and the following mutation form one atomic gate.
func lockPaymentProviderMutation(ctx context.Context, client *dbent.Client, providerInstanceID int64) error {
	if client == nil || providerInstanceID <= 0 {
		return fmt.Errorf("provider mutation requires a valid instance")
	}
	query := client.PaymentProviderInstance.Query().Where(
		paymentproviderinstance.IDEQ(providerInstanceID),
	)
	if client.Driver().Dialect() == dialect.Postgres {
		query = query.ForUpdate()
	}
	if _, err := query.OnlyID(ctx); err != nil {
		return fmt.Errorf("lock payment provider mutation: %w", err)
	}
	return nil
}

func (s *PaymentConfigService) countPendingOrdersByPlan(ctx context.Context, planID int64) (int, error) {
	return s.entClient.PaymentOrder.Query().
		Where(
			paymentorder.PlanIDEQ(planID),
			paymentorder.StatusIn(pendingOrderStatuses...),
		).Count(ctx)
}

var validProviderKeys = map[string]bool{
	payment.TypeEasyPay: true, payment.TypeAlipay: true, payment.TypeWxpay: true, payment.TypeStripe: true,
}

func (s *PaymentConfigService) CreateProviderInstance(ctx context.Context, req CreateProviderInstanceRequest) (*dbent.PaymentProviderInstance, error) {
	typesStr := joinTypes(req.SupportedTypes)
	if err := validateProviderRequest(req.ProviderKey, req.Name, typesStr); err != nil {
		return nil, err
	}
	if err := validateProviderLimits(req.ProviderKey, typesStr, req.Limits); err != nil {
		return nil, err
	}
	if err := s.validateVisibleMethodEnablementConflicts(ctx, 0, req.ProviderKey, typesStr, req.Enabled); err != nil {
		return nil, err
	}
	if req.Enabled {
		if err := s.validateProviderConfig(req.ProviderKey, req.Config); err != nil {
			return nil, err
		}
	}
	enc, err := s.encryptConfig(req.Config)
	if err != nil {
		return nil, err
	}
	allowUserRefund := req.AllowUserRefund && req.RefundEnabled
	return s.entClient.PaymentProviderInstance.Create().
		SetProviderKey(req.ProviderKey).SetName(req.Name).SetConfig(enc).
		SetSupportedTypes(typesStr).SetEnabled(req.Enabled).SetPaymentMode(req.PaymentMode).
		SetSortOrder(req.SortOrder).SetLimits(req.Limits).SetRefundEnabled(req.RefundEnabled).
		SetAllowUserRefund(allowUserRefund).
		Save(ctx)
}

func validateProviderRequest(providerKey, name, supportedTypes string) error {
	if strings.TrimSpace(name) == "" {
		return infraerrors.BadRequest("VALIDATION_ERROR", "provider name is required")
	}
	if !validProviderKeys[providerKey] {
		return infraerrors.BadRequest("VALIDATION_ERROR", fmt.Sprintf("invalid provider key: %s", providerKey))
	}
	// supported_types can be empty (provider accepts no payment types until configured)
	return nil
}

func validateProviderLimits(providerKey, supportedTypes, limits string) error {
	if err := payment.ValidateInstanceLimits(limits, providerKey, supportedTypes); err != nil {
		return infraerrors.BadRequest("INVALID_PAYMENT_LIMITS", err.Error())
	}
	return nil
}

// UpdateProviderInstance updates a provider instance by ID (patch semantics).
// NOTE: This function exceeds 30 lines due to per-field nil-check patch update
// boilerplate and settlement-protected order safety checks.
func (s *PaymentConfigService) UpdateProviderInstance(ctx context.Context, id int64, req UpdateProviderInstanceRequest) (*dbent.PaymentProviderInstance, error) {
	tx, err := s.entClient.Tx(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin provider update: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	client := tx.Client()
	if err := lockPaymentProviderMutation(ctx, client, id); err != nil {
		return nil, err
	}
	current, err := client.PaymentProviderInstance.Get(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("load provider instance: %w", err)
	}
	var pendingOrderCount *int
	getPendingOrderCount := func() (int, error) {
		if pendingOrderCount != nil {
			return *pendingOrderCount, nil
		}
		count, err := countProviderSettlementProtectedOrders(ctx, client, id)
		if err != nil {
			return 0, fmt.Errorf("check pending orders: %w", err)
		}
		pendingOrderCount = &count
		return count, nil
	}
	nextEnabled := current.Enabled
	if req.Enabled != nil {
		nextEnabled = *req.Enabled
	}
	nextSupportedTypes := current.SupportedTypes
	if req.SupportedTypes != nil {
		nextSupportedTypes = joinTypes(req.SupportedTypes)
	}
	nextLimits := current.Limits
	if req.Limits != nil {
		nextLimits = *req.Limits
	}
	if err := validateProviderLimits(current.ProviderKey, nextSupportedTypes, nextLimits); err != nil {
		return nil, err
	}
	if err := s.validateVisibleMethodEnablementConflicts(ctx, id, current.ProviderKey, nextSupportedTypes, nextEnabled); err != nil {
		return nil, err
	}
	var mergedConfig map[string]string
	if req.Config != nil {
		currentConfig, err := s.decryptConfig(current.Config)
		if err != nil {
			return nil, fmt.Errorf("decrypt existing config: %w", err)
		}
		mergedConfig = mergeProviderConfig(current.ProviderKey, currentConfig, req.Config)
		if err := validateEasyPayEndpointSecretBinding(current.ProviderKey, currentConfig, req.Config, mergedConfig); err != nil {
			return nil, err
		}
		if hasPendingOrderProtectedConfigChange(current.ProviderKey, currentConfig, mergedConfig) {
			count, err := getPendingOrderCount()
			if err != nil {
				return nil, err
			}
			if count > 0 {
				return nil, infraerrors.Conflict("PENDING_ORDERS", "instance has settlement-protected orders").
					WithMetadata(map[string]string{"count": strconv.Itoa(count)})
			}
		}
	}
	if req.Enabled != nil && !*req.Enabled {
		count, err := getPendingOrderCount()
		if err != nil {
			return nil, err
		}
		if count > 0 {
			return nil, infraerrors.Conflict("PENDING_ORDERS", "instance has settlement-protected orders").
				WithMetadata(map[string]string{"count": strconv.Itoa(count)})
		}
	}
	// Validate merged config when the instance will end up enabled.
	// This surfaces provider-level errors (e.g. wxpay missing certSerial) at save time,
	// so admins see them in the dialog instead of only when an order is created.
	finalEnabled := current.Enabled
	if req.Enabled != nil {
		finalEnabled = *req.Enabled
	}
	if finalEnabled {
		configToValidate := mergedConfig
		if configToValidate == nil {
			configToValidate, err = s.decryptConfig(current.Config)
			if err != nil {
				return nil, fmt.Errorf("decrypt existing config: %w", err)
			}
		}
		if err := s.validateProviderConfig(current.ProviderKey, configToValidate); err != nil {
			return nil, err
		}
	}
	u := client.PaymentProviderInstance.UpdateOneID(id)
	if req.Name != nil {
		u.SetName(*req.Name)
	}
	if mergedConfig != nil {
		enc, err := s.encryptConfig(mergedConfig)
		if err != nil {
			return nil, err
		}
		u.SetConfig(enc)
	}
	if req.SupportedTypes != nil {
		// Check settlement-protected orders before removing payment types.
		count, err := getPendingOrderCount()
		if err != nil {
			return nil, err
		}
		if count > 0 {
			// Load current instance to compare types
			oldTypes := strings.Split(current.SupportedTypes, ",")
			newTypes := req.SupportedTypes
			for _, ot := range oldTypes {
				ot = strings.TrimSpace(ot)
				if ot == "" {
					continue
				}
				found := false
				for _, nt := range newTypes {
					if strings.TrimSpace(nt) == ot {
						found = true
						break
					}
				}
				if !found {
					return nil, infraerrors.Conflict("PENDING_ORDERS", "cannot remove payment types while instance has settlement-protected orders").
						WithMetadata(map[string]string{"count": strconv.Itoa(count)})
				}
			}
		}
		u.SetSupportedTypes(joinTypes(req.SupportedTypes))
	}
	if req.Enabled != nil {
		u.SetEnabled(*req.Enabled)
	}
	if req.SortOrder != nil {
		u.SetSortOrder(*req.SortOrder)
	}
	if req.Limits != nil {
		u.SetLimits(*req.Limits)
	}
	if req.RefundEnabled != nil {
		u.SetRefundEnabled(*req.RefundEnabled)
		// Cascade: turning off refund_enabled also disables allow_user_refund
		if !*req.RefundEnabled {
			u.SetAllowUserRefund(false)
		}
	}
	if req.AllowUserRefund != nil {
		// Only allow enabling when refund_enabled is (or will be) true
		if *req.AllowUserRefund {
			refundEnabled := false
			if req.RefundEnabled != nil {
				refundEnabled = *req.RefundEnabled
			} else {
				refundEnabled = current.RefundEnabled
			}
			if refundEnabled {
				u.SetAllowUserRefund(true)
			}
		} else {
			u.SetAllowUserRefund(false)
		}
	}
	if req.PaymentMode != nil {
		u.SetPaymentMode(*req.PaymentMode)
	}
	updated, err := u.Save(ctx)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit provider update: %w", err)
	}
	return updated.Unwrap(), nil
}

// GetUserRefundEligibleInstanceIDs returns provider instance IDs that allow user refund.
func (s *PaymentConfigService) GetUserRefundEligibleInstanceIDs(ctx context.Context) ([]string, error) {
	instances, err := s.entClient.PaymentProviderInstance.Query().
		Where(
			paymentproviderinstance.RefundEnabledEQ(true),
			paymentproviderinstance.AllowUserRefundEQ(true),
		).Select(paymentproviderinstance.FieldID).All(ctx)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(instances))
	for _, inst := range instances {
		ids = append(ids, strconv.FormatInt(int64(inst.ID), 10))
	}
	return ids, nil
}

func mergeProviderConfig(providerKey string, existing, newConfig map[string]string) map[string]string {
	merged := make(map[string]string, len(existing)+len(newConfig))
	for key, value := range existing {
		merged[key] = value
	}
	for k, v := range newConfig {
		// Preserve existing secrets when the client submits an empty value
		// (admin UI omits the value to indicate "leave unchanged").
		if v == "" && isSensitiveProviderConfigField(providerKey, k) {
			continue
		}
		merged[k] = v
	}
	return merged
}

func (s *PaymentConfigService) decryptConfig(stored string) (map[string]string, error) {
	cfg, _, err := payment.DecryptProviderConfig(stored, s.encryptionKey)
	return cfg, err
}

// MigrateProviderConfigsToEncrypted rewrites legacy plaintext or shared-root
// provider configs before the HTTP server starts. The compare-and-swap prevents
// overwriting a concurrent admin change; any unreadable row fails startup.
func (s *PaymentConfigService) MigrateProviderConfigsToEncrypted(ctx context.Context, legacyKeys ...[]byte) (int, error) {
	if s == nil || s.entClient == nil {
		return 0, fmt.Errorf("payment provider config migration requires a database client")
	}
	instances, err := s.entClient.PaymentProviderInstance.Query().All(ctx)
	if err != nil {
		return 0, fmt.Errorf("list payment provider configs: %w", err)
	}
	migrated := 0
	for _, instance := range instances {
		if instance.Config == "" {
			continue
		}
		cfg, needsRewrite, err := decryptProviderConfigForMigration(
			instance.Config,
			s.encryptionKey,
			legacyKeys,
		)
		if err != nil {
			return migrated, fmt.Errorf("provider instance %d config is unreadable: %w", instance.ID, err)
		}
		if !needsRewrite {
			continue
		}
		encrypted, err := payment.EncryptProviderConfig(cfg, s.encryptionKey)
		if err != nil {
			return migrated, fmt.Errorf("encrypt provider instance %d config: %w", instance.ID, err)
		}
		updated, err := s.entClient.PaymentProviderInstance.Update().
			Where(
				paymentproviderinstance.IDEQ(instance.ID),
				paymentproviderinstance.ConfigEQ(instance.Config),
			).
			SetConfig(encrypted).
			Save(ctx)
		if err != nil {
			return migrated, fmt.Errorf("persist encrypted provider instance %d config: %w", instance.ID, err)
		}
		if updated != 1 {
			latest, getErr := s.entClient.PaymentProviderInstance.Get(ctx, instance.ID)
			if getErr != nil {
				return migrated, fmt.Errorf("reload concurrently migrated provider instance %d: %w", instance.ID, getErr)
			}
			_, stillPlaintext, decryptErr := payment.DecryptProviderConfig(latest.Config, s.encryptionKey)
			if decryptErr != nil || stillPlaintext {
				return migrated, fmt.Errorf("provider instance %d changed concurrently but is not valid encrypted config", instance.ID)
			}
			continue
		}
		migrated++
	}
	return migrated, nil
}

func decryptProviderConfigForMigration(stored string, currentKey []byte, legacyKeys [][]byte) (map[string]string, bool, error) {
	cfg, legacyPlaintext, currentErr := payment.DecryptProviderConfig(stored, currentKey)
	if currentErr == nil {
		return cfg, legacyPlaintext, nil
	}
	for _, legacyKey := range legacyKeys {
		if len(legacyKey) != payment.AES256KeySize {
			continue
		}
		cfg, _, legacyErr := payment.DecryptProviderConfig(stored, legacyKey)
		if legacyErr == nil {
			return cfg, true, nil
		}
	}
	return nil, false, currentErr
}

func (s *PaymentConfigService) DeleteProviderInstance(ctx context.Context, id int64) error {
	tx, err := s.entClient.Tx(ctx)
	if err != nil {
		return fmt.Errorf("begin provider delete: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	client := tx.Client()
	if err := lockPaymentProviderMutation(ctx, client, id); err != nil {
		return err
	}
	count, err := countProviderSettlementProtectedOrders(ctx, client, id)
	if err != nil {
		return fmt.Errorf("check pending orders: %w", err)
	}
	if count > 0 {
		return infraerrors.Conflict("PENDING_ORDERS",
			fmt.Sprintf("this instance has %d settlement-protected orders and cannot be deleted", count))
	}
	if err := client.PaymentProviderInstance.DeleteOneID(id).Exec(ctx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit provider delete: %w", err)
	}
	return nil
}

func (s *PaymentConfigService) encryptConfig(cfg map[string]string) (string, error) {
	return payment.EncryptProviderConfig(cfg, s.encryptionKey)
}
