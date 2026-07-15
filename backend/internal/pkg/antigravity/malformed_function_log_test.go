package antigravity

import (
	"bytes"
	"encoding/json"
	"log"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMalformedFunctionCallLogsMetadataWithoutCandidateContent(t *testing.T) {
	const secret = "tenant-tool-argument-must-not-reach-logs"
	response := V1InternalResponse{
		Response: GeminiResponse{
			Candidates: []GeminiCandidate{{
				FinishReason: "MALFORMED_FUNCTION_CALL",
				Content: &GeminiContent{
					Role: "model",
					Parts: []GeminiPart{{
						FunctionCall: &GeminiFunctionCall{
							Name: "sensitive_tool",
							Args: map[string]any{"token": secret},
						},
					}},
				},
			}},
		},
	}
	payload, err := json.Marshal(response)
	require.NoError(t, err)

	var captured bytes.Buffer
	previousOutput := log.Writer()
	log.SetOutput(&captured)
	t.Cleanup(func() { log.SetOutput(previousOutput) })

	processor := NewStreamingProcessor("model-stream")
	_ = processor.ProcessLine("data: " + string(payload))
	_, _, err = TransformGeminiToClaude(payload, "model-buffered")
	require.NoError(t, err)

	logs := captured.String()
	require.Contains(t, logs, "MALFORMED_FUNCTION_CALL")
	require.Contains(t, logs, "model-stream")
	require.Contains(t, logs, "model-buffered")
	require.False(t, strings.Contains(logs, secret), logs)
	require.False(t, strings.Contains(logs, "sensitive_tool"), logs)
}
