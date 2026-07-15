//go:build unit

package service

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAntigravityTransformErrorLogOmitsResponseBody(t *testing.T) {
	secret := "tenant-secret-in-gemini-response"
	sink, restore := captureStructuredLog(t)
	defer restore()

	logAntigravityTransformError(errors.New("synthetic transform failure"), []byte(`{"text":"`+secret+`"}`))

	require.False(t, sink.ContainsMessage(secret))
	require.True(t, sink.ContainsMessage("transform_error"))
	require.True(t, sink.ContainsMessage("body_bytes="))
}

func TestWebSearchExecutionLogOmitsTenantQuery(t *testing.T) {
	secret := "private medical search query"
	sink, restore := captureStructuredLog(t)
	defer restore()

	logWebSearchExecution(&Account{ID: 7, Name: "search-account"}, secret)

	require.False(t, sink.ContainsMessage(secret))
	require.False(t, sink.ContainsFieldValue("query", secret))
	require.True(t, sink.ContainsFieldValue("query_bytes", "28"))
}
