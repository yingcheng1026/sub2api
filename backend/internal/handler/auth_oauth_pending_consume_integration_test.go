//go:build integration

package handler

import (
	"context"
	"testing"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	_ "github.com/Wei-Shaw/sub2api/ent/runtime"
	"github.com/Wei-Shaw/sub2api/internal/service"
	_ "github.com/lib/pq"
	"github.com/stretchr/testify/require"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
)

func TestConsumePendingOAuthBrowserSessionTxRejectsStaleConcurrentConsumer(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	container, err := tcpostgres.Run(
		ctx,
		"postgres:18.1-alpine3.23",
		tcpostgres.WithDatabase("sub2api_test"),
		tcpostgres.WithUsername("postgres"),
		tcpostgres.WithPassword("postgres"),
		tcpostgres.BasicWaitStrategies(),
	)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, container.Terminate(context.Background())) })

	dsn, err := container.ConnectionString(ctx, "sslmode=disable", "TimeZone=UTC")
	require.NoError(t, err)
	client, err := dbent.Open("postgres", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	require.NoError(t, client.Schema.Create(ctx))

	session, err := client.PendingAuthSession.Create().
		SetSessionToken("stale-consume-session").
		SetIntent("login").
		SetProviderType("oidc").
		SetProviderKey("issuer").
		SetProviderSubject("subject").
		SetBrowserSessionKey("browser-session-key").
		SetExpiresAt(time.Now().UTC().Add(10 * time.Minute)).
		Save(ctx)
	require.NoError(t, err)

	firstTx, err := client.Tx(ctx)
	require.NoError(t, err)
	defer func() { _ = firstTx.Rollback() }()
	secondTx, err := client.Tx(ctx)
	require.NoError(t, err)
	defer func() { _ = secondTx.Rollback() }()

	firstRead, err := firstTx.Client().PendingAuthSession.Get(ctx, session.ID)
	require.NoError(t, err)
	secondStaleRead, err := secondTx.Client().PendingAuthSession.Get(ctx, session.ID)
	require.NoError(t, err)
	require.Nil(t, firstRead.ConsumedAt)
	require.Nil(t, secondStaleRead.ConsumedAt)

	require.NoError(t, consumeStoredPendingOAuthBrowserSessionTx(ctx, firstTx, firstRead, "browser-session-key"))
	require.NoError(t, firstTx.Commit())

	err = consumeStoredPendingOAuthBrowserSessionTx(ctx, secondTx, secondStaleRead, "browser-session-key")
	require.ErrorIs(t, err, service.ErrPendingAuthSessionConsumed)
}
