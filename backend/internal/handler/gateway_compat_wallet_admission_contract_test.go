package handler

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGatewayCompatibilityEndpointsFenceWalletUsageBeforeTransport(t *testing.T) {
	for _, path := range []string{
		"gateway_handler_responses.go",
		"gateway_handler_chat_completions.go",
	} {
		t.Run(path, func(t *testing.T) {
			raw, err := os.ReadFile(path)
			require.NoError(t, err)
			source := string(raw)
			for _, required := range []string{
				"PrepareUsageBillingRequestContext",
				"PrepareGatewayWalletUsageBillingAdmission",
				"MarkGatewayUsageBillingAttemptFailed",
				"MarkGatewayUsageBillingOrphaned",
				"AbandonGatewayUsageBilling",
				"result.UsageBillingIdentity = usageBillingIdentity",
				"submitGatewayUsageRecordTask",
				"RequestPayloadHash: requestPayloadHash",
			} {
				require.Contains(t, source, required)
			}
		})
	}
}

func TestGeminiGatewayEndpointsFenceWalletUsageBeforeTransport(t *testing.T) {
	for _, path := range []string{
		"gemini_v1beta_handler.go",
		"gateway_handler.go",
	} {
		t.Run(path, func(t *testing.T) {
			raw, err := os.ReadFile(path)
			require.NoError(t, err)
			source := string(raw)
			for _, required := range []string{
				"PrepareUsageBillingRequestContext",
				"PrepareGatewayWalletUsageBillingAdmission",
				"MarkGatewayUsageBillingAttemptFailed",
				"MarkGatewayUsageBillingOrphaned",
				"AbandonGatewayUsageBilling",
				"result.UsageBillingIdentity = usageBillingIdentity",
			} {
				require.Contains(t, source, required)
			}
		})
	}
}

func TestSidecarGatewayEndpointsFenceWalletUsageBeforeTransport(t *testing.T) {
	for _, path := range []string{
		"kiro_gateway_handler.go",
		"cursor_gateway_handler.go",
	} {
		t.Run(path, func(t *testing.T) {
			raw, err := os.ReadFile(path)
			require.NoError(t, err)
			source := string(raw)
			for _, required := range []string{
				"PrepareUsageBillingRequestContext",
				"PrepareGatewayWalletUsageBillingAdmission",
				"MarkGatewayUsageBillingAttemptFailed",
				"MarkGatewayUsageBillingOrphaned",
				"AbandonGatewayUsageBilling",
				"result.UsageBillingIdentity = usageBillingIdentity",
				"RejectUnmeteredStream",
				"billing_service_error",
				"billing_request_conflict",
			} {
				require.Contains(t, source, required)
			}
		})
	}
}
