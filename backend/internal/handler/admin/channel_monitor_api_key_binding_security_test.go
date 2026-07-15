package admin

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

type monitorAPIKeyRepoStub struct {
	service.APIKeyRepository
	key *service.APIKey
}

func (s monitorAPIKeyRepoStub) GetByID(context.Context, int64) (*service.APIKey, error) {
	if s.key == nil {
		return nil, service.ErrAPIKeyNotFound
	}
	copy := *s.key
	return &copy, nil
}

func TestChannelMonitorResolvesOwnedAPIKeyServerSide(t *testing.T) {
	apiKeyService := service.NewAPIKeyService(
		monitorAPIKeyRepoStub{key: &service.APIKey{ID: 9, UserID: 7, Key: "sk-server-only", Status: service.StatusAPIKeyActive}},
		nil, nil, nil, nil, nil, &config.Config{},
	)
	h := ProvideChannelMonitorHandler(nil, apiKeyService, nil)
	id := int64(9)
	resolved, err := h.resolveOwnedAPIKey(context.Background(), 7, &id, "masked-browser-value")
	require.NoError(t, err)
	require.Equal(t, "sk-server-only", resolved)

	_, err = h.resolveOwnedAPIKey(context.Background(), 8, &id, "")
	require.ErrorIs(t, err, service.ErrInsufficientPerms)
}
