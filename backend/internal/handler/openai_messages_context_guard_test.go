package handler

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestAnalyzeOpenAIMessagesContextRiskDetectsReadLoopContributors(t *testing.T) {
	body := []byte(`{
		"model":"claude-opus-4-7",
		"system":"You are Claude Code.",
		"tools":[{"name":"Read","input_schema":{"type":"object"}}],
		"messages":[
			{"role":"assistant","content":[{"type":"tool_use","id":"toolu_read_1","name":"Read","input":{"file_path":"/repo/results.md","offset":300}}]},
			{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_read_1","content":"` + strings.Repeat("line\\n", 30000) + `"}]},
			{"role":"assistant","content":[{"type":"tool_use","id":"toolu_read_2","name":"Read","input":{"file_path":"/repo/results.md","offset":600}}]},
			{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_read_2","content":"short"}]},
			{"role":"assistant","content":[{"type":"tool_use","id":"toolu_bash_1","name":"Bash","input":{"command":"pwd"}}]}
		]
	}`)

	diag := analyzeOpenAIMessagesContextRisk(body)

	require.Equal(t, 5, diag.MessageCount)
	require.Equal(t, 3, diag.ToolUseCount)
	require.Equal(t, 2, diag.ReadToolUseCount)
	require.Equal(t, 2, diag.ToolResultCount)
	require.Equal(t, 2, diag.ReadToolResultCount)
	require.Equal(t, "/repo/results.md", diag.LargestReadFilePath)
	require.GreaterOrEqual(t, diag.LargestReadToolResultBytes, 120000)
	require.True(t, diag.needsFileNavigationGuardrail())
}

func TestApplyOpenAIMessagesContextGuardrailAppendsSystemOnce(t *testing.T) {
	body := []byte(`{
		"model":"claude-opus-4-7",
		"system":[{"type":"text","text":"Existing Claude Code system."}],
		"messages":[
			{"role":"assistant","content":[{"type":"tool_use","id":"toolu_read_1","name":"Read","input":{"file_path":"/repo/results.md","offset":300}}]},
			{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_read_1","content":"` + strings.Repeat("line\\n", 30000) + `"}]}
		]
	}`)

	diag := analyzeOpenAIMessagesContextRisk(body)
	guarded, injected, err := applyOpenAIMessagesContextGuardrail(body, diag)

	require.NoError(t, err)
	require.True(t, injected)
	require.True(t, gjson.GetBytes(guarded, "system").IsArray())
	require.Contains(t, string(guarded), openAIMessagesContextGuardrailMarker)
	require.Contains(t, string(guarded), "line numbers")
	require.Contains(t, string(guarded), "byte offsets")

	guardedAgain, injectedAgain, err := applyOpenAIMessagesContextGuardrail(guarded, analyzeOpenAIMessagesContextRisk(guarded))
	require.NoError(t, err)
	require.False(t, injectedAgain)
	require.Equal(t, string(guarded), string(guardedAgain))
}
