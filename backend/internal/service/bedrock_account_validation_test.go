//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAdminCreateAccountRejectsBedrockAuthorityRegionBeforeRepositoryUse(t *testing.T) {
	svc := &adminServiceImpl{}
	account, err := svc.CreateAccount(context.Background(), &CreateAccountInput{
		Name:                 "bad-bedrock",
		Platform:             PlatformAnthropic,
		Type:                 AccountTypeBedrock,
		Credentials:          map[string]any{"aws_region": "x@attacker.example/"},
		SkipDefaultGroupBind: true,
	})
	require.Error(t, err)
	require.Nil(t, account)
}

func TestAccountServiceCreateRejectsBedrockAuthorityRegionBeforeRepositoryUse(t *testing.T) {
	svc := NewAccountService(nil, nil)
	account, err := svc.Create(context.Background(), CreateAccountRequest{
		Name:        "bad-bedrock",
		Platform:    PlatformAnthropic,
		Type:        AccountTypeBedrock,
		Credentials: map[string]any{"aws_region": "us-east-1@attacker.example"},
	})
	require.Error(t, err)
	require.Nil(t, account)
}

func TestAdminBulkUpdateRejectsBedrockAuthorityRegionBeforeRepositoryUse(t *testing.T) {
	svc := &adminServiceImpl{}
	result, err := svc.BulkUpdateAccounts(context.Background(), &BulkUpdateAccountsInput{
		AccountIDs:  []int64{1},
		Credentials: map[string]any{"aws_region": "us-east-1.attacker.example"},
	})
	require.Error(t, err)
	require.Nil(t, result)
}
