package provider

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestValidateWxpayOutTradeNoEnforcesProviderBoundary(t *testing.T) {
	require.NoError(t, validateWxpayOutTradeNo("0123456789abcdef0123456789abcdef"))
	require.Error(t, validateWxpayOutTradeNo(strings.Repeat("a", 33)))
	require.Error(t, validateWxpayOutTradeNo("contains space"))
}
