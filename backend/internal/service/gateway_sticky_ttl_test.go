package service

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestGatewayServiceStickySessionTTL_ConfigOverridesDefault(t *testing.T) {
	cache := &stubGatewayCache{}
	svc := &GatewayService{
		cache: cache,
		cfg: &config.Config{
			Gateway: config.GatewayConfig{
				OpenAIWS: config.GatewayOpenAIWSConfig{
					StickySessionTTLSeconds: 600,
				},
			},
		},
	}

	require.Equal(t, 600*time.Second, svc.stickySessionTTL())
	require.NoError(t, svc.BindStickySession(context.Background(), nil, "session-hash", 42))
	require.Equal(t, 600*time.Second, cache.lastSetTTL)
}

func TestGatewayServiceStickySessionTTL_DefaultWhenUnset(t *testing.T) {
	svc := &GatewayService{cfg: &config.Config{}}

	require.Equal(t, stickySessionTTL, svc.stickySessionTTL())
}
