//go:build unit

package service

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"strconv"
	"strings"
	"testing"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/payment"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPaymentProviderConfigsPersistEncryptedAndMigrateLegacyPlaintext(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	key := []byte("0123456789abcdef0123456789abcdef")
	svc := &PaymentConfigService{entClient: client, encryptionKey: key}

	created, err := svc.CreateProviderInstance(ctx, CreateProviderInstanceRequest{
		ProviderKey:    payment.TypeStripe,
		Name:           "encrypted-stripe",
		Config:         map[string]string{"secretKey": "sk-live-do-not-store-plain", "publishableKey": "pk-live"},
		SupportedTypes: []string{payment.TypeStripe},
		Enabled:        false,
	})
	require.NoError(t, err)
	require.NotNil(t, created)
	saved, err := client.PaymentProviderInstance.Get(ctx, created.ID)
	require.NoError(t, err)
	require.False(t, json.Valid([]byte(saved.Config)))
	require.NotContains(t, saved.Config, "sk-live-do-not-store-plain")
	decoded, err := svc.decryptConfig(saved.Config)
	require.NoError(t, err)
	require.Equal(t, "sk-live-do-not-store-plain", decoded["secretKey"])

	legacySecret := "legacy-provider-secret"
	legacy, err := client.PaymentProviderInstance.Create().
		SetProviderKey(payment.TypeEasyPay).
		SetName("legacy-plaintext").
		SetConfig(`{"pid":"legacy","pkey":"` + legacySecret + `"}`).
		SetSupportedTypes(payment.TypeAlipay).
		Save(ctx)
	require.NoError(t, err)

	migrated, err := svc.MigrateProviderConfigsToEncrypted(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, migrated)
	migratedSaved, err := client.PaymentProviderInstance.Get(ctx, legacy.ID)
	require.NoError(t, err)
	require.False(t, json.Valid([]byte(migratedSaved.Config)))
	require.False(t, strings.Contains(migratedSaved.Config, legacySecret))
	decoded, err = svc.decryptConfig(migratedSaved.Config)
	require.NoError(t, err)
	require.Equal(t, legacySecret, decoded["pkey"])

	migrated, err = svc.MigrateProviderConfigsToEncrypted(ctx)
	require.NoError(t, err)
	require.Zero(t, migrated)
}

func TestPaymentProviderConfigMigrationFailsClosed(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	_, err := client.PaymentProviderInstance.Create().
		SetProviderKey(payment.TypeEasyPay).
		SetName("corrupted-config").
		SetConfig("not-json-or-valid-ciphertext").
		Save(ctx)
	require.NoError(t, err)

	svc := &PaymentConfigService{
		entClient:     client,
		encryptionKey: []byte("0123456789abcdef0123456789abcdef"),
	}
	_, err = svc.MigrateProviderConfigsToEncrypted(ctx)
	require.Error(t, err)
	require.Contains(t, err.Error(), "provider instance")
}

func TestPaymentProviderConfigMigrationRotatesSharedRootCiphertext(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	legacyKey := []byte("legacy-shared-root-32-byte-key!!")
	currentKey := []byte("current-payment-root-32-byte-key")
	legacyCiphertext, err := payment.EncryptProviderConfig(
		map[string]string{"secretKey": "legacy-provider-secret"},
		legacyKey,
	)
	require.NoError(t, err)
	instance, err := client.PaymentProviderInstance.Create().
		SetProviderKey(payment.TypeStripe).
		SetName("legacy-shared-root").
		SetConfig(legacyCiphertext).
		Save(ctx)
	require.NoError(t, err)

	svc := &PaymentConfigService{entClient: client, encryptionKey: currentKey}
	_, err = svc.MigrateProviderConfigsToEncrypted(ctx)
	require.Error(t, err)
	migrated, err := svc.MigrateProviderConfigsToEncrypted(ctx, legacyKey)
	require.NoError(t, err)
	require.Equal(t, 1, migrated)
	refreshed, err := client.PaymentProviderInstance.Get(ctx, instance.ID)
	require.NoError(t, err)
	decoded, _, err := payment.DecryptProviderConfig(refreshed.Config, currentKey)
	require.NoError(t, err)
	require.Equal(t, "legacy-provider-secret", decoded["secretKey"])
	_, _, err = payment.DecryptProviderConfig(refreshed.Config, legacyKey)
	require.Error(t, err)
}

func TestValidateProviderRequest(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		providerKey    string
		providerName   string
		supportedTypes string
		wantErr        bool
		errContains    string
	}{
		{
			name:           "valid easypay with types",
			providerKey:    "easypay",
			providerName:   "MyProvider",
			supportedTypes: "alipay,wxpay",
			wantErr:        false,
		},
		{
			name:           "valid stripe with empty types",
			providerKey:    "stripe",
			providerName:   "Stripe Provider",
			supportedTypes: "",
			wantErr:        false,
		},
		{
			name:           "valid alipay provider",
			providerKey:    "alipay",
			providerName:   "Alipay Direct",
			supportedTypes: "alipay",
			wantErr:        false,
		},
		{
			name:           "valid wxpay provider",
			providerKey:    "wxpay",
			providerName:   "WeChat Pay",
			supportedTypes: "wxpay",
			wantErr:        false,
		},
		{
			name:           "invalid provider key",
			providerKey:    "invalid",
			providerName:   "Name",
			supportedTypes: "alipay",
			wantErr:        true,
			errContains:    "invalid provider key",
		},
		{
			name:           "empty name",
			providerKey:    "easypay",
			providerName:   "",
			supportedTypes: "alipay",
			wantErr:        true,
			errContains:    "provider name is required",
		},
		{
			name:           "whitespace-only name",
			providerKey:    "easypay",
			providerName:   "  ",
			supportedTypes: "alipay",
			wantErr:        true,
			errContains:    "provider name is required",
		},
		{
			name:           "tab-only name",
			providerKey:    "easypay",
			providerName:   "\t",
			supportedTypes: "alipay",
			wantErr:        true,
			errContains:    "provider name is required",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := validateProviderRequest(tc.providerKey, tc.providerName, tc.supportedTypes)
			if tc.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.errContains)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestIsSensitiveProviderConfigField(t *testing.T) {
	t.Parallel()

	tests := []struct {
		providerKey string
		field       string
		wantSen     bool
	}{
		// Stripe: publishableKey is public, only secretKey/webhookSecret are secrets
		{"stripe", "secretKey", true},
		{"stripe", "webhookSecret", true},
		{"stripe", "SecretKey", true}, // case-insensitive
		{"stripe", "publishableKey", false},
		{"stripe", "appId", false},

		// Alipay
		{"alipay", "privateKey", true},
		{"alipay", "publicKey", true},
		{"alipay", "alipayPublicKey", true},
		{"alipay", "appId", false},
		{"alipay", "notifyUrl", false},

		// Wxpay
		{"wxpay", "privateKey", true},
		{"wxpay", "apiV3Key", true},
		{"wxpay", "publicKey", true},
		{"wxpay", "publicKeyId", false},
		{"wxpay", "certSerial", false},
		{"wxpay", "mchId", false},

		// EasyPay
		{"easypay", "pkey", true},
		{"easypay", "pid", false},
		{"easypay", "apiBase", false},

		// Unknown provider: never sensitive
		{"unknown", "secretKey", false},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.providerKey+"/"+tc.field, func(t *testing.T) {
			t.Parallel()

			got := isSensitiveProviderConfigField(tc.providerKey, tc.field)
			assert.Equal(t, tc.wantSen, got, "isSensitiveProviderConfigField(%q, %q)", tc.providerKey, tc.field)
		})
	}
}

func TestJoinTypes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input []string
		want  string
	}{
		{
			name:  "multiple types",
			input: []string{"alipay", "wxpay"},
			want:  "alipay,wxpay",
		},
		{
			name:  "single type",
			input: []string{"stripe"},
			want:  "stripe",
		},
		{
			name:  "empty slice",
			input: []string{},
			want:  "",
		},
		{
			name:  "nil slice",
			input: nil,
			want:  "",
		},
		{
			name:  "three types",
			input: []string{"alipay", "wxpay", "stripe"},
			want:  "alipay,wxpay,stripe",
		},
		{
			name:  "types with spaces are not trimmed",
			input: []string{" alipay ", " wxpay "},
			want:  " alipay , wxpay ",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := joinTypes(tc.input)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestCreateProviderInstanceAllowsVisibleMethodProvidersFromDifferentSources(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	svc := &PaymentConfigService{
		entClient:     client,
		encryptionKey: []byte("0123456789abcdef0123456789abcdef"),
	}

	_, err := svc.CreateProviderInstance(ctx, CreateProviderInstanceRequest{
		ProviderKey: "easypay",
		Name:        "EasyPay Alipay",
		Config: map[string]string{
			"pid":       "1001",
			"pkey":      "pkey-1001",
			"apiBase":   "https://pay.example.com",
			"notifyUrl": "https://merchant.example.com/notify",
			"returnUrl": "https://merchant.example.com/return",
		},
		SupportedTypes: []string{"alipay"},
		Enabled:        true,
	})
	require.NoError(t, err)

	_, err = svc.CreateProviderInstance(ctx, CreateProviderInstanceRequest{
		ProviderKey:    "alipay",
		Name:           "Official Alipay",
		Config:         map[string]string{"appId": "app-1", "privateKey": "private-key"},
		SupportedTypes: []string{"alipay"},
		Enabled:        true,
	})
	require.NoError(t, err)
}

func TestCreateProviderInstanceRejectsMalformedOrUnsafeLimits(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		limits string
	}{
		{name: "malformed json", limits: `{not-json}`},
		{name: "negative limit", limits: `{"alipay":{"singleMax":-1}}`},
		{name: "inverted range", limits: `{"alipay":{"singleMin":100,"singleMax":10}}`},
		{name: "unknown field", limits: `{"alipay":{"singleMaximum":100}}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			client := newPaymentConfigServiceTestClient(t)
			svc := &PaymentConfigService{
				entClient:     client,
				encryptionKey: []byte("0123456789abcdef0123456789abcdef"),
			}

			created, err := svc.CreateProviderInstance(ctx, CreateProviderInstanceRequest{
				ProviderKey:    payment.TypeEasyPay,
				Name:           "invalid-limits",
				Config:         validEasyPayProviderConfig(t),
				SupportedTypes: []string{payment.TypeAlipay},
				Enabled:        true,
				Limits:         tt.limits,
			})
			require.Nil(t, created)
			require.Equal(t, "INVALID_PAYMENT_LIMITS", infraerrors.Reason(err))
		})
	}
}

func TestUpdateProviderInstanceAllowsEnablingVisibleMethodProviderFromDifferentSource(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	svc := &PaymentConfigService{
		entClient:     client,
		encryptionKey: []byte("0123456789abcdef0123456789abcdef"),
	}

	existing, err := svc.CreateProviderInstance(ctx, CreateProviderInstanceRequest{
		ProviderKey: "easypay",
		Name:        "EasyPay WeChat",
		Config: map[string]string{
			"pid":       "2001",
			"pkey":      "pkey-2001",
			"apiBase":   "https://pay.example.com",
			"notifyUrl": "https://merchant.example.com/notify",
			"returnUrl": "https://merchant.example.com/return",
		},
		SupportedTypes: []string{"wxpay"},
		Enabled:        true,
	})
	require.NoError(t, err)
	require.NotNil(t, existing)

	candidate, err := svc.CreateProviderInstance(ctx, CreateProviderInstanceRequest{
		ProviderKey:    "wxpay",
		Name:           "Official WeChat",
		Config:         validWxpayProviderConfig(t),
		SupportedTypes: []string{"wxpay"},
		Enabled:        false,
	})
	require.NoError(t, err)

	_, err = svc.UpdateProviderInstance(ctx, candidate.ID, UpdateProviderInstanceRequest{
		Enabled: boolPtrValue(true),
	})
	require.NoError(t, err)
}

func TestUpdateProviderInstancePersistsEnabledAndSupportedTypes(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	svc := &PaymentConfigService{
		entClient:     client,
		encryptionKey: []byte("0123456789abcdef0123456789abcdef"),
	}

	instance, err := svc.CreateProviderInstance(ctx, CreateProviderInstanceRequest{
		ProviderKey: "easypay",
		Name:        "EasyPay",
		Config: map[string]string{
			"pid":       "3001",
			"pkey":      "pkey-3001",
			"apiBase":   "https://pay.example.com",
			"notifyUrl": "https://merchant.example.com/notify",
			"returnUrl": "https://merchant.example.com/return",
		},
		SupportedTypes: []string{"alipay"},
		Enabled:        false,
	})
	require.NoError(t, err)

	_, err = svc.UpdateProviderInstance(ctx, instance.ID, UpdateProviderInstanceRequest{
		Enabled:        boolPtrValue(true),
		SupportedTypes: []string{"alipay", "wxpay"},
	})
	require.NoError(t, err)

	saved, err := client.PaymentProviderInstance.Get(ctx, instance.ID)
	require.NoError(t, err)
	require.True(t, saved.Enabled)
	require.Equal(t, "alipay,wxpay", saved.SupportedTypes)
}

func TestUpdateProviderInstanceRejectsProtectedConfigChangesWhilePendingOrders(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		providerKey   string
		createConfig  func(*testing.T) map[string]string
		supportedType []string
		updateConfig  map[string]string
		fieldName     string
		wantValue     string
	}{
		{
			name:          "wxpay appId",
			providerKey:   payment.TypeWxpay,
			createConfig:  validWxpayProviderConfig,
			supportedType: []string{payment.TypeWxpay},
			updateConfig:  map[string]string{"appId": "wx-app-updated"},
			fieldName:     "appId",
			wantValue:     "wx-app-test",
		},
		{
			name:          "wxpay mpAppId",
			providerKey:   payment.TypeWxpay,
			createConfig:  validWxpayProviderConfigWithJSAPIAppID,
			supportedType: []string{payment.TypeWxpay},
			updateConfig:  map[string]string{"mpAppId": "wx-mp-app-updated"},
			fieldName:     "mpAppId",
			wantValue:     "wx-mp-app-test",
		},
		{
			name:          "wxpay mchId",
			providerKey:   payment.TypeWxpay,
			createConfig:  validWxpayProviderConfig,
			supportedType: []string{payment.TypeWxpay},
			updateConfig:  map[string]string{"mchId": "mch-updated"},
			fieldName:     "mchId",
			wantValue:     "mch-test",
		},
		{
			name:          "wxpay publicKeyId",
			providerKey:   payment.TypeWxpay,
			createConfig:  validWxpayProviderConfig,
			supportedType: []string{payment.TypeWxpay},
			updateConfig:  map[string]string{"publicKeyId": "public-key-id-updated"},
			fieldName:     "publicKeyId",
			wantValue:     "public-key-id-test",
		},
		{
			name:          "wxpay certSerial",
			providerKey:   payment.TypeWxpay,
			createConfig:  validWxpayProviderConfig,
			supportedType: []string{payment.TypeWxpay},
			updateConfig:  map[string]string{"certSerial": "cert-serial-updated"},
			fieldName:     "certSerial",
			wantValue:     "cert-serial-test",
		},
		{
			name:          "alipay appId",
			providerKey:   payment.TypeAlipay,
			createConfig:  validAlipayProviderConfig,
			supportedType: []string{payment.TypeAlipay},
			updateConfig:  map[string]string{"appId": "alipay-app-updated"},
			fieldName:     "appId",
			wantValue:     "alipay-app-test",
		},
		{
			name:          "easypay pid",
			providerKey:   payment.TypeEasyPay,
			createConfig:  validEasyPayProviderConfig,
			supportedType: []string{payment.TypeAlipay},
			updateConfig:  map[string]string{"pid": "pid-updated"},
			fieldName:     "pid",
			wantValue:     "pid-test",
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			ctx := context.Background()
			client := newPaymentConfigServiceTestClient(t)
			svc := &PaymentConfigService{
				entClient:     client,
				encryptionKey: []byte("0123456789abcdef0123456789abcdef"),
			}

			instance, err := svc.CreateProviderInstance(ctx, CreateProviderInstanceRequest{
				ProviderKey:    tc.providerKey,
				Name:           "protected-config-instance",
				Config:         tc.createConfig(t),
				SupportedTypes: tc.supportedType,
				Enabled:        true,
			})
			require.NoError(t, err)

			createPendingProviderConfigOrder(t, ctx, client, instance)

			updated, err := svc.UpdateProviderInstance(ctx, instance.ID, UpdateProviderInstanceRequest{
				Config: tc.updateConfig,
			})
			require.Nil(t, updated)
			require.Error(t, err)
			require.Equal(t, "PENDING_ORDERS", infraerrors.Reason(err))

			saved, err := client.PaymentProviderInstance.Get(ctx, instance.ID)
			require.NoError(t, err)
			cfg, err := svc.decryptConfig(saved.Config)
			require.NoError(t, err)
			require.Equal(t, tc.wantValue, cfg[tc.fieldName])
		})
	}
}

func TestUpdateProviderInstanceAllowsSafeConfigChangesWhilePendingOrders(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		providerKey   string
		createConfig  func(*testing.T) map[string]string
		supportedType []string
		updateConfig  map[string]string
		fieldName     string
		wantValue     string
	}{
		{
			name:          "wxpay notifyUrl",
			providerKey:   payment.TypeWxpay,
			createConfig:  validWxpayProviderConfig,
			supportedType: []string{payment.TypeWxpay},
			updateConfig:  map[string]string{"notifyUrl": "https://merchant.example.com/wxpay/notify-v2"},
			fieldName:     "notifyUrl",
			wantValue:     "https://merchant.example.com/wxpay/notify-v2",
		},
		{
			name:          "alipay same appId",
			providerKey:   payment.TypeAlipay,
			createConfig:  validAlipayProviderConfig,
			supportedType: []string{payment.TypeAlipay},
			updateConfig:  map[string]string{"appId": "alipay-app-test"},
			fieldName:     "appId",
			wantValue:     "alipay-app-test",
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			ctx := context.Background()
			client := newPaymentConfigServiceTestClient(t)
			svc := &PaymentConfigService{
				entClient:     client,
				encryptionKey: []byte("0123456789abcdef0123456789abcdef"),
			}

			instance, err := svc.CreateProviderInstance(ctx, CreateProviderInstanceRequest{
				ProviderKey:    tc.providerKey,
				Name:           "safe-config-instance",
				Config:         tc.createConfig(t),
				SupportedTypes: tc.supportedType,
				Enabled:        true,
			})
			require.NoError(t, err)

			createPendingProviderConfigOrder(t, ctx, client, instance)

			updated, err := svc.UpdateProviderInstance(ctx, instance.ID, UpdateProviderInstanceRequest{
				Config: tc.updateConfig,
			})
			require.NoError(t, err)
			require.NotNil(t, updated)

			saved, err := client.PaymentProviderInstance.Get(ctx, instance.ID)
			require.NoError(t, err)
			cfg, err := svc.decryptConfig(saved.Config)
			require.NoError(t, err)
			require.Equal(t, tc.wantValue, cfg[tc.fieldName])
		})
	}
}

func TestUpdateEasyPayAPIBaseRequiresExplicitPKeyReentry(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	svc := &PaymentConfigService{
		entClient:     client,
		encryptionKey: []byte("0123456789abcdef0123456789abcdef"),
	}
	instance, err := svc.CreateProviderInstance(ctx, CreateProviderInstanceRequest{
		ProviderKey:    payment.TypeEasyPay,
		Name:           "bound-easypay",
		Config:         validEasyPayProviderConfig(t),
		SupportedTypes: []string{payment.TypeAlipay},
		Enabled:        true,
	})
	require.NoError(t, err)

	updated, err := svc.UpdateProviderInstance(ctx, instance.ID, UpdateProviderInstanceRequest{
		Config: map[string]string{"apiBase": "https://attacker.example.com"},
	})
	require.Nil(t, updated)
	require.Equal(t, "EASYPAY_PKEY_REENTRY_REQUIRED", infraerrors.Reason(err))
	saved, err := client.PaymentProviderInstance.Get(ctx, instance.ID)
	require.NoError(t, err)
	savedConfig, err := svc.decryptConfig(saved.Config)
	require.NoError(t, err)
	require.Equal(t, "https://pay.example.com", savedConfig["apiBase"])
	require.Equal(t, "pkey-test", savedConfig["pkey"])

	updated, err = svc.UpdateProviderInstance(ctx, instance.ID, UpdateProviderInstanceRequest{
		Config: map[string]string{
			"apiBase": "https://payments-v2.example.com",
			"pkey":    "pkey-v2",
		},
	})
	require.NoError(t, err)
	require.NotNil(t, updated)
	saved, err = client.PaymentProviderInstance.Get(ctx, instance.ID)
	require.NoError(t, err)
	savedConfig, err = svc.decryptConfig(saved.Config)
	require.NoError(t, err)
	require.Equal(t, "https://payments-v2.example.com", savedConfig["apiBase"])
	require.Equal(t, "pkey-v2", savedConfig["pkey"])
}

func createPendingProviderConfigOrder(t *testing.T, ctx context.Context, client *dbent.Client, instance *dbent.PaymentProviderInstance) {
	t.Helper()

	user, err := client.User.Create().
		SetEmail("provider-config-pending@example.com").
		SetPasswordHash("hash").
		SetUsername("provider-config-pending-user").
		Save(ctx)
	require.NoError(t, err)

	instanceID := strconv.FormatInt(instance.ID, 10)
	_, err = client.PaymentOrder.Create().
		SetUserID(user.ID).
		SetUserEmail(user.Email).
		SetUserName(user.Username).
		SetAmount(88).
		SetPayAmount(88).
		SetFeeRate(0).
		SetRechargeCode("PENDING-PROVIDER-CONFIG-" + instanceID).
		SetOutTradeNo("sub2_pending_provider_config_" + instanceID).
		SetPaymentType(providerPendingOrderPaymentType(instance.ProviderKey)).
		SetPaymentTradeNo("").
		SetOrderType(payment.OrderTypeBalance).
		SetStatus(OrderStatusPending).
		SetExpiresAt(time.Now().Add(time.Hour)).
		SetClientIP("127.0.0.1").
		SetSrcHost("api.example.com").
		SetProviderInstanceID(instanceID).
		SetProviderKey(instance.ProviderKey).
		Save(ctx)
	require.NoError(t, err)
}

func providerPendingOrderPaymentType(providerKey string) string {
	switch providerKey {
	case payment.TypeWxpay:
		return payment.TypeWxpay
	case payment.TypeAlipay:
		return payment.TypeAlipay
	default:
		return payment.TypeAlipay
	}
}

func boolPtrValue(v bool) *bool {
	return &v
}

func validAlipayProviderConfig(t *testing.T) map[string]string {
	t.Helper()

	return map[string]string{
		"appId":      "alipay-app-test",
		"privateKey": "alipay-private-key-test",
		"notifyUrl":  "https://merchant.example.com/alipay/notify",
		"returnUrl":  "https://merchant.example.com/alipay/return",
	}
}

func validEasyPayProviderConfig(t *testing.T) map[string]string {
	t.Helper()

	return map[string]string{
		"pid":       "pid-test",
		"pkey":      "pkey-test",
		"apiBase":   "https://pay.example.com",
		"notifyUrl": "https://merchant.example.com/easypay/notify",
		"returnUrl": "https://merchant.example.com/easypay/return",
	}
}

func validWxpayProviderConfig(t *testing.T) map[string]string {
	t.Helper()

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	privDER, err := x509.MarshalPKCS8PrivateKey(key)
	require.NoError(t, err)
	pubDER, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	require.NoError(t, err)

	return map[string]string{
		"appId":       "wx-app-test",
		"mchId":       "mch-test",
		"privateKey":  string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privDER})),
		"apiV3Key":    "12345678901234567890123456789012",
		"publicKey":   string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pubDER})),
		"publicKeyId": "public-key-id-test",
		"certSerial":  "cert-serial-test",
	}
}

func validWxpayProviderConfigWithJSAPIAppID(t *testing.T) map[string]string {
	t.Helper()

	cfg := validWxpayProviderConfig(t)
	cfg["mpAppId"] = "wx-mp-app-test"
	return cfg
}
