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

func TestResolveMessagesDispatchModel_OfficialClaudeCodeSelectors(t *testing.T) {
	t.Parallel()

	group := &Group{
		MessagesDispatchModelConfig: OpenAIMessagesDispatchModelConfig{
			OpusMappedModel:   "gpt-5.5",
			SonnetMappedModel: "gpt-5.4",
			HaikuMappedModel:  "gpt-5.4-mini",
		},
	}

	tests := []struct {
		name  string
		model string
		want  string
	}{
		{name: "official opus 1m alias", model: "opus[1m]", want: "gpt-5.5"},
		{name: "official opus alias", model: "opus", want: "gpt-5.5"},
		{name: "canonical opus 1m", model: "claude-opus-4-7[1m]", want: "gpt-5.5"},
		{name: "official sonnet 1m alias", model: "sonnet[1m]", want: "gpt-5.4"},
		{name: "official default selector", model: "default", want: "gpt-5.4"},
		{name: "official haiku alias", model: "haiku", want: "gpt-5.4-mini"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.want, group.ResolveMessagesDispatchModel(tt.model))
		})
	}
}

func TestResolveMessagesDispatchModel_DefaultsUseCurrentOpenAITiers(t *testing.T) {
	t.Parallel()

	group := &Group{}

	require.Equal(t, "gpt-5.5", group.ResolveMessagesDispatchModel("opus[1m]"))
	require.Equal(t, "gpt-5.4", group.ResolveMessagesDispatchModel("sonnet[1m]"))
	require.Equal(t, "gpt-5.4-mini", group.ResolveMessagesDispatchModel("haiku"))
}
