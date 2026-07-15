//go:build unit

package repository

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestEncodeSchedulerCachedAccountEncryptsFullSecretPayload(t *testing.T) {
	account := service.Account{
		ID:          42,
		Credentials: map[string]any{"access_token": "account-secret-that-must-not-be-plaintext"},
		Proxy:       &service.Proxy{Password: "proxy-secret-that-must-not-be-plaintext"},
	}
	encryptor := newDomainMigrationTestEncryptor(t)

	encoded, err := encodeSchedulerCachedAccount(account, schedulerCachePayloadKindAccount, encryptor)
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(encoded, secretDomainCiphertextPrefix))
	require.NotContains(t, encoded, "account-secret-that-must-not-be-plaintext")
	require.NotContains(t, encoded, "proxy-secret-that-must-not-be-plaintext")

	decoded, err := decodeSchedulerCachedAccount(encoded, schedulerCachePayloadKindAccount, encryptor)
	require.NoError(t, err)
	require.Equal(t, "account-secret-that-must-not-be-plaintext", decoded.GetCredential("access_token"))
	require.NotNil(t, decoded.Proxy)
	require.Equal(t, "proxy-secret-that-must-not-be-plaintext", decoded.Proxy.Password)
}

func TestDecodeSchedulerCachedAccountRejectsPlaintextAndWrongPayloadKind(t *testing.T) {
	encryptor := newDomainMigrationTestEncryptor(t)
	legacy, err := json.Marshal(service.Account{ID: 42, Credentials: map[string]any{"api_key": "legacy-plaintext"}})
	require.NoError(t, err)

	_, err = decodeSchedulerCachedAccount(legacy, schedulerCachePayloadKindAccount, encryptor)
	require.Error(t, err)

	encoded, err := encodeSchedulerCachedAccount(service.Account{ID: 42}, schedulerCachePayloadKindAccount, encryptor)
	require.NoError(t, err)
	_, err = decodeSchedulerCachedAccount(encoded, schedulerCachePayloadKindMetadata, encryptor)
	require.ErrorContains(t, err, "payload kind")
}

func TestProvideSchedulerCacheFailsClosedWithoutDomainEncryptor(t *testing.T) {
	rdb := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1"})
	t.Cleanup(func() { _ = rdb.Close() })

	_, err := NewSchedulerCache(rdb, nil)
	require.ErrorContains(t, err, "domain secret encryptor")
}

func TestBuildSchedulerMetadataAccount_KeepsOpenAIWSFlags(t *testing.T) {
	account := service.Account{
		ID:       42,
		Platform: service.PlatformOpenAI,
		Type:     service.AccountTypeOAuth,
		Extra: map[string]any{
			"openai_oauth_responses_websockets_v2_enabled": true,
			"openai_oauth_responses_websockets_v2_mode":    service.OpenAIWSIngressModePassthrough,
			"openai_ws_force_http":                         true,
			"mixed_scheduling":                             true,
			"unused_large_field":                           "drop-me",
		},
	}

	got := buildSchedulerMetadataAccount(account)

	require.Equal(t, true, got.Extra["openai_oauth_responses_websockets_v2_enabled"])
	require.Equal(t, service.OpenAIWSIngressModePassthrough, got.Extra["openai_oauth_responses_websockets_v2_mode"])
	require.Equal(t, true, got.Extra["openai_ws_force_http"])
	require.Equal(t, true, got.Extra["mixed_scheduling"])
	require.Nil(t, got.Extra["unused_large_field"])
}

func TestBuildSchedulerMetadataAccount_KeepsSlimGroupMembership(t *testing.T) {
	account := service.Account{
		ID:       42,
		Platform: service.PlatformAnthropic,
		GroupIDs: []int64{7, 9, 7, 0},
		AccountGroups: []service.AccountGroup{
			{
				AccountID: 42,
				GroupID:   7,
				Priority:  2,
				Account:   &service.Account{ID: 42, Name: "drop-from-metadata"},
				Group:     &service.Group{ID: 7, Name: "drop-from-metadata"},
			},
			{
				AccountID: 42,
				GroupID:   11,
				Priority:  3,
				Group:     &service.Group{ID: 11, Name: "drop-from-metadata"},
			},
			{
				AccountID: 42,
				GroupID:   0,
				Priority:  4,
			},
		},
	}

	got := buildSchedulerMetadataAccount(account)

	require.Equal(t, []int64{7, 9, 11}, got.GroupIDs)
	require.Len(t, got.AccountGroups, 2)
	require.Equal(t, int64(42), got.AccountGroups[0].AccountID)
	require.Equal(t, int64(7), got.AccountGroups[0].GroupID)
	require.Equal(t, 2, got.AccountGroups[0].Priority)
	require.Nil(t, got.AccountGroups[0].Account)
	require.Nil(t, got.AccountGroups[0].Group)
	require.Equal(t, int64(11), got.AccountGroups[1].GroupID)
	require.Nil(t, got.Groups)
}

func TestBuildSchedulerMetadataAccount_DropsAPIKeyButKeepsPresenceMarker(t *testing.T) {
	account := service.Account{
		ID:       42,
		Platform: service.PlatformGemini,
		Type:     service.AccountTypeAPIKey,
		Credentials: map[string]any{
			"api_key":       "must-not-be-copied",
			"project_id":    "project-1",
			"oauth_type":    "ai_studio",
			"model_mapping": map[string]any{"gemini-2.5-pro": "gemini-2.5-pro"},
		},
	}

	got := buildSchedulerMetadataAccount(account)

	require.Empty(t, got.GetCredential("api_key"))
	require.Equal(t, true, got.Credentials[service.SchedulerMetadataAPIKeyConfigured])
	require.Equal(t, "project-1", got.GetCredential("project_id"))
	require.NotEmpty(t, got.GetModelMapping())
}
