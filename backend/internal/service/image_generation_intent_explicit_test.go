package service

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestIsExplicitImageGenerationIntent_IgnoresPassiveNamespace(t *testing.T) {
	body := []byte(`{
		"model": "gpt-5.5",
		"input": [{"type":"message","role":"user","content":"hello"}],
		"tools": [
			{"type":"function","name":"Read"},
			{"type":"namespace","name":"image_gen","tools":[{"type":"function","name":"imagegen"}]}
		],
		"tool_choice": "auto"
	}`)

	assert.False(t, IsExplicitImageGenerationIntent("/v1/responses", "gpt-5.5", body),
		"passive image_gen namespace should NOT be explicit image intent")

	assert.True(t, IsImageGenerationIntent("/v1/responses", "gpt-5.5", body),
		"passive image_gen namespace SHOULD be general image intent (for permission check)")
}

func TestIsExplicitImageGenerationIntent_DetectsNativeTool(t *testing.T) {
	body := []byte(`{
		"model": "gpt-5.5",
		"tools": [{"type":"image_generation","model":"gpt-image-2"}],
		"tool_choice": "auto"
	}`)

	assert.True(t, IsExplicitImageGenerationIntent("/v1/responses", "gpt-5.5", body),
		"native image_generation tool IS explicit intent")
}

func TestIsExplicitImageGenerationIntent_DetectsImageModel(t *testing.T) {
	assert.True(t, IsExplicitImageGenerationIntent("/v1/responses", "gpt-image-2", nil),
		"image model IS explicit intent")
}

func TestIsExplicitImageGenerationIntent_DetectsImageEndpoint(t *testing.T) {
	assert.True(t, IsExplicitImageGenerationIntent("/v1/images/generations", "gpt-5.5", nil),
		"image endpoint IS explicit intent")
}

func TestIsExplicitImageGenerationIntent_DetectsExplicitToolChoice(t *testing.T) {
	body := []byte(`{"model":"gpt-5.5","tools":[{"type":"function","name":"Read"}],"tool_choice":"image_generation"}`)
	assert.True(t, IsExplicitImageGenerationIntent("/v1/responses", "gpt-5.5", body),
		"explicit tool_choice selecting image_generation IS explicit intent")
}

func TestIsExplicitImageGenerationIntent_PlainTextRequest(t *testing.T) {
	body := []byte(`{
		"model": "gpt-5.5",
		"input": "hello",
		"tools": [{"type":"function","name":"Read"}]
	}`)

	assert.False(t, IsExplicitImageGenerationIntent("/v1/responses", "gpt-5.5", body),
		"plain text request should NOT be explicit image intent")
}

func TestClassifyOpenAIWSImageIntent_SeparatesAdmissionFromPermission(t *testing.T) {
	passive := []byte(`{"type":"response.create","model":"gpt-5.5","tools":[{"type":"namespace","name":"image_gen","tools":[{"type":"function","name":"imagegen"}]}]}`)
	intent := classifyOpenAIWSImageIntent("response.create", "gpt-5.5", passive, PlatformOpenAI)
	assert.False(t, intent.Explicit, "passive namespace must not consume image admission capacity")
	assert.True(t, intent.Permission, "passive namespace remains subject to the group permission gate")
}

func TestClassifyOpenAIWSImageIntent_SessionUpdateUsesNestedSession(t *testing.T) {
	update := []byte(`{"type":"session.update","session":{"model":"gpt-5.5","tools":[{"type":"image_generation","model":"gpt-image-2"}]}}`)
	intent := classifyOpenAIWSImageIntent("session.update", "gpt-5.1", update, PlatformOpenAI)
	assert.True(t, intent.Explicit)
	assert.True(t, intent.Permission)

	plain := []byte(`{"type":"session.update","session":{"voice":"alloy"}}`)
	intent = classifyOpenAIWSImageIntent("session.update", "gpt-5.5", plain, PlatformOpenAI)
	assert.False(t, intent.Explicit)
	assert.False(t, intent.Permission)
}

func TestOpenAIWSSessionImageIntentState_InheritsSessionToolsIntoResponseCreate(t *testing.T) {
	var state openAIWSSessionImageIntentState
	update := []byte(`{"type":"session.update","session":{"tools":[{"type":"image_generation","model":"gpt-image-2"}]}}`)
	intent := state.classify("session.update", "gpt-5.5", update, PlatformOpenAI)
	assert.True(t, intent.Explicit)
	assert.True(t, intent.Permission)

	plainTurn := []byte(`{"type":"response.create","model":"gpt-5.5","input":"hello"}`)
	intent = state.classify("response.create", "gpt-5.5", plainTurn, PlatformOpenAI)
	assert.True(t, intent.Explicit, "session image tools must keep later turns in image admission")
	assert.True(t, intent.Permission, "session image tools must keep later turns behind the permission gate")
}
