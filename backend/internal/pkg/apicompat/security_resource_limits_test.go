package apicompat

import (
	"bytes"
	"encoding/json"
	"errors"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSanitizeAnthropicRequestBodyRejectsOversizedSlowPath(t *testing.T) {
	body := make([]byte, 0, 16<<20+128)
	body = append(body, `{"messages":[],"thinking":"","padding":"`...)
	body = append(body, bytes.Repeat([]byte{'x'}, 16<<20)...)
	body = append(body, `"}`...)

	out, removed, err := SanitizeAnthropicRequestBody(body)
	require.ErrorIs(t, err, ErrAnthropicSanitizeBodyTooLarge)
	require.Zero(t, removed)
	require.Equal(t, body, out)
}

func TestResponsesEventToChatChunksBoundsToolCallState(t *testing.T) {
	state := NewResponsesEventToChatState()
	for i := 0; i < 129; i++ {
		ResponsesEventToChatChunks(&ResponsesStreamEvent{
			Type:        "response.output_item.added",
			OutputIndex: i,
			Item: &ResponsesOutput{
				Type:   "function_call",
				CallID: "call_" + strconv.Itoa(i),
				Name:   "tool",
			},
		}, state)
	}

	require.LessOrEqual(t, len(state.OutputIndexToToolIndex), 128)
	require.ErrorIs(t, state.Err(), ErrResponsesCompatibilityResourceLimit)
}

func TestBufferedResponseAccumulatorBoundsFunctionCallState(t *testing.T) {
	acc := NewBufferedResponseAccumulator()
	for i := 0; i < 129; i++ {
		acc.ProcessEvent(&ResponsesStreamEvent{
			Type:        "response.output_item.added",
			OutputIndex: i,
			Item: &ResponsesOutput{
				Type:   "function_call",
				CallID: "call_" + strconv.Itoa(i),
				Name:   "tool",
			},
		})
	}

	require.LessOrEqual(t, len(acc.funcCalls), 128)
	require.ErrorIs(t, acc.Err(), ErrResponsesCompatibilityResourceLimit)
}

func TestResponsesEventToChatChunksBoundsTotalEventCount(t *testing.T) {
	state := NewResponsesEventToChatState()
	state.eventCount = maxResponsesCompatibilityEvents

	chunks := ResponsesEventToChatChunks(&ResponsesStreamEvent{Type: "response.created"}, state)

	require.Empty(t, chunks)
	require.ErrorIs(t, state.Err(), ErrResponsesCompatibilityResourceLimit)
}

func TestBufferedResponseAccumulatorBoundsRetainedBytes(t *testing.T) {
	acc := NewBufferedResponseAccumulator()
	acc.retainedBytes = maxResponsesCompatibilityOutputBytes - 1

	err := acc.ProcessEvent(&ResponsesStreamEvent{
		Type:  "response.output_text.delta",
		Delta: "xx",
	})

	require.ErrorIs(t, err, ErrResponsesCompatibilityResourceLimit)
	require.ErrorIs(t, acc.Err(), ErrResponsesCompatibilityResourceLimit)
	require.Zero(t, acc.text.Len())
	require.False(t, acc.HasContent())
	require.Nil(t, acc.BuildOutput())
}

func TestBufferedResponseAccumulatorBoundsTotalEventCount(t *testing.T) {
	acc := NewBufferedResponseAccumulator()
	acc.eventCount = maxResponsesCompatibilityEvents

	err := acc.ProcessEvent(&ResponsesStreamEvent{Type: "response.created"})

	require.True(t, errors.Is(err, ErrResponsesCompatibilityResourceLimit))
}

func TestBufferedResponseAccumulatorKeepsBuildersStableAcrossSliceGrowth(t *testing.T) {
	acc := NewBufferedResponseAccumulator()
	require.NoError(t, acc.ProcessEvent(&ResponsesStreamEvent{
		Type:        "response.output_item.added",
		OutputIndex: 1,
		Item:        &ResponsesOutput{Type: "function_call", CallID: "call_1", Name: "one"},
	}))
	require.NoError(t, acc.ProcessEvent(&ResponsesStreamEvent{
		Type:        "response.function_call_arguments.delta",
		OutputIndex: 1,
		Delta:       `{"a":`,
	}))
	require.NoError(t, acc.ProcessEvent(&ResponsesStreamEvent{
		Type:        "response.output_item.added",
		OutputIndex: 2,
		Item:        &ResponsesOutput{Type: "function_call", CallID: "call_2", Name: "two"},
	}))
	require.NoError(t, acc.ProcessEvent(&ResponsesStreamEvent{
		Type:        "response.function_call_arguments.delta",
		OutputIndex: 1,
		Delta:       `1}`,
	}))

	output := acc.BuildOutput()
	require.Len(t, output, 2)
	require.Equal(t, `{"a":1}`, output[0].Arguments)
}

func BenchmarkMergeConsecutiveMessages1000(b *testing.B) {
	messages := make([]AnthropicMessage, 1000)
	for i := range messages {
		content, err := json.Marshal([]AnthropicContentBlock{{Type: "text", Text: "x"}})
		require.NoError(b, err)
		messages[i] = AnthropicMessage{Role: "user", Content: content}
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = mergeConsecutiveMessages(messages)
	}
}

func BenchmarkResponsesToChatCompletions1000Parts(b *testing.B) {
	parts := make([]ResponsesContentPart, 1000)
	for i := range parts {
		parts[i] = ResponsesContentPart{Type: "output_text", Text: "x"}
	}
	resp := &ResponsesResponse{
		ID:     "resp_benchmark",
		Status: "completed",
		Output: []ResponsesOutput{{Type: "message", Content: parts}},
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = ResponsesToChatCompletions(resp, "gpt-test")
	}
}
