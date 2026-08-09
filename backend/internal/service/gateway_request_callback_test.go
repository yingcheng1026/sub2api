package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNotifyUpstreamAcceptedHTTP2xxOnly(t *testing.T) {
	accepted := 0
	ctx := WithUpstreamAcceptedCallback(context.Background(), func() { accepted++ })
	NotifyUpstreamAcceptedHTTP2xx(ctx, 199)
	NotifyUpstreamAcceptedHTTP2xx(ctx, 300)
	require.Zero(t, accepted)
	NotifyUpstreamAcceptedHTTP2xx(ctx, 200)
	require.Equal(t, 1, accepted)
}
