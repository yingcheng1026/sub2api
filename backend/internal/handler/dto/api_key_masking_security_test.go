package dto

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestAPIKeyFromServiceMaskedDoesNotExposeReusableSecret(t *testing.T) {
	raw := "sk-super-secret-api-key"
	out := APIKeyFromServiceMasked(&service.APIKey{
		ID:        7,
		UserID:    9,
		Key:       raw,
		KeyPrefix: "sk-sup",
		Name:      "customer key",
	})
	require.NotNil(t, out)
	require.NotEqual(t, raw, out.Key)
	require.NotContains(t, out.Key, "secret")
	require.Equal(t, "sk-sup••••", out.Key)
}

func TestAdminSubscriptionMapperMasksNestedWalletKey(t *testing.T) {
	raw := "sk-wallet-secret"
	out := UserSubscriptionFromServiceAdmin(&service.UserSubscription{
		WalletUniversalKey: &service.APIKey{Key: raw, KeyPrefix: "sk-wall"},
	})
	require.NotNil(t, out)
	require.NotNil(t, out.WalletUniversalKey)
	require.NotEqual(t, raw, out.WalletUniversalKey.Key)
}

func TestDefaultNestedMappersDoNotExposeAPIKeyPlaintext(t *testing.T) {
	key := APIKeyFromService(&service.APIKey{ID: 9, Key: "sk-default-mapper-secret", KeyPrefix: "sk-defau"})
	require.Equal(t, "sk-default-mapper-secret", key.Key, "one-time create mapper remains explicit")

	user := UserFromService(&service.User{APIKeys: []service.APIKey{{ID: 9, Key: "sk-default-mapper-secret", KeyPrefix: "sk-defau"}}})
	require.Len(t, user.APIKeys, 1)
	require.NotContains(t, user.APIKeys[0].Key, "secret")

	usage := UsageLogFromService(&service.UsageLog{APIKey: &service.APIKey{ID: 9, Key: "sk-default-mapper-secret", KeyPrefix: "sk-defau"}})
	require.NotNil(t, usage.APIKey)
	require.NotContains(t, usage.APIKey.Key, "secret")
}
