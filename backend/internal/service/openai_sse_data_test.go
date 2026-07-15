package service

import (
	"bufio"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestReadOpenAISSELineBounded(t *testing.T) {
	t.Run("returns a complete line within the limit", func(t *testing.T) {
		reader := bufio.NewReader(strings.NewReader("data: ok\nnext"))

		line, err := readOpenAISSELineBounded(reader, 16)

		require.NoError(t, err)
		require.Equal(t, []byte("data: ok\n"), line)
	})

	t.Run("rejects an oversized line without returning attacker bytes", func(t *testing.T) {
		reader := bufio.NewReaderSize(strings.NewReader(strings.Repeat("x", 65)+"\n"), 8)

		line, err := readOpenAISSELineBounded(reader, 64)

		require.Nil(t, line)
		require.Error(t, err)
		require.True(t, errors.Is(err, ErrUpstreamResponseBodyTooLarge))
	})
}

func TestOpenAISSEDataAccumulatorBounded(t *testing.T) {
	t.Run("emits a valid multi-line event", func(t *testing.T) {
		acc := newOpenAISSEDataAccumulator(64)
		var payloads []string
		emit := func(payload []byte) { payloads = append(payloads, string(payload)) }

		require.NoError(t, acc.AddLine("data: {\"ok\":", emit))
		require.NoError(t, acc.AddLine("data: true}", emit))
		require.NoError(t, acc.AddLine("", emit))
		require.Equal(t, []string{"{\"ok\":\ntrue}"}, payloads)
	})

	t.Run("rejects an event whose aggregate data exceeds the limit", func(t *testing.T) {
		acc := newOpenAISSEDataAccumulator(8)
		emit := func([]byte) { t.Fatal("oversized event must not be emitted") }

		require.NoError(t, acc.AddLine("data: 1234", emit))
		err := acc.AddLine("data: 5678", emit)

		require.Error(t, err)
		require.True(t, errors.Is(err, ErrUpstreamResponseBodyTooLarge))
	})

	t.Run("rejects excessive empty data fields", func(t *testing.T) {
		acc := newOpenAISSEDataAccumulator(openAISSEMaxDataLinesPerEvent * 2)
		emit := func([]byte) { t.Fatal("oversized event must not be emitted") }
		for range openAISSEMaxDataLinesPerEvent {
			require.NoError(t, acc.AddLine("data:", emit))
		}

		err := acc.AddLine("data:", emit)

		require.Error(t, err)
		require.True(t, errors.Is(err, ErrUpstreamResponseBodyTooLarge))
	})
}
