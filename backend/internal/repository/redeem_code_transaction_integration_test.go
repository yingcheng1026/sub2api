//go:build integration

package repository

import (
	"context"
	"errors"
	"testing"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestRedeemCodeRepositoryCreateUsesCallerTransaction(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	repo := NewRedeemCodeRepository(client)
	codeValue := "tx-rollback-" + uuid.NewString()[:20]

	tx, err := client.Tx(ctx)
	require.NoError(t, err)
	txCtx := dbent.NewTxContext(ctx, tx)

	created := &service.RedeemCode{
		Code:   codeValue,
		Type:   service.RedeemTypeBalance,
		Value:  10,
		Status: service.StatusUnused,
	}
	require.NoError(t, repo.Create(txCtx, created))
	_, err = repo.GetByCode(txCtx, codeValue)
	require.NoError(t, err, "transaction must read its own uncommitted redeem code")
	require.NoError(t, tx.Rollback())

	_, err = repo.GetByCode(ctx, codeValue)
	require.True(t, errors.Is(err, service.ErrRedeemCodeNotFound), "rollback must remove the redeem code")
}
