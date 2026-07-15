package service

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type pricingRemoteClientStub struct {
	body      []byte
	hash      string
	bodyErr   error
	hashErr   error
	bodyCalls int
	hashCalls int
}

func (s *pricingRemoteClientStub) FetchPricingJSON(context.Context, string) ([]byte, error) {
	s.bodyCalls++
	return s.body, s.bodyErr
}

func (s *pricingRemoteClientStub) FetchHashText(context.Context, string) (string, error) {
	s.hashCalls++
	return s.hash, s.hashErr
}

func newPricingIntegrityTestService(t *testing.T, remote *pricingRemoteClientStub) *PricingService {
	t.Helper()
	return NewPricingService(&config.Config{
		Pricing: config.PricingConfig{
			RemoteURL: "https://raw.githubusercontent.com/example/pricing.json",
			HashURL:   "https://raw.githubusercontent.com/example/pricing.sha256",
			DataDir:   t.TempDir(),
		},
	}, remote)
}

func TestPricingDownloadFailsClosedWhenHashFetchFails(t *testing.T) {
	remote := &pricingRemoteClientStub{
		body:    []byte(`{"gpt-test":{"input_cost_per_token":0.1}}`),
		hashErr: errors.New("hash unavailable"),
	}
	svc := newPricingIntegrityTestService(t, remote)

	err := svc.downloadPricingData()

	require.ErrorContains(t, err, "fetch remote pricing hash")
	require.Zero(t, remote.bodyCalls, "unverifiable pricing data must not be downloaded or activated")
	require.Empty(t, svc.pricingData)
}

func TestPricingDownloadRejectsMalformedOrMismatchedHash(t *testing.T) {
	body := []byte(`{"gpt-test":{"input_cost_per_token":0.1}}`)

	t.Run("malformed hash", func(t *testing.T) {
		remote := &pricingRemoteClientStub{body: body, hash: "not-a-sha256"}
		svc := newPricingIntegrityTestService(t, remote)

		err := svc.downloadPricingData()

		require.ErrorContains(t, err, "invalid remote pricing hash")
		require.Zero(t, remote.bodyCalls)
		require.Empty(t, svc.pricingData)
	})

	t.Run("hash mismatch", func(t *testing.T) {
		remote := &pricingRemoteClientStub{body: body, hash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}
		svc := newPricingIntegrityTestService(t, remote)

		err := svc.downloadPricingData()

		require.ErrorContains(t, err, "pricing hash mismatch")
		require.Equal(t, 1, remote.bodyCalls)
		require.Empty(t, svc.pricingData)
		_, statErr := os.Stat(svc.getPricingFilePath())
		require.ErrorIs(t, statErr, os.ErrNotExist)
	})
}

func TestPricingDownloadPersistsVerifiedDataWithPrivatePermissions(t *testing.T) {
	body := []byte(`{"gpt-test":{"input_cost_per_token":0.1}}`)
	hash := sha256.Sum256(body)
	remote := &pricingRemoteClientStub{body: body, hash: fmt.Sprintf("%x", hash)}
	svc := newPricingIntegrityTestService(t, remote)

	require.NoError(t, svc.downloadPricingData())
	require.NotNil(t, svc.pricingData["gpt-test"])
	require.Equal(t, remote.hash, svc.localHash)

	for _, path := range []string{svc.getPricingFilePath(), svc.getHashFilePath()} {
		info, err := os.Stat(path)
		require.NoError(t, err)
		require.Equal(t, os.FileMode(0o600), info.Mode().Perm(), path)
	}
}

func TestPricingLocalLoadRequiresMatchingPersistedHash(t *testing.T) {
	body := []byte(`{"gpt-test":{"input_cost_per_token":0.1}}`)
	hash := sha256.Sum256(body)

	tests := []struct {
		name        string
		hashMarker  string
		writeMarker bool
		wantError   string
	}{
		{name: "missing marker", wantError: "read local pricing hash"},
		{name: "malformed marker", hashMarker: "not-a-hash", writeMarker: true, wantError: "validate local pricing hash"},
		{name: "mismatched marker", hashMarker: strings.Repeat("a", 64), writeMarker: true, wantError: "local pricing hash mismatch"},
		{name: "matching marker", hashMarker: fmt.Sprintf("%x", hash), writeMarker: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := newPricingIntegrityTestService(t, &pricingRemoteClientStub{})
			require.NoError(t, os.WriteFile(svc.getPricingFilePath(), body, 0o600))
			if tt.writeMarker {
				require.NoError(t, os.WriteFile(svc.getHashFilePath(), []byte(tt.hashMarker+"\n"), 0o600))
			}

			err := svc.loadVerifiedLocalPricingData(svc.getPricingFilePath())
			if tt.wantError != "" {
				require.ErrorContains(t, err, tt.wantError)
				require.Empty(t, svc.pricingData)
				return
			}
			require.NoError(t, err)
			require.Equal(t, fmt.Sprintf("%x", hash), svc.localHash)
			require.NotNil(t, svc.pricingData["gpt-test"])
		})
	}
}

func TestPricingURLAlwaysRequiresHTTPS(t *testing.T) {
	svc := NewPricingService(&config.Config{
		Security: config.SecurityConfig{
			URLAllowlist: config.URLAllowlistConfig{
				Enabled:           false,
				AllowInsecureHTTP: true,
			},
		},
	}, &pricingRemoteClientStub{})

	_, err := svc.validatePricingURL("http://pricing.example.test/model.json")
	require.ErrorContains(t, err, "invalid url scheme")
	normalized, err := svc.validatePricingURL("https://pricing.example.test/model.json")
	require.NoError(t, err)
	require.Equal(t, "https://pricing.example.test/model.json", normalized)
}

func TestParsePricingDataRejectsNegativeCosts(t *testing.T) {
	svc := &PricingService{}

	_, err := svc.parsePricingData([]byte(`{
		"gpt-test": {
			"input_cost_per_token": -0.1,
			"output_cost_per_token": 0.2
		}
	}`))

	require.ErrorContains(t, err, "negative pricing value")
}

func TestPricingServiceGetModelPricing_GPT56ExactFallbacks(t *testing.T) {
	svc := &PricingService{pricingData: map[string]*LiteLLMModelPricing{
		"gpt-5.4": {InputCostPerToken: 99},
		"gpt-5.6": {InputCostPerToken: 98},
		"gpt-5.6-sol": {
			InputCostPerToken:           101,
			OutputCostPerToken:          102,
			CacheCreationInputTokenCost: 103,
			CacheReadInputTokenCost:     104,
		},
		"gpt-5.6-terra": {
			InputCostPerToken:           201,
			OutputCostPerToken:          202,
			CacheCreationInputTokenCost: 203,
			CacheReadInputTokenCost:     204,
		},
		"gpt-5.6-luna": {
			InputCostPerToken:           301,
			OutputCostPerToken:          302,
			CacheCreationInputTokenCost: 303,
			CacheReadInputTokenCost:     304,
		},
		"gpt-5.6-unknown": {InputCostPerToken: 97},
	}}
	tests := []struct {
		model      string
		input      float64
		output     float64
		cacheWrite float64
		cacheRead  float64
	}{
		{model: "gpt-5.6-sol", input: 5e-6, output: 30e-6, cacheWrite: 6.25e-6, cacheRead: 0.5e-6},
		{model: "gpt-5.6-terra", input: 2.5e-6, output: 15e-6, cacheWrite: 3.125e-6, cacheRead: 0.25e-6},
		{model: "gpt-5.6-luna", input: 1e-6, output: 6e-6, cacheWrite: 1.25e-6, cacheRead: 0.1e-6},
		{model: "openai/gpt-5.6-luna-high", input: 1e-6, output: 6e-6, cacheWrite: 1.25e-6, cacheRead: 0.1e-6},
	}
	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			pricing := svc.GetModelPricing(tt.model)
			require.NotNil(t, pricing)
			require.InDelta(t, tt.input, pricing.InputCostPerToken, 1e-12)
			require.InDelta(t, tt.output, pricing.OutputCostPerToken, 1e-12)
			require.InDelta(t, tt.cacheWrite, pricing.CacheCreationInputTokenCost, 1e-12)
			require.InDelta(t, tt.cacheRead, pricing.CacheReadInputTokenCost, 1e-12)

			pricing.InputCostPerToken = 401
			pricing.OutputCostPerToken = 402
			pricing.CacheCreationInputTokenCost = 403
			pricing.CacheReadInputTokenCost = 404

			fresh := svc.GetModelPricing(tt.model)
			require.NotSame(t, pricing, fresh)
			require.InDelta(t, tt.input, fresh.InputCostPerToken, 1e-12)
			require.InDelta(t, tt.output, fresh.OutputCostPerToken, 1e-12)
			require.InDelta(t, tt.cacheWrite, fresh.CacheCreationInputTokenCost, 1e-12)
			require.InDelta(t, tt.cacheRead, fresh.CacheReadInputTokenCost, 1e-12)
		})
	}

	require.Nil(t, svc.GetModelPricing("gpt-5.6"))
	require.Nil(t, svc.GetModelPricing("gpt-5.6-unknown"))
}

func TestBundledModelPricing_GPT56ExactPrices(t *testing.T) {
	t.Parallel()

	body, err := os.ReadFile(filepath.Join("..", "..", "resources", "model-pricing", "model_prices_and_context_window.json"))
	require.NoError(t, err)
	svc := &PricingService{}
	pricingData, err := svc.parsePricingData(body)
	require.NoError(t, err)

	tests := map[string][4]float64{
		"gpt-5.6-sol":   {5e-6, 30e-6, 6.25e-6, 0.5e-6},
		"gpt-5.6-terra": {2.5e-6, 15e-6, 3.125e-6, 0.25e-6},
		"gpt-5.6-luna":  {1e-6, 6e-6, 1.25e-6, 0.1e-6},
	}
	for model, want := range tests {
		pricing := pricingData[model]
		require.NotNil(t, pricing, model)
		require.InDelta(t, want[0], pricing.InputCostPerToken, 1e-12)
		require.InDelta(t, want[1], pricing.OutputCostPerToken, 1e-12)
		require.InDelta(t, want[2], pricing.CacheCreationInputTokenCost, 1e-12)
		require.InDelta(t, want[3], pricing.CacheReadInputTokenCost, 1e-12)
	}
}

func TestParsePricingData_ParsesPriorityAndServiceTierFields(t *testing.T) {
	svc := &PricingService{}
	body := []byte(`{
		"gpt-5.4": {
			"input_cost_per_token": 0.0000025,
			"input_cost_per_token_priority": 0.000005,
			"output_cost_per_token": 0.000015,
			"output_cost_per_token_priority": 0.00003,
			"cache_creation_input_token_cost": 0.0000025,
			"cache_read_input_token_cost": 0.00000025,
			"cache_read_input_token_cost_priority": 0.0000005,
			"supports_service_tier": true,
			"supports_prompt_caching": true,
			"litellm_provider": "openai",
			"mode": "chat"
		}
	}`)

	data, err := svc.parsePricingData(body)
	require.NoError(t, err)
	pricing := data["gpt-5.4"]
	require.NotNil(t, pricing)
	require.InDelta(t, 5e-6, pricing.InputCostPerTokenPriority, 1e-12)
	require.InDelta(t, 3e-5, pricing.OutputCostPerTokenPriority, 1e-12)
	require.InDelta(t, 5e-7, pricing.CacheReadInputTokenCostPriority, 1e-12)
	require.True(t, pricing.SupportsServiceTier)
}

func TestGetModelPricing_Gpt53CodexSparkUsesGpt51CodexPricing(t *testing.T) {
	sparkPricing := &LiteLLMModelPricing{InputCostPerToken: 1}
	gpt53Pricing := &LiteLLMModelPricing{InputCostPerToken: 9}

	svc := &PricingService{
		pricingData: map[string]*LiteLLMModelPricing{
			"gpt-5.1-codex": sparkPricing,
			"gpt-5.3":       gpt53Pricing,
		},
	}

	got := svc.GetModelPricing("gpt-5.3-codex-spark")
	require.Same(t, sparkPricing, got)
}

func TestGetModelPricing_Gpt53CodexFallbackStillUsesGpt52Codex(t *testing.T) {
	gpt52CodexPricing := &LiteLLMModelPricing{InputCostPerToken: 2}

	svc := &PricingService{
		pricingData: map[string]*LiteLLMModelPricing{
			"gpt-5.2-codex": gpt52CodexPricing,
		},
	}

	got := svc.GetModelPricing("gpt-5.3-codex")
	require.Same(t, gpt52CodexPricing, got)
}

func TestGetModelPricing_OpenAIFallbackMatchedLoggedAsInfo(t *testing.T) {
	logSink, restore := captureStructuredLog(t)
	defer restore()

	gpt52CodexPricing := &LiteLLMModelPricing{InputCostPerToken: 2}
	svc := &PricingService{
		pricingData: map[string]*LiteLLMModelPricing{
			"gpt-5.2-codex": gpt52CodexPricing,
		},
	}

	got := svc.GetModelPricing("gpt-5.3-codex")
	require.Same(t, gpt52CodexPricing, got)

	require.True(t, logSink.ContainsMessageAtLevel("[Pricing] OpenAI fallback matched gpt-5.3-codex -> gpt-5.2-codex", "info"))
	require.False(t, logSink.ContainsMessageAtLevel("[Pricing] OpenAI fallback matched gpt-5.3-codex -> gpt-5.2-codex", "warn"))
}

func TestGetModelPricing_Gpt54UsesStaticFallbackWhenRemoteMissing(t *testing.T) {
	svc := &PricingService{
		pricingData: map[string]*LiteLLMModelPricing{
			"gpt-5.1-codex": &LiteLLMModelPricing{InputCostPerToken: 1.25e-6},
		},
	}

	got := svc.GetModelPricing("gpt-5.4")
	require.NotNil(t, got)
	require.InDelta(t, 2.5e-6, got.InputCostPerToken, 1e-12)
	require.InDelta(t, 1.5e-5, got.OutputCostPerToken, 1e-12)
	require.InDelta(t, 2.5e-7, got.CacheReadInputTokenCost, 1e-12)
	require.Equal(t, 272000, got.LongContextInputTokenThreshold)
	require.InDelta(t, 2.0, got.LongContextInputCostMultiplier, 1e-12)
	require.InDelta(t, 1.5, got.LongContextOutputCostMultiplier, 1e-12)
}

func TestGetModelPricing_OpenAICompactAliasUsesStaticFallback(t *testing.T) {
	svc := &PricingService{
		pricingData: map[string]*LiteLLMModelPricing{
			"gpt-5.1-codex": {InputCostPerToken: 1.25e-6},
		},
	}

	got := svc.GetModelPricing("openai/gpt5.5")
	require.NotNil(t, got)
	require.InDelta(t, 2.5e-6, got.InputCostPerToken, 1e-12)
	require.InDelta(t, 1.5e-5, got.OutputCostPerToken, 1e-12)
}

func TestGetModelPricing_Gpt54MiniUsesDedicatedStaticFallbackWhenRemoteMissing(t *testing.T) {
	svc := &PricingService{
		pricingData: map[string]*LiteLLMModelPricing{
			"gpt-5.1-codex": {InputCostPerToken: 1.25e-6},
		},
	}

	got := svc.GetModelPricing("gpt-5.4-mini")
	require.NotNil(t, got)
	require.InDelta(t, 7.5e-7, got.InputCostPerToken, 1e-12)
	require.InDelta(t, 4.5e-6, got.OutputCostPerToken, 1e-12)
	require.InDelta(t, 7.5e-8, got.CacheReadInputTokenCost, 1e-12)
	require.Zero(t, got.LongContextInputTokenThreshold)
}

func TestGetModelPricing_Gpt54NanoUsesDedicatedStaticFallbackWhenRemoteMissing(t *testing.T) {
	svc := &PricingService{
		pricingData: map[string]*LiteLLMModelPricing{
			"gpt-5.1-codex": {InputCostPerToken: 1.25e-6},
		},
	}

	got := svc.GetModelPricing("gpt-5.4-nano")
	require.NotNil(t, got)
	require.InDelta(t, 2e-7, got.InputCostPerToken, 1e-12)
	require.InDelta(t, 1.25e-6, got.OutputCostPerToken, 1e-12)
	require.InDelta(t, 2e-8, got.CacheReadInputTokenCost, 1e-12)
	require.Zero(t, got.LongContextInputTokenThreshold)
}

func TestGetModelPricing_ImageModelDoesNotFallbackToTextModel(t *testing.T) {
	imagePricing := &LiteLLMModelPricing{InputCostPerToken: 3}
	textPricing := &LiteLLMModelPricing{InputCostPerToken: 9}

	svc := &PricingService{
		pricingData: map[string]*LiteLLMModelPricing{
			"gpt-image-2": imagePricing,
			"gpt-5.4":     textPricing,
		},
	}

	got := svc.GetModelPricing("gpt-image-3")
	require.Same(t, imagePricing, got)
}

func TestParsePricingData_PreservesPriorityAndServiceTierFields(t *testing.T) {
	raw := map[string]any{
		"gpt-5.4": map[string]any{
			"input_cost_per_token":                 2.5e-6,
			"input_cost_per_token_priority":        5e-6,
			"output_cost_per_token":                15e-6,
			"output_cost_per_token_priority":       30e-6,
			"cache_read_input_token_cost":          0.25e-6,
			"cache_read_input_token_cost_priority": 0.5e-6,
			"supports_service_tier":                true,
			"supports_prompt_caching":              true,
			"litellm_provider":                     "openai",
			"mode":                                 "chat",
		},
	}
	body, err := json.Marshal(raw)
	require.NoError(t, err)

	svc := &PricingService{}
	pricingMap, err := svc.parsePricingData(body)
	require.NoError(t, err)

	pricing := pricingMap["gpt-5.4"]
	require.NotNil(t, pricing)
	require.InDelta(t, 2.5e-6, pricing.InputCostPerToken, 1e-12)
	require.InDelta(t, 5e-6, pricing.InputCostPerTokenPriority, 1e-12)
	require.InDelta(t, 15e-6, pricing.OutputCostPerToken, 1e-12)
	require.InDelta(t, 30e-6, pricing.OutputCostPerTokenPriority, 1e-12)
	require.InDelta(t, 0.25e-6, pricing.CacheReadInputTokenCost, 1e-12)
	require.InDelta(t, 0.5e-6, pricing.CacheReadInputTokenCostPriority, 1e-12)
	require.True(t, pricing.SupportsServiceTier)
}

func TestParsePricingData_PreservesServiceTierPriorityFields(t *testing.T) {
	svc := &PricingService{}
	pricingData, err := svc.parsePricingData([]byte(`{
		"gpt-5.4": {
			"input_cost_per_token": 0.0000025,
			"input_cost_per_token_priority": 0.000005,
			"output_cost_per_token": 0.000015,
			"output_cost_per_token_priority": 0.00003,
			"cache_read_input_token_cost": 0.00000025,
			"cache_read_input_token_cost_priority": 0.0000005,
			"supports_service_tier": true,
			"litellm_provider": "openai",
			"mode": "chat"
		}
	}`))
	require.NoError(t, err)

	pricing := pricingData["gpt-5.4"]
	require.NotNil(t, pricing)
	require.InDelta(t, 0.0000025, pricing.InputCostPerToken, 1e-12)
	require.InDelta(t, 0.000005, pricing.InputCostPerTokenPriority, 1e-12)
	require.InDelta(t, 0.000015, pricing.OutputCostPerToken, 1e-12)
	require.InDelta(t, 0.00003, pricing.OutputCostPerTokenPriority, 1e-12)
	require.InDelta(t, 0.00000025, pricing.CacheReadInputTokenCost, 1e-12)
	require.InDelta(t, 0.0000005, pricing.CacheReadInputTokenCostPriority, 1e-12)
	require.True(t, pricing.SupportsServiceTier)
}
