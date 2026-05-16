package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	appconfig "github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

const cursorSidecarAdminCreatePath = "/admin/accounts/cursor"

type ProvisionCursorAccountRequest struct {
	Name                    string         `json:"name" binding:"required"`
	Notes                   *string        `json:"notes"`
	Email                   string         `json:"email" binding:"required"`
	AccessToken             string         `json:"access_token" binding:"required"`
	RefreshToken            string         `json:"refresh_token" binding:"required"`
	CursorTokenExpiresAt    string         `json:"cursor_token_expires_at"`
	AccountUUID             string         `json:"account_uuid"`
	CursorServiceMachineID  string         `json:"cursor_service_machine_id"`
	CursorClientVersion     string         `json:"cursor_client_version"`
	CursorConfigVersion     string         `json:"cursor_config_version"`
	CursorClientID          string         `json:"cursor_client_id"`
	CursorMembershipType    string         `json:"cursor_membership_type"`
	Extra                   map[string]any `json:"extra"`
	ProxyID                 *int64         `json:"proxy_id"`
	Concurrency             int            `json:"concurrency"`
	Priority                int            `json:"priority"`
	RateMultiplier          *float64       `json:"rate_multiplier"`
	LoadFactor              *int           `json:"load_factor"`
	GroupIDs                []int64        `json:"group_ids"`
	ExpiresAt               *int64         `json:"expires_at"`
	AutoPauseOnExpired      *bool          `json:"auto_pause_on_expired"`
	ConfirmMixedChannelRisk *bool          `json:"confirm_mixed_channel_risk"`
}

type cursorSidecarProvisionResponse struct {
	Provider               string `json:"provider"`
	AccountRef             string `json:"account_ref"`
	Email                  string `json:"email"`
	ExpiresAt              string `json:"expires_at"`
	AccountUUID            string `json:"account_uuid"`
	CursorServiceMachineID string `json:"cursor_service_machine_id"`
	CursorClientVersion    string `json:"cursor_client_version"`
	CursorMembershipType   string `json:"cursor_membership_type"`
	GeneratedAt            string `json:"generated_at"`
}

// ProvisionCursorAccount writes Cursor credentials into the sidecar, reloads
// its in-memory pool, then creates the matching Sub2API account with only a
// sidecar account reference persisted in the main database.
func (h *AccountHandler) ProvisionCursorAccount(c *gin.Context) {
	var req ProvisionCursorAccountRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	if req.RateMultiplier != nil && *req.RateMultiplier < 0 {
		response.BadRequest(c, "rate_multiplier must be >= 0")
		return
	}
	if strings.TrimSpace(req.Email) == "" || !strings.Contains(req.Email, "@") {
		response.BadRequest(c, "email is required")
		return
	}

	sanitizeExtraBaseRPM(req.Extra)
	skipCheck := req.ConfirmMixedChannelRisk != nil && *req.ConfirmMixedChannelRisk

	result, err := executeAdminIdempotent(c, "admin.accounts.cursor.provision", req, service.DefaultWriteIdempotencyTTL(), func(ctx context.Context) (any, error) {
		sidecarAccount, execErr := provisionCursorSidecarAccount(ctx, req)
		if execErr != nil {
			return nil, execErr
		}

		extra := cloneCursorProvisionExtra(req.Extra)
		if sidecarAccount.Email != "" {
			extra["cursor_email"] = sidecarAccount.Email
		}
		if sidecarAccount.ExpiresAt != "" {
			extra["cursor_token_expires_at"] = sidecarAccount.ExpiresAt
		}
		if sidecarAccount.CursorMembershipType != "" {
			extra["cursor_membership_type"] = sidecarAccount.CursorMembershipType
		}

		account, execErr := h.adminService.CreateAccount(ctx, &service.CreateAccountInput{
			Name:                  req.Name,
			Notes:                 req.Notes,
			Platform:              service.PlatformCursor,
			Type:                  service.AccountTypeUpstream,
			Credentials:           map[string]any{"sidecar_account_ref": sidecarAccount.AccountRef},
			Extra:                 extra,
			ProxyID:               req.ProxyID,
			Concurrency:           req.Concurrency,
			Priority:              req.Priority,
			RateMultiplier:        req.RateMultiplier,
			LoadFactor:            req.LoadFactor,
			GroupIDs:              req.GroupIDs,
			ExpiresAt:             req.ExpiresAt,
			AutoPauseOnExpired:    req.AutoPauseOnExpired,
			SkipMixedChannelCheck: skipCheck,
		})
		if execErr != nil {
			return nil, execErr
		}

		return gin.H{
			"account":        h.buildAccountResponseWithRuntime(ctx, account),
			"cursor_sidecar": sidecarAccount,
		}, nil
	})
	if err != nil {
		var mixedErr *service.MixedChannelError
		if errors.As(err, &mixedErr) {
			c.JSON(http.StatusConflict, gin.H{
				"error":   "mixed_channel_warning",
				"message": mixedErr.Error(),
			})
			return
		}
		response.ErrorFrom(c, err)
		return
	}

	if result != nil && result.Replayed {
		c.Header("X-Idempotency-Replayed", "true")
	}
	response.Success(c, result.Data)
}

func provisionCursorSidecarAccount(ctx context.Context, req ProvisionCursorAccountRequest) (*cursorSidecarProvisionResponse, error) {
	sidecarURL, err := cursorSidecarAdminURL()
	if err != nil {
		return nil, err
	}

	payload := map[string]any{
		"email":                     strings.TrimSpace(req.Email),
		"access_token":              strings.TrimSpace(req.AccessToken),
		"refresh_token":             strings.TrimSpace(req.RefreshToken),
		"expires_at":                strings.TrimSpace(req.CursorTokenExpiresAt),
		"account_uuid":              strings.TrimSpace(req.AccountUUID),
		"cursor_service_machine_id": strings.TrimSpace(req.CursorServiceMachineID),
		"cursor_client_version":     strings.TrimSpace(req.CursorClientVersion),
		"cursor_config_version":     strings.TrimSpace(req.CursorConfigVersion),
		"cursor_client_id":          strings.TrimSpace(req.CursorClientID),
		"cursor_membership_type":    strings.TrimSpace(req.CursorMembershipType),
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, sidecarURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if key := strings.TrimSpace(os.Getenv("CURSOR_SIDECAR_API_KEY")); key != "" {
		httpReq.Header.Set("Authorization", "Bearer "+key)
		httpReq.Header.Set("x-api-key", key)
		httpReq.Header.Set("X-Cursor-Sidecar-Key", key)
	}

	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("cursor sidecar provisioning request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 1024*1024))
	if err != nil {
		return nil, fmt.Errorf("cursor sidecar provisioning response read failed: %w", err)
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("cursor sidecar provisioning failed: status %d: %s", resp.StatusCode, cursorSidecarSafeError(respBody))
	}

	var sidecarAccount cursorSidecarProvisionResponse
	if err := json.Unmarshal(respBody, &sidecarAccount); err != nil {
		return nil, fmt.Errorf("cursor sidecar provisioning returned invalid json: %w", err)
	}
	if strings.TrimSpace(sidecarAccount.AccountRef) == "" {
		return nil, errors.New("cursor sidecar provisioning did not return account_ref")
	}
	return &sidecarAccount, nil
}

func cursorSidecarAdminURL() (string, error) {
	raw := strings.TrimSpace(os.Getenv("CURSOR_SIDECAR_URL"))
	if raw == "" {
		return "", errors.New("CURSOR_SIDECAR_URL is not configured")
	}
	if err := appconfig.ValidateAbsoluteHTTPURL(raw); err != nil {
		return "", err
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	if u.User != nil || u.RawQuery != "" || u.ForceQuery {
		return "", errors.New("CURSOR_SIDECAR_URL must not include userinfo or query")
	}
	u.Path = strings.TrimRight(u.Path, "/") + cursorSidecarAdminCreatePath
	u.RawQuery = ""
	u.Fragment = ""
	return u.String(), nil
}

func cursorSidecarSafeError(body []byte) string {
	var parsed struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(body, &parsed); err == nil {
		if msg := strings.TrimSpace(parsed.Error.Message); msg != "" {
			return msg
		}
		if msg := strings.TrimSpace(parsed.Message); msg != "" {
			return msg
		}
	}
	text := strings.TrimSpace(string(body))
	if len(text) > 300 {
		text = text[:300]
	}
	if text == "" {
		return "empty response body"
	}
	return strconv.Quote(text)
}

func cloneCursorProvisionExtra(extra map[string]any) map[string]any {
	cloned := make(map[string]any, len(extra)+3)
	for key, value := range extra {
		cloned[key] = value
	}
	return cloned
}
