package proxyurl

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestForLogRemovesAllProxyUserInfoAndURLPayload(t *testing.T) {
	raw := "http://alice:p%40ssword@Proxy.Example.com:8080/private?access_token=query-secret#fragment-secret"
	got := ForLog(raw)
	require.Equal(t, "http://proxy.example.com:8080", got)
	for _, secret := range []string{"alice", "ssword", "private", "query-secret", "fragment-secret"} {
		require.NotContains(t, got, secret)
	}
	require.Equal(t, "direct", ForLog(""))
	require.Equal(t, "configured", ForLog("://not-a-url"))
}

func TestTransportCacheKeyHashesCredentialsButKeepsTransportURL(t *testing.T) {
	_, first, err := Parse("http://alice:first-secret@proxy.example.com:8080/path?token=ignored")
	require.NoError(t, err)
	_, same, err := Parse("http://alice:first-secret@proxy.example.com:8080")
	require.NoError(t, err)
	_, second, err := Parse("http://alice:second-secret@proxy.example.com:8080")
	require.NoError(t, err)

	firstKey := TransportCacheKey(first)
	require.Equal(t, firstKey, TransportCacheKey(same))
	require.NotEqual(t, firstKey, TransportCacheKey(second))
	require.Contains(t, firstKey, "http://proxy.example.com:8080")
	require.Contains(t, firstKey, "auth_sha256=")
	for _, secret := range []string{"alice", "first-secret", "second-secret", "token=ignored"} {
		require.False(t, strings.Contains(firstKey, secret), "cache key leaked %q", secret)
	}
}
