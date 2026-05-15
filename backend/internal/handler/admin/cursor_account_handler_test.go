package admin

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestAccountHandlerProvisionCursorAccountCreatesSidecarAndMainAccount(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var sidecarAuth string
	var sidecarPayload map[string]any
	sidecar := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)
		require.Equal(t, "/admin/accounts/cursor", r.URL.Path)
		sidecarAuth = r.Header.Get("Authorization")
		require.NoError(t, json.NewDecoder(r.Body).Decode(&sidecarPayload))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"provider":"cursor","account_ref":"cursor-account@example.com","email":"cursor-account@example.com","expires_at":"2026-05-15T11:00:00Z","cursor_client_version":"3.3.30","cursor_membership_type":"pro"}`))
	}))
	t.Cleanup(sidecar.Close)
	t.Setenv("CURSOR_SIDECAR_URL", sidecar.URL)
	t.Setenv("CURSOR_SIDECAR_API_KEY", "sidecar-secret")

	adminSvc := newStubAdminService()
	handler := NewAccountHandler(adminSvc, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	router := gin.New()
	router.POST("/api/v1/admin/accounts/cursor-sidecar", handler.ProvisionCursorAccount)

	raw, err := json.Marshal(map[string]any{
		"name":                      "cursor-prod-002",
		"email":                     "cursor-account@example.com",
		"access_token":              "cursor-access",
		"refresh_token":             "cursor-refresh",
		"cursor_token_expires_at":   "2026-05-15T11:00:00Z",
		"cursor_service_machine_id": "machine-1",
		"cursor_client_version":     "3.3.30",
		"group_ids":                 []int64{21},
		"concurrency":               1,
	})
	require.NoError(t, err)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/accounts/cursor-sidecar", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "Bearer sidecar-secret", sidecarAuth)
	require.Equal(t, "cursor-account@example.com", sidecarPayload["email"])
	require.Equal(t, "cursor-access", sidecarPayload["access_token"])
	require.Equal(t, "cursor-refresh", sidecarPayload["refresh_token"])
	require.Len(t, adminSvc.createdAccounts, 1)

	created := adminSvc.createdAccounts[0]
	require.Equal(t, "cursor-prod-002", created.Name)
	require.Equal(t, "cursor", created.Platform)
	require.Equal(t, "upstream", created.Type)
	require.Equal(t, []int64{int64(21)}, created.GroupIDs)
	require.Equal(t, "cursor-account@example.com", created.Credentials["sidecar_account_ref"])
	require.Equal(t, "cursor-account@example.com", created.Extra["cursor_email"])
	require.Equal(t, "pro", created.Extra["cursor_membership_type"])
}
