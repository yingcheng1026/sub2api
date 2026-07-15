//go:build unit

package service

import (
	"context"
	"strconv"
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

func TestCreateOrderInTxReservesDailyLimitForPendingOrders(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	user, err := client.User.Create().
		SetEmail("payment-admission-daily@example.com").
		SetPasswordHash("hash").
		SetUsername("payment-admission-daily").
		Save(ctx)
	require.NoError(t, err)

	svc := &PaymentService{entClient: client}
	req := CreateOrderRequest{
		UserID:      user.ID,
		PaymentType: payment.TypeAlipay,
		OrderType:   payment.OrderTypeBalance,
		ClientIP:    "127.0.0.1",
		SrcHost:     "app.example.com",
	}
	serviceUser := &User{ID: user.ID, Email: user.Email, Username: user.Username}
	cfg := &PaymentConfig{
		MaxPendingOrders: 3,
		DailyLimit:       100,
		OrderTimeoutMin:  30,
	}

	first, err := svc.createOrderInTx(ctx, req, serviceUser, nil, cfg, 60, 60, 0, 60, nil)
	require.NoError(t, err)
	require.Equal(t, OrderStatusPending, first.Status)

	second, err := svc.createOrderInTx(ctx, req, serviceUser, nil, cfg, 60, 60, 0, 60, nil)
	require.Nil(t, second)
	require.Error(t, err)
	require.Equal(t, "DAILY_LIMIT_EXCEEDED", infraerrors.Reason(err))

	count, err := client.PaymentOrder.Query().Count(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, count, "a rejected daily-limit reservation must not create another order")
}

func TestLockPaymentOrderAdmissionUserUsesPostgresRowLock(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	driver := entsql.OpenDB(dialect.Postgres, db)
	client := dbent.NewClient(dbent.Driver(driver))
	t.Cleanup(func() { _ = client.Close() })

	mock.ExpectQuery(`SELECT .* FROM "users" WHERE "users"\."id" = \$1 AND "users"\."deleted_at" IS NULL LIMIT 2 FOR UPDATE`).
		WithArgs(int64(42)).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(42)))

	require.NoError(t, lockPaymentOrderAdmissionUser(context.Background(), client, 42))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestReserveSelectedProviderCapacityRejectsOversubscription(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	user, err := client.User.Create().
		SetEmail("payment-provider-capacity@example.com").
		SetPasswordHash("hash").
		SetUsername("payment-provider-capacity").
		Save(ctx)
	require.NoError(t, err)
	instance, err := client.PaymentProviderInstance.Create().
		SetProviderKey(payment.TypeEasyPay).
		SetName("capacity-limited").
		SetConfig("encrypted-config").
		SetSupportedTypes(payment.TypeAlipay).
		SetLimits(`{"alipay":{"dailyLimit":100}}`).
		Save(ctx)
	require.NoError(t, err)

	_, err = client.PaymentOrder.Create().
		SetUserID(user.ID).
		SetUserEmail(user.Email).
		SetUserName(user.Username).
		SetAmount(80).
		SetPayAmount(80).
		SetFeeRate(0).
		SetRechargeCode("CAPACITY-EXISTING").
		SetOutTradeNo("capacity-existing").
		SetPaymentType(payment.TypeAlipay).
		SetPaymentTradeNo("").
		SetOrderType(payment.OrderTypeBalance).
		SetProviderInstanceID(strconv.FormatInt(instance.ID, 10)).
		SetProviderKey(instance.ProviderKey).
		SetStatus(OrderStatusPending).
		SetExpiresAt(time.Now().Add(time.Hour)).
		SetClientIP("127.0.0.1").
		SetSrcHost("app.example.com").
		Save(ctx)
	require.NoError(t, err)

	tx, err := client.Tx(ctx)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()

	sel := &payment.InstanceSelection{
		InstanceID:     strconv.FormatInt(instance.ID, 10),
		ProviderKey:    instance.ProviderKey,
		SupportedTypes: instance.SupportedTypes,
	}
	svc := &PaymentService{}
	err = svc.reserveSelectedProviderCapacity(ctx, tx, payment.TypeAlipay, 21, sel)
	require.Error(t, err)
	require.True(t, infraerrors.IsTooManyRequests(err))
	require.Equal(t, "PAYMENT_PROVIDER_CAPACITY_EXCEEDED", infraerrors.Reason(err))

	require.NoError(t, svc.reserveSelectedProviderCapacity(ctx, tx, payment.TypeAlipay, 20, sel))
}

func TestReserveSelectedProviderCapacityAllowsFirstReservation(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	instance, err := client.PaymentProviderInstance.Create().
		SetProviderKey(payment.TypeEasyPay).
		SetName("capacity-empty").
		SetConfig("encrypted-config").
		SetSupportedTypes(payment.TypeAlipay).
		SetLimits(`{"alipay":{"dailyLimit":100}}`).
		Save(ctx)
	require.NoError(t, err)

	tx, err := client.Tx(ctx)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()

	sel := &payment.InstanceSelection{
		InstanceID:     strconv.FormatInt(instance.ID, 10),
		ProviderKey:    instance.ProviderKey,
		SupportedTypes: instance.SupportedTypes,
	}
	require.NoError(t, (&PaymentService{}).reserveSelectedProviderCapacity(ctx, tx, payment.TypeAlipay, 20, sel))
}

func TestLockPaymentProviderAdmissionUsesPostgresRowLock(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	driver := entsql.OpenDB(dialect.Postgres, db)
	client := dbent.NewClient(dbent.Driver(driver))
	t.Cleanup(func() { _ = client.Close() })

	mock.ExpectQuery(`SELECT .* FROM "payment_provider_instances" WHERE "payment_provider_instances"\."id" = \$1 AND "payment_provider_instances"\."enabled" LIMIT 2 FOR UPDATE`).
		WithArgs(int64(17)).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(17)))

	require.NoError(t, lockPaymentProviderAdmission(context.Background(), client, 17))
	require.NoError(t, mock.ExpectationsWereMet())
}
