package service

import (
	"fmt"
	"strings"
	"testing"

	openaiModels "github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/stretchr/testify/require"
)

func TestNormalizeKnownOpenAICodexModel_GPT56ExactTiers(t *testing.T) {
	t.Parallel()

	tiers := []string{"sol", "terra", "luna"}
	suffixes := []string{"none", "low", "medium", "high", "xhigh"}
	for _, tier := range tiers {
		tier := tier
		exact := "gpt-5.6-" + tier
		cases := []string{
			exact,
			"openai/" + exact,
			strings.ToUpper("OPENAI/" + exact),
		}
		for _, suffix := range suffixes {
			cases = append(cases, exact+"-"+suffix)
		}
		for _, input := range cases {
			input := input
			t.Run(fmt.Sprintf("%s/%s", tier, input), func(t *testing.T) {
				require.Equal(t, exact, normalizeKnownOpenAICodexModel(input))
			})
		}
	}
}

func TestNormalizeKnownOpenAICodexModel_UnknownGPT56FailsClosed(t *testing.T) {
	t.Parallel()

	for _, input := range []string{
		"gpt-5.6",
		"gpt-5.6-unknown",
		"gpt-5.6-sol-turbo",
		"gpt-5.6-unknown-high",
		"gpt-5.6-sol-minimal",
		"gpt-5.6-sol-20260710",
		"gpt-5.6-sol-openai-compact",
		"gpt-5.7",
	} {
		input := input
		t.Run(input, func(t *testing.T) {
			require.Empty(t, normalizeKnownOpenAICodexModel(input))
		})
	}
}

func TestDefaultOpenAIModels_GPT56ExactTiers(t *testing.T) {
	t.Parallel()

	require.ElementsMatch(t, []string{"gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna"},
		filterGPT56ModelIDs(openaiModels.DefaultModelIDs()))
}

func filterGPT56ModelIDs(modelIDs []string) []string {
	filtered := make([]string, 0, 3)
	for _, modelID := range modelIDs {
		if strings.HasPrefix(modelID, "gpt-5.6") {
			filtered = append(filtered, modelID)
		}
	}
	return filtered
}
