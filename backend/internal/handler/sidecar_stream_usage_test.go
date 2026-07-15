package handler

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestReadMeteredSidecarStreamAggregatesSSEUsage(t *testing.T) {
	t.Parallel()
	body := "data: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":12}}}\n\n" +
		"data: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":8}}\n\n"

	gotBody, usage, err := readMeteredSidecarStream(strings.NewReader(body), 4096, extractKiroUsage, true)
	require.NoError(t, err)
	require.Equal(t, body, string(gotBody))
	require.Equal(t, 12, usage.InputTokens)
	require.Equal(t, 8, usage.OutputTokens)
}

func TestReadMeteredSidecarStreamFailsClosed(t *testing.T) {
	t.Parallel()

	_, _, err := readMeteredSidecarStream(strings.NewReader("data: {}\n\n"), 4096, extractKiroUsage, true)
	require.ErrorIs(t, err, errSidecarStreamUsageMissing)

	_, _, err = readMeteredSidecarStream(strings.NewReader(strings.Repeat("x", 17)), 16, extractKiroUsage, false)
	require.ErrorContains(t, err, "exceeds 16 bytes")
}

type failingSidecarWriter struct{}

func (failingSidecarWriter) Write([]byte) (int, error) {
	return 0, errors.New("client disconnected")
}

func TestWriteBufferedSidecarStreamClassifiesClientDeliveryFailure(t *testing.T) {
	t.Parallel()

	err := writeBufferedSidecarStream(failingSidecarWriter{}, []byte("data: {}\n\n"))
	require.ErrorIs(t, err, errSidecarClientDelivery)
}

func TestSidecarUpstreamContextPreservesMeteringAfterClientCancellation(t *testing.T) {
	t.Parallel()

	parent, cancel := context.WithCancel(context.Background())
	cancel()
	require.ErrorIs(t, sidecarUpstreamContext(parent, false).Err(), context.Canceled)
	require.NoError(t, sidecarUpstreamContext(parent, true).Err())
}
