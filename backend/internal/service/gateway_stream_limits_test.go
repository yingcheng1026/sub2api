package service

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestResolveGatewayMaxLineSizeUsesSecureDefaultAndHardCeiling(t *testing.T) {
	t.Parallel()

	require.Equal(t, config.DefaultGatewayMaxLineSize, resolveGatewayMaxLineSize(nil))
	require.Equal(t, 2*1024*1024, resolveGatewayMaxLineSize(&config.Config{
		Gateway: config.GatewayConfig{MaxLineSize: 2 * 1024 * 1024},
	}))
	require.Equal(t, config.MaxGatewayMaxLineSize, resolveGatewayMaxLineSize(&config.Config{
		Gateway: config.GatewayConfig{MaxLineSize: 512 * 1024 * 1024},
	}))
}
