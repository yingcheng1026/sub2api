package service

import "testing"

import "github.com/stretchr/testify/require"

func TestNormalizeOpenAIMessagesDispatchModelConfig(t *testing.T) {
	t.Parallel()

	cfg := normalizeOpenAIMessagesDispatchModelConfig(OpenAIMessagesDispatchModelConfig{
		OpusMappedModel:   " gpt-5.4-high ",
		SonnetMappedModel: "gpt-5.3-codex",
		HaikuMappedModel:  " gpt-5.4-mini-medium ",
		ExactModelMappings: map[string]string{
			" claude-sonnet-4-5-20250929 ": " gpt-5.2-high ",
			"":                             "gpt-5.4",
			"claude-opus-4-6":              " ",
		},
	})

	require.Equal(t, "gpt-5.4", cfg.OpusMappedModel)
	require.Equal(t, "gpt-5.3-codex", cfg.SonnetMappedModel)
	require.Equal(t, "gpt-5.4-mini", cfg.HaikuMappedModel)
	require.Equal(t, map[string]string{
		"claude-sonnet-4-5-20250929": "gpt-5.2",
	}, cfg.ExactModelMappings)
}

func TestResolveMessagesDispatchModel_NativeGPT56DoesNotEnterClaudeFamilyMapping(t *testing.T) {
	t.Parallel()

	group := &Group{MessagesDispatchModelConfig: OpenAIMessagesDispatchModelConfig{
		OpusMappedModel:   "gpt-5.6-sol",
		SonnetMappedModel: "gpt-5.6-terra",
		HaikuMappedModel:  "gpt-5.6-luna",
	}}

	for _, model := range []string{"gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna"} {
		require.Empty(t, group.ResolveMessagesDispatchModel(model), "native GPT model %q must bypass Claude family dispatch", model)
	}
}
