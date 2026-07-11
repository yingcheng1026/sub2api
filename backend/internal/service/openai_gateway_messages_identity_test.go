package service

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestForwardAsAnthropic_ModelIdentityMatrix(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		accountType    string
		requestedModel string
		mappedModel    string
		stream         bool
		toolResponse   bool
	}{
		{name: "api_key_native_sol_buffered", accountType: AccountTypeAPIKey, requestedModel: "gpt-5.6-sol", mappedModel: "gpt-5.6-sol"},
		{name: "api_key_native_terra_stream", accountType: AccountTypeAPIKey, requestedModel: "gpt-5.6-terra", mappedModel: "gpt-5.6-terra", stream: true},
		{name: "api_key_native_luna_buffered", accountType: AccountTypeAPIKey, requestedModel: "gpt-5.6-luna", mappedModel: "gpt-5.6-luna"},
		{name: "oauth_native_sol_stream", accountType: AccountTypeOAuth, requestedModel: "gpt-5.6-sol", mappedModel: "gpt-5.6-sol", stream: true},
		{name: "oauth_native_terra_buffered", accountType: AccountTypeOAuth, requestedModel: "gpt-5.6-terra", mappedModel: "gpt-5.6-terra"},
		{name: "oauth_native_luna_stream", accountType: AccountTypeOAuth, requestedModel: "gpt-5.6-luna", mappedModel: "gpt-5.6-luna", stream: true},
		{name: "api_key_legacy_alias_buffered", accountType: AccountTypeAPIKey, requestedModel: "claude-sonnet-4-6", mappedModel: "gpt-5.6-terra", toolResponse: true},
		{name: "api_key_legacy_alias_stream", accountType: AccountTypeAPIKey, requestedModel: "claude-sonnet-4-6", mappedModel: "gpt-5.6-terra", stream: true, toolResponse: true},
		{name: "oauth_legacy_alias_buffered", accountType: AccountTypeOAuth, requestedModel: "claude-sonnet-4-6", mappedModel: "gpt-5.6-terra", toolResponse: true},
		{name: "oauth_legacy_alias_stream", accountType: AccountTypeOAuth, requestedModel: "claude-sonnet-4-6", mappedModel: "gpt-5.6-terra", stream: true, toolResponse: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			body := []byte(fmt.Sprintf(`{"model":%q,"max_tokens":64,"messages":[{"role":"user","content":"local identity test"}],"stream":%t}`, tt.requestedModel, tt.stream))
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))
			c.Request.Header.Set("Content-Type", "application/json")

			upstream := &httpUpstreamRecorder{resp: openAIIdentitySSE(tt.mappedModel, tt.toolResponse)}
			svc := &OpenAIGatewayService{
				httpUpstream: upstream,
				cfg: &config.Config{Security: config.SecurityConfig{
					URLAllowlist: config.URLAllowlistConfig{Enabled: false},
				}},
			}
			account := openAIIdentityTestAccount(tt.accountType, tt.requestedModel, tt.mappedModel)

			defaultMappedModel := "gpt-5.4"
			if strings.HasPrefix(tt.requestedModel, "claude-") {
				defaultMappedModel = ""
			}
			result, err := svc.ForwardAsAnthropic(context.Background(), c, account, body, "local-identity-session", defaultMappedModel)
			require.NoError(t, err)
			require.NotNil(t, result)

			require.Equal(t, tt.requestedModel, result.Model, "product-visible result identity must match the client request")
			require.Equal(t, tt.mappedModel, result.BillingModel, "billing identity must match the selected GPT tier")
			require.Equal(t, tt.mappedModel, result.UpstreamModel, "upstream identity must match the exact GPT tier")
			require.Equal(t, tt.stream, result.Stream)
			require.Equal(t, tt.mappedModel, gjson.GetBytes(upstream.lastBody, "model").String(), "upstream JSON must retain the exact GPT tier")
			require.NotEqual(t, "gpt-5.4", gjson.GetBytes(upstream.lastBody, "model").String(), "GPT-5.6 must not be downgraded by the OAuth transform")

			if tt.accountType == AccountTypeOAuth {
				require.Equal(t, "Bearer oauth-local-test", upstream.lastReq.Header.Get("Authorization"))
				require.Equal(t, "https://chatgpt.com/backend-api/codex/responses", upstream.lastReq.URL.String())
			} else {
				require.Equal(t, "Bearer sk-local-test", upstream.lastReq.Header.Get("Authorization"))
				require.Equal(t, "https://api.openai.com/v1/responses", upstream.lastReq.URL.String())
			}

			if tt.stream {
				require.Contains(t, rec.Body.String(), `"type":"message_start"`)
				require.Contains(t, rec.Body.String(), fmt.Sprintf(`"model":%q`, tt.requestedModel))
			} else {
				require.Equal(t, tt.requestedModel, gjson.GetBytes(rec.Body.Bytes(), "model").String())
			}

			if tt.toolResponse {
				if tt.stream {
					require.Contains(t, rec.Body.String(), `"type":"tool_use"`)
					require.Contains(t, rec.Body.String(), `"name":"Read"`)
					require.Contains(t, rec.Body.String(), `\"file_path\":\"README.md\"`)
				} else {
					require.Equal(t, "tool_use", gjson.GetBytes(rec.Body.Bytes(), "content.0.type").String())
					require.Equal(t, "Read", gjson.GetBytes(rec.Body.Bytes(), "content.0.name").String())
					require.Equal(t, "README.md", gjson.GetBytes(rec.Body.Bytes(), "content.0.input.file_path").String())
				}
			}
		})
	}
}

func openAIIdentityTestAccount(accountType, requestedModel, mappedModel string) *Account {
	mapping := map[string]any{requestedModel: mappedModel}
	if tier, isGPT56Family := classifyOpenAIGPT56PreviewModel(mappedModel); isGPT56Family && tier != "" {
		mapping[tier] = tier
	}
	credentials := map[string]any{"model_mapping": mapping}
	if accountType == AccountTypeOAuth {
		credentials["access_token"] = "oauth-local-test"
		credentials["chatgpt_account_id"] = "chatgpt-local-test"
	} else {
		credentials["api_key"] = "sk-local-test"
	}
	return &Account{
		ID:          9401,
		Name:        "local-openai-identity",
		Platform:    PlatformOpenAI,
		Type:        accountType,
		Concurrency: 1,
		Credentials: credentials,
		Status:      StatusActive,
		Schedulable: true,
	}
}

func openAIIdentitySSE(model string, toolResponse bool) *http.Response {
	var events []string
	if toolResponse {
		events = []string{
			fmt.Sprintf(`data: {"type":"response.created","response":{"id":"resp_identity","object":"response","model":%q,"status":"in_progress"}}`, model),
			`data: {"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","call_id":"call_identity","name":"Read","status":"in_progress"}}`,
			`data: {"type":"response.function_call_arguments.done","output_index":0,"arguments":"{\"file_path\":\"README.md\",\"pages\":\"\"}"}`,
			`data: {"type":"response.output_item.done","output_index":0,"item":{"type":"function_call","call_id":"call_identity","name":"Read","arguments":"{\"file_path\":\"README.md\",\"pages\":\"\"}","status":"completed"}}`,
			fmt.Sprintf(`data: {"type":"response.completed","response":{"id":"resp_identity","object":"response","model":%q,"status":"completed","output":[{"type":"function_call","call_id":"call_identity","name":"Read","arguments":"{\"file_path\":\"README.md\",\"pages\":\"\"}","status":"completed"}],"usage":{"input_tokens":7,"output_tokens":3,"total_tokens":10}}}`, model),
		}
	} else {
		events = []string{
			fmt.Sprintf(`data: {"type":"response.created","response":{"id":"resp_identity","object":"response","model":%q,"status":"in_progress"}}`, model),
			`data: {"type":"response.output_text.delta","output_index":0,"content_index":0,"delta":"local identity ok"}`,
			`data: {"type":"response.output_text.done","output_index":0,"content_index":0,"text":"local identity ok"}`,
			fmt.Sprintf(`data: {"type":"response.completed","response":{"id":"resp_identity","object":"response","model":%q,"status":"completed","output":[{"type":"message","id":"msg_identity","role":"assistant","status":"completed","content":[{"type":"output_text","text":"local identity ok"}]}],"usage":{"input_tokens":7,"output_tokens":3,"total_tokens":10}}}`, model),
		}
	}
	events = append(events, "data: [DONE]", "")
	return &http.Response{
		StatusCode: http.StatusOK,
		Header: http.Header{
			"Content-Type": []string{"text/event-stream"},
			"x-request-id": []string{"rid_identity"},
		},
		Body: io.NopCloser(strings.NewReader(strings.Join(events, "\n\n"))),
	}
}
