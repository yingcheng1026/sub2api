package dto

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestUsageLogFromService_IncludesOpenAIWSMode(t *testing.T) {
	t.Parallel()

	wsLog := &service.UsageLog{
		RequestID:    "req_1",
		Model:        "gpt-5.3-codex",
		OpenAIWSMode: true,
	}
	httpLog := &service.UsageLog{
		RequestID:    "resp_1",
		Model:        "gpt-5.3-codex",
		OpenAIWSMode: false,
	}

	require.True(t, UsageLogFromService(wsLog).OpenAIWSMode)
	require.False(t, UsageLogFromService(httpLog).OpenAIWSMode)
	require.True(t, UsageLogFromServiceAdmin(wsLog).OpenAIWSMode)
	require.False(t, UsageLogFromServiceAdmin(httpLog).OpenAIWSMode)
}

func TestUsageLogFromService_PrefersRequestTypeForLegacyFields(t *testing.T) {
	t.Parallel()

	log := &service.UsageLog{
		RequestID:    "req_2",
		Model:        "gpt-5.3-codex",
		RequestType:  service.RequestTypeWSV2,
		Stream:       false,
		OpenAIWSMode: false,
	}

	userDTO := UsageLogFromService(log)
	adminDTO := UsageLogFromServiceAdmin(log)

	require.Equal(t, "ws_v2", userDTO.RequestType)
	require.True(t, userDTO.Stream)
	require.True(t, userDTO.OpenAIWSMode)
	require.Equal(t, "ws_v2", adminDTO.RequestType)
	require.True(t, adminDTO.Stream)
	require.True(t, adminDTO.OpenAIWSMode)
}

func TestUsageCleanupTaskFromService_RequestTypeMapping(t *testing.T) {
	t.Parallel()

	requestType := int16(service.RequestTypeStream)
	task := &service.UsageCleanupTask{
		ID:     1,
		Status: service.UsageCleanupStatusPending,
		Filters: service.UsageCleanupFilters{
			RequestType: &requestType,
		},
	}

	dtoTask := UsageCleanupTaskFromService(task)
	require.NotNil(t, dtoTask)
	require.NotNil(t, dtoTask.Filters.RequestType)
	require.Equal(t, "stream", *dtoTask.Filters.RequestType)
}

func TestRequestTypeStringPtrNil(t *testing.T) {
	t.Parallel()
	require.Nil(t, requestTypeStringPtr(nil))
}

func TestUsageLogFromService_IncludesServiceTierForUserAndAdmin(t *testing.T) {
	t.Parallel()

	serviceTier := "priority"
	inboundEndpoint := "/v1/chat/completions"
	upstreamEndpoint := "/v1/responses"
	log := &service.UsageLog{
		RequestID:             "req_3",
		Model:                 "gpt-5.4",
		ServiceTier:           &serviceTier,
		InboundEndpoint:       &inboundEndpoint,
		UpstreamEndpoint:      &upstreamEndpoint,
		AccountRateMultiplier: f64Ptr(1.5),
	}

	userDTO := UsageLogFromService(log)
	adminDTO := UsageLogFromServiceAdmin(log)

	require.NotNil(t, userDTO.ServiceTier)
	require.Equal(t, serviceTier, *userDTO.ServiceTier)
	require.NotNil(t, userDTO.InboundEndpoint)
	require.Equal(t, inboundEndpoint, *userDTO.InboundEndpoint)
	require.NotNil(t, userDTO.UpstreamEndpoint)
	require.Equal(t, upstreamEndpoint, *userDTO.UpstreamEndpoint)
	require.NotNil(t, adminDTO.ServiceTier)
	require.Equal(t, serviceTier, *adminDTO.ServiceTier)
	require.NotNil(t, adminDTO.InboundEndpoint)
	require.Equal(t, inboundEndpoint, *adminDTO.InboundEndpoint)
	require.NotNil(t, adminDTO.UpstreamEndpoint)
	require.Equal(t, upstreamEndpoint, *adminDTO.UpstreamEndpoint)
	require.NotNil(t, adminDTO.AccountRateMultiplier)
	require.InDelta(t, 1.5, *adminDTO.AccountRateMultiplier, 1e-12)
}

func TestUsageLogFromService_UsesUpstreamModelAndKeepsUpstreamAdminOnly(t *testing.T) {
	t.Parallel()

	upstreamModel := "gpt-5.4"
	log := &service.UsageLog{
		RequestID:      "req_4",
		Model:          "claude-sonnet-4-6",
		RequestedModel: "claude-sonnet-4-6",
		UpstreamModel:  &upstreamModel,
	}

	userDTO := UsageLogFromService(log)
	adminDTO := UsageLogFromServiceAdmin(log)

	require.Equal(t, "gpt-5.4", userDTO.Model)
	require.Equal(t, "gpt-5.4", adminDTO.Model)

	userJSON, err := json.Marshal(userDTO)
	require.NoError(t, err)
	require.NotContains(t, string(userJSON), "upstream_model")

	adminJSON, err := json.Marshal(adminDTO)
	require.NoError(t, err)
	require.Contains(t, string(adminJSON), `"upstream_model":"gpt-5.4"`)
}

func TestUsageLogFromService_FallsBackToStoredModelWhenUpstreamMissing(t *testing.T) {
	t.Parallel()

	log := &service.UsageLog{
		RequestID:      "req_model_fallback",
		Model:          "gpt-5.4",
		RequestedModel: "claude-sonnet-4-6",
	}

	userDTO := UsageLogFromService(log)
	adminDTO := UsageLogFromServiceAdmin(log)

	require.Equal(t, "gpt-5.4", userDTO.Model)
	require.Equal(t, "gpt-5.4", adminDTO.Model)
}

func TestUsageLogFromServiceAdmin_IncludesBillingIdentityWithoutLeakingUserAuditFields(t *testing.T) {
	t.Parallel()

	upstreamModel := "gpt-5.4"
	billingModel := "gpt-5.4"
	log := &service.UsageLog{
		RequestID:      "req_billing_identity",
		Model:          "gpt-5.4",
		RequestedModel: "claude-sonnet-4-6",
		UpstreamModel:  &upstreamModel,
		BillingModel:   &billingModel,
	}

	userJSON, err := json.Marshal(UsageLogFromService(log))
	require.NoError(t, err)
	require.NotContains(t, string(userJSON), "requested_model")
	require.NotContains(t, string(userJSON), "billing_model")
	require.NotContains(t, string(userJSON), "compat_mode")

	adminJSON, err := json.Marshal(UsageLogFromServiceAdmin(log))
	require.NoError(t, err)
	for _, expected := range []string{
		`"model":"gpt-5.4"`,
		`"requested_model":"claude-sonnet-4-6"`,
		`"upstream_model":"gpt-5.4"`,
		`"billing_model":"gpt-5.4"`,
		`"compat_mode":"legacy_claude_alias"`,
	} {
		require.Contains(t, string(adminJSON), expected)
	}
}

func TestUsageLogMapperPricingEvidenceIsAdminOnly(t *testing.T) {
	t.Parallel()

	source := service.PricingSourceBuiltinGPT56
	revision := service.GPT56PricingRevision
	hash := strings.Repeat("b", 64)
	log := &service.UsageLog{
		RequestID: "req-pricing-evidence", Model: "gpt-5.6-terra",
		PricingSource: &source, PricingRevision: &revision, PricingHash: &hash,
	}

	userJSON, err := json.Marshal(UsageLogFromService(log))
	require.NoError(t, err)
	for _, field := range []string{"pricing_source", "pricing_revision", "pricing_hash"} {
		require.NotContains(t, string(userJSON), field)
	}

	adminJSON, err := json.Marshal(UsageLogFromServiceAdmin(log))
	require.NoError(t, err)
	require.Contains(t, string(adminJSON), `"pricing_source":"builtin_gpt56"`)
	require.Contains(t, string(adminJSON), `"pricing_revision":"gpt56-policy-v1"`)
	require.Contains(t, string(adminJSON), `"pricing_hash":"`+hash+`"`)
}

func TestUsageLogFromServiceAdmin_DerivesCompatModeFromStoredIdentity(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		requestedModel string
		executedModel  string
		billingModel   *string
		want           string
	}{
		{
			name:           "native GPT",
			requestedModel: "gpt-5.6-terra",
			executedModel:  "gpt-5.6-terra",
			billingModel:   stringPtr("gpt-5.6-terra"),
			want:           "native_gpt",
		},
		{
			name:           "legacy Claude alias",
			requestedModel: "claude-sonnet-4-6",
			executedModel:  "gpt-5.4",
			billingModel:   stringPtr("gpt-5.4"),
			want:           "legacy_claude_alias",
		},
		{
			name:           "bare opus alias",
			requestedModel: "opus",
			executedModel:  "gpt-5.6-sol",
			billingModel:   stringPtr("gpt-5.6-sol"),
			want:           "legacy_claude_alias",
		},
		{
			name:           "bare sonnet alias",
			requestedModel: "sonnet",
			executedModel:  "gpt-5.6-terra",
			billingModel:   stringPtr("gpt-5.6-terra"),
			want:           "legacy_claude_alias",
		},
		{
			name:           "bare haiku alias",
			requestedModel: "haiku",
			executedModel:  "gpt-5.6-luna",
			billingModel:   stringPtr("gpt-5.6-luna"),
			want:           "legacy_claude_alias",
		},
		{
			name:           "bare default alias",
			requestedModel: "default",
			executedModel:  "gpt-5.4",
			billingModel:   stringPtr("gpt-5.4"),
			want:           "legacy_claude_alias",
		},
		{
			name:           "historical audit missing",
			requestedModel: "claude-sonnet-4-6",
			executedModel:  "gpt-5.4",
			want:           "other",
		},
		{
			name:           "other provider",
			requestedModel: "gemini-3-pro",
			executedModel:  "gemini-3-pro",
			billingModel:   stringPtr("gemini-3-pro"),
			want:           "other",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt := tt
			dto := UsageLogFromServiceAdmin(&service.UsageLog{
				Model:          tt.executedModel,
				RequestedModel: tt.requestedModel,
				BillingModel:   tt.billingModel,
			})
			require.Equal(t, tt.want, dto.CompatMode)
		})
	}
}

func stringPtr(value string) *string {
	return &value
}

func f64Ptr(value float64) *float64 {
	return &value
}
