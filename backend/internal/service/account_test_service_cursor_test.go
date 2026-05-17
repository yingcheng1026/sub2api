package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type cursorAccountTestRepo struct {
	AccountRepository
	account *Account
}

func (r *cursorAccountTestRepo) GetByID(_ context.Context, id int64) (*Account, error) {
	if r.account != nil && r.account.ID == id {
		return r.account, nil
	}
	return nil, errors.New("account not found")
}

func TestAccountTestService_CursorUsesSidecarAccountRef(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("CURSOR_SIDECAR_URL", "")
	t.Setenv("CURSOR_SIDECAR_API_KEY", "")
	t.Setenv("CURSOR_REQUEST_TIMEOUT_SECONDS", "")

	var seenPath string
	var seenAccountRef string
	var seenAccountID string
	var seenSidecarKey string
	var seenBearer string
	var seenModel string
	var seenPrompt string

	sidecar := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenPath = r.URL.Path
		seenAccountRef = r.Header.Get("X-Cursor-Account-Ref")
		seenAccountID = r.Header.Get("X-Cursor-Account-ID")
		seenSidecarKey = r.Header.Get("X-Cursor-Sidecar-Key")
		seenBearer = r.Header.Get("Authorization")

		var body struct {
			Model    string `json:"model"`
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		seenModel = body.Model
		if len(body.Messages) > 0 {
			seenPrompt = body.Messages[0].Content
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"cursor-ok"}}]}`))
	}))
	defer sidecar.Close()

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/admin/accounts/83/test", nil)

	account := &Account{
		ID:          83,
		Platform:    PlatformCursor,
		Type:        AccountTypeUpstream,
		Concurrency: 1,
		Credentials: map[string]any{"sidecar_account_ref": "cursor-prod@example.com"},
	}
	svc := &AccountTestService{
		accountRepo: &cursorAccountTestRepo{account: account},
		cfg: &config.Config{Cursor: config.CursorConfig{
			SidecarURL:            sidecar.URL + "/internal",
			SidecarAPIKey:         "sidecar-key",
			RequestTimeoutSeconds: 3,
		}},
	}

	err := svc.TestAccountConnection(c, 83, "claude-sonnet-4-6", "hi", AccountTestModeDefault)

	require.NoError(t, err)
	require.Equal(t, "/internal/v1/chat/completions", seenPath)
	require.Equal(t, "cursor-prod@example.com", seenAccountRef)
	require.Equal(t, "83", seenAccountID)
	require.Equal(t, "sidecar-key", seenSidecarKey)
	require.Equal(t, "Bearer sidecar-key", seenBearer)
	require.Equal(t, DefaultCursorTestModel, seenModel)
	require.Equal(t, "hi", seenPrompt)
	require.Contains(t, rec.Body.String(), "cursor-ok")
	require.Contains(t, rec.Body.String(), `"type":"test_complete"`)
}

func TestNormalizeCursorTestModelHonorsCursorMapping(t *testing.T) {
	account := &Account{
		Credentials: map[string]any{
			"model_mapping": map[string]any{
				"claude-sonnet-4-6": "cursor-gpt-5.5-high",
			},
		},
	}

	require.Equal(t, "cursor-gpt-5.5-high", normalizeCursorTestModel(account, "claude-sonnet-4-6"))
	require.Equal(t, DefaultCursorTestModel, normalizeCursorTestModel(&Account{}, "claude-sonnet-4-6"))
	require.Equal(t, "cursor-composer-2", normalizeCursorTestModel(&Account{}, "cursor-composer-2"))
}

func TestAccountTestService_CursorRequiresSidecarConfigured(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("CURSOR_SIDECAR_URL", "")
	t.Setenv("CURSOR_SIDECAR_API_KEY", "")
	t.Setenv("CURSOR_REQUEST_TIMEOUT_SECONDS", "")

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/admin/accounts/84/test", nil)

	account := &Account{
		ID:          84,
		Platform:    PlatformCursor,
		Type:        AccountTypeUpstream,
		Concurrency: 1,
		Credentials: map[string]any{"sidecar_account_ref": "cursor-prod@example.com"},
	}
	svc := &AccountTestService{
		accountRepo: &cursorAccountTestRepo{account: account},
		cfg:         &config.Config{},
	}

	err := svc.TestAccountConnection(c, 84, "claude-sonnet-4-6", "hi", AccountTestModeDefault)

	require.Error(t, err)
	require.Contains(t, err.Error(), "cursor sidecar is not configured")
	require.Contains(t, strings.TrimSpace(rec.Body.String()), "cursor sidecar is not configured")
}
