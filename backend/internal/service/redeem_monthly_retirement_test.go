package service

import (
	"context"
	"testing"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

func TestMonthlyRedeemCodeCreationPathsAreRetired(t *testing.T) {
	ctx := context.Background()
	redeemSvc := &RedeemService{}

	_, err := redeemSvc.GenerateCodes(ctx, GenerateCodesRequest{
		Count: 1,
		Value: 30,
		Type:  RedeemTypeSubscription,
	})
	require.Error(t, err)
	require.Equal(t, "MONTHLY_PLANS_RETIRED", infraerrors.Reason(err))

	err = redeemSvc.CreateCode(ctx, &RedeemCode{
		Code:  "RETIRED-MONTHLY-CODE",
		Type:  RedeemTypeSubscription,
		Value: 30,
	})
	require.Error(t, err)
	require.Equal(t, "MONTHLY_PLANS_RETIRED", infraerrors.Reason(err))

	adminSvc := &adminServiceImpl{}
	_, err = adminSvc.GenerateRedeemCodes(ctx, &GenerateRedeemCodesInput{
		Count: 1,
		Type:  RedeemTypeSubscription,
		Value: 30,
	})
	require.Error(t, err)
	require.Equal(t, "MONTHLY_PLANS_RETIRED", infraerrors.Reason(err))
}
