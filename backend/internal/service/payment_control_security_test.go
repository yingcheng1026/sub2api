//go:build unit

package service

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	sqlmock "github.com/DATA-DOG/go-sqlmock"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/payment"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

func TestValidateCreateOrderPaymentMethodEnabled(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		enabledTypes []string
		method       string
		settings     map[string]string
		wantReason   string
	}{
		{
			name:         "rejects method omitted from enabled types",
			enabledTypes: []string{payment.TypeStripe},
			method:       payment.TypeAlipay,
			settings:     map[string]string{SettingPaymentVisibleMethodAlipayEnabled: "true"},
			wantReason:   "PAYMENT_METHOD_DISABLED",
		},
		{
			name:         "rejects disabled visible method despite enabled type",
			enabledTypes: []string{payment.TypeAlipay},
			method:       payment.TypeAlipay,
			settings:     map[string]string{SettingPaymentVisibleMethodAlipayEnabled: "false"},
			wantReason:   "PAYMENT_METHOD_DISABLED",
		},
		{
			name:         "allows enabled visible method",
			enabledTypes: []string{payment.TypeAlipay},
			method:       payment.TypeAlipay,
			settings:     map[string]string{SettingPaymentVisibleMethodAlipayEnabled: "true"},
		},
		{
			name:         "allows enabled non-visible method",
			enabledTypes: []string{payment.TypeStripe},
			method:       payment.TypeStripe,
			settings:     map[string]string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			svc := &PaymentConfigService{settingRepo: &paymentConfigSettingRepoStub{values: tt.settings}}
			err := svc.validateCreateOrderPaymentMethodEnabled(
				context.Background(),
				&PaymentConfig{EnabledTypes: tt.enabledTypes},
				tt.method,
			)
			if tt.wantReason == "" {
				require.NoError(t, err)
				return
			}
			require.Equal(t, tt.wantReason, infraerrors.Reason(err))
		})
	}
}

func TestCreateOrderEnforcesServerSideMethodPolicyBeforeProviderSelection(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		enabledTypes string
		visibleFlag  string
	}{
		{name: "method omitted from global allowlist", enabledTypes: payment.TypeStripe, visibleFlag: "true"},
		{name: "visible method disabled", enabledTypes: payment.TypeAlipay, visibleFlag: "false"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			configService := &PaymentConfigService{settingRepo: &paymentConfigSettingRepoStub{values: map[string]string{
				SettingPaymentEnabled:                    "true",
				SettingEnabledPaymentTypes:               tt.enabledTypes,
				SettingPaymentVisibleMethodAlipayEnabled: tt.visibleFlag,
			}}}
			response, err := (&PaymentService{configService: configService}).CreateOrder(
				context.Background(),
				CreateOrderRequest{PaymentType: payment.TypeAlipay, Amount: 10},
			)
			require.Nil(t, response)
			require.Equal(t, "PAYMENT_METHOD_DISABLED", infraerrors.Reason(err))
		})
	}
}

func TestProviderAdminProtectsLateRecoverableOrderStatuses(t *testing.T) {
	t.Parallel()

	for _, status := range []string{OrderStatusCancelled, OrderStatusExpired, OrderStatusFailed} {
		status := status
		t.Run(status, func(t *testing.T) {
			t.Parallel()

			ctx := context.Background()
			client := newPaymentConfigServiceTestClient(t)
			svc := &PaymentConfigService{
				entClient:     client,
				encryptionKey: []byte("0123456789abcdef0123456789abcdef"),
			}
			instance, err := svc.CreateProviderInstance(ctx, CreateProviderInstanceRequest{
				ProviderKey:    payment.TypeEasyPay,
				Name:           "late-recovery-" + strings.ToLower(status),
				Config:         validEasyPayProviderConfig(t),
				SupportedTypes: []string{payment.TypeAlipay},
				Enabled:        true,
			})
			require.NoError(t, err)
			createProviderControlOrder(t, ctx, client, instance, status)

			updated, err := svc.UpdateProviderInstance(ctx, instance.ID, UpdateProviderInstanceRequest{
				Config: map[string]string{"pid": "rotated-merchant"},
			})
			require.Nil(t, updated)
			require.Equal(t, "PENDING_ORDERS", infraerrors.Reason(err))

			updated, err = svc.UpdateProviderInstance(ctx, instance.ID, UpdateProviderInstanceRequest{
				Enabled: boolPtrValue(false),
			})
			require.Nil(t, updated)
			require.Equal(t, "PENDING_ORDERS", infraerrors.Reason(err))

			err = svc.DeleteProviderInstance(ctx, instance.ID)
			require.Equal(t, "PENDING_ORDERS", infraerrors.Reason(err))
		})
	}
}

func TestLockPaymentProviderMutationUsesPostgresRowLock(t *testing.T) {
	t.Parallel()

	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	driver := entsql.OpenDB(dialect.Postgres, db)
	client := dbent.NewClient(dbent.Driver(driver))
	t.Cleanup(func() { _ = client.Close() })

	mock.ExpectQuery(`SELECT .* FROM "payment_provider_instances" WHERE "payment_provider_instances"\."id" = \$1 LIMIT 2 FOR UPDATE`).
		WithArgs(int64(17)).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(17)))

	require.NoError(t, lockPaymentProviderMutation(context.Background(), client, 17))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestGetDashboardStatsRejectsUnboundedDaysBeforeDatabaseAccess(t *testing.T) {
	t.Parallel()

	stats, err := (&PaymentService{}).GetDashboardStats(context.Background(), MaxPaymentDashboardDays+1)
	require.Nil(t, stats)
	require.Equal(t, "INVALID_DASHBOARD_DAYS", infraerrors.Reason(err))
}

func createProviderControlOrder(
	t *testing.T,
	ctx context.Context,
	client *dbent.Client,
	instance *dbent.PaymentProviderInstance,
	status string,
) {
	t.Helper()

	suffix := strings.ToLower(status)
	user, err := client.User.Create().
		SetEmail("provider-control-" + suffix + "@example.com").
		SetPasswordHash("hash").
		SetUsername("provider-control-" + suffix).
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
		SetRechargeCode("PROVIDER-CONTROL-" + status).
		SetOutTradeNo("provider-control-" + suffix + "-" + instanceID).
		SetPaymentType(payment.TypeAlipay).
		SetPaymentTradeNo("").
		SetOrderType(payment.OrderTypeBalance).
		SetStatus(status).
		SetExpiresAt(time.Now().Add(time.Hour)).
		SetClientIP("127.0.0.1").
		SetSrcHost("api.example.com").
		SetProviderInstanceID(instanceID).
		SetProviderKey(instance.ProviderKey).
		Save(ctx)
	require.NoError(t, err)
}
