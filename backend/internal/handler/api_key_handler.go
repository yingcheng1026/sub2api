// Package handler provides HTTP request handlers for the application.
package handler

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/handler/dto"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

// APIKeyHandler handles API key-related requests
type APIKeyHandler struct {
	apiKeyService      *service.APIKeyService
	modelRouteProvider service.ModelRouteProvider
	totpService        *service.TotpService
}

// NewAPIKeyHandler creates a new APIKeyHandler
func NewAPIKeyHandler(apiKeyService *service.APIKeyService) *APIKeyHandler {
	return NewAPIKeyHandlerWithModelRoutes(apiKeyService, nil)
}

func NewAPIKeyHandlerWithModelRoutes(apiKeyService *service.APIKeyService, modelRouteProvider service.ModelRouteProvider) *APIKeyHandler {
	return &APIKeyHandler{
		apiKeyService:      apiKeyService,
		modelRouteProvider: modelRouteProvider,
	}
}

func ProvideAPIKeyHandler(apiKeyService *service.APIKeyService, modelRouteProvider service.ModelRouteProvider, totpService *service.TotpService) *APIKeyHandler {
	h := NewAPIKeyHandlerWithModelRoutes(apiKeyService, modelRouteProvider)
	h.totpService = totpService
	return h
}

// CreateAPIKeyRequest represents the create API key request payload
type CreateAPIKeyRequest struct {
	Name          string   `json:"name" binding:"required"`
	Verification  string   `json:"verification" binding:"required,max=256"`
	GroupID       *int64   `json:"group_id"`        // nullable
	CustomKey     *string  `json:"custom_key"`      // 可选的自定义key
	IPWhitelist   []string `json:"ip_whitelist"`    // IP 白名单
	IPBlacklist   []string `json:"ip_blacklist"`    // IP 黑名单
	Quota         *float64 `json:"quota"`           // 配额限制 (USD)
	ExpiresInDays *int     `json:"expires_in_days"` // 过期天数

	// Rate limit fields (0 = unlimited)
	RateLimit5h *float64 `json:"rate_limit_5h"`
	RateLimit1d *float64 `json:"rate_limit_1d"`
	RateLimit7d *float64 `json:"rate_limit_7d"`
}

// UpdateAPIKeyRequest represents the update API key request payload
type UpdateAPIKeyRequest struct {
	Name        string    `json:"name"`
	GroupID     *int64    `json:"group_id"`
	Status      string    `json:"status" binding:"omitempty,oneof=active inactive"`
	IPWhitelist *[]string `json:"ip_whitelist"` // 省略=不修改，显式 []=清空
	IPBlacklist *[]string `json:"ip_blacklist"` // 省略=不修改，显式 []=清空
	Quota       *float64  `json:"quota"`        // 配额限制 (USD), 0=无限制
	ExpiresAt   *string   `json:"expires_at"`   // 过期时间 (ISO 8601)
	ResetQuota  *bool     `json:"reset_quota"`  // 重置已用配额

	// Rate limit fields (nil = no change, 0 = unlimited)
	RateLimit5h         *float64 `json:"rate_limit_5h"`
	RateLimit1d         *float64 `json:"rate_limit_1d"`
	RateLimit7d         *float64 `json:"rate_limit_7d"`
	ResetRateLimitUsage *bool    `json:"reset_rate_limit_usage"` // 重置限速用量
	Verification        string   `json:"verification" binding:"omitempty,max=256"`
}

// List handles listing user's API keys with pagination
// GET /api/v1/api-keys
func (h *APIKeyHandler) List(c *gin.Context) {
	subject, ok := middleware2.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}

	page, pageSize := response.ParsePagination(c)
	params := pagination.PaginationParams{
		Page:      page,
		PageSize:  pageSize,
		SortBy:    c.DefaultQuery("sort_by", "created_at"),
		SortOrder: c.DefaultQuery("sort_order", "desc"),
	}

	// Parse filter parameters
	var filters service.APIKeyListFilters
	if search := strings.TrimSpace(c.Query("search")); search != "" {
		if len(search) > 100 {
			search = search[:100]
		}
		filters.Search = search
	}
	filters.Status = c.Query("status")
	if groupIDStr := c.Query("group_id"); groupIDStr != "" {
		gid, err := strconv.ParseInt(groupIDStr, 10, 64)
		if err == nil {
			filters.GroupID = &gid
		}
	}

	keys, result, err := h.apiKeyService.List(c.Request.Context(), subject.UserID, params, filters)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	out := make([]dto.APIKey, 0, len(keys))
	for i := range keys {
		out = append(out, *dto.APIKeyFromServiceMasked(&keys[i]))
	}
	response.Paginated(c, out, result.Total, page, pageSize)
}

// GetByID handles getting a single API key
// GET /api/v1/api-keys/:id
func (h *APIKeyHandler) GetByID(c *gin.Context) {
	subject, ok := middleware2.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}

	keyID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid key ID")
		return
	}

	key, err := h.apiKeyService.GetByID(c.Request.Context(), keyID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	// 验证所有权
	if key.UserID != subject.UserID {
		response.Forbidden(c, "Not authorized to access this key")
		return
	}

	response.Success(c, dto.APIKeyFromServiceMasked(key))
}

// Create handles creating a new API key
// POST /api/v1/api-keys
func (h *APIKeyHandler) Create(c *gin.Context) {
	subject, ok := middleware2.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}
	c.Header("Cache-Control", "no-store, max-age=0")
	c.Header("Pragma", "no-cache")

	var req CreateAPIKeyRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	verificationMode, err := h.verifyFreshAPIKeyCredential(c.Request.Context(), subject.UserID, req.Verification, service.APIKeyStepUpPurposeCreate)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	svcReq := service.CreateAPIKeyRequest{
		Name:          req.Name,
		GroupID:       req.GroupID,
		CustomKey:     req.CustomKey,
		IPWhitelist:   req.IPWhitelist,
		IPBlacklist:   req.IPBlacklist,
		ExpiresInDays: req.ExpiresInDays,
	}
	if req.Quota != nil {
		svcReq.Quota = *req.Quota
	}
	if req.RateLimit5h != nil {
		svcReq.RateLimit5h = *req.RateLimit5h
	}
	if req.RateLimit1d != nil {
		svcReq.RateLimit1d = *req.RateLimit1d
	}
	if req.RateLimit7d != nil {
		svcReq.RateLimit7d = *req.RateLimit7d
	}

	idempotencyRequest := struct {
		Name          string   `json:"name"`
		GroupID       *int64   `json:"group_id"`
		CustomKey     *string  `json:"custom_key"`
		IPWhitelist   []string `json:"ip_whitelist"`
		IPBlacklist   []string `json:"ip_blacklist"`
		Quota         *float64 `json:"quota"`
		ExpiresInDays *int     `json:"expires_in_days"`
		RateLimit5h   *float64 `json:"rate_limit_5h"`
		RateLimit1d   *float64 `json:"rate_limit_1d"`
		RateLimit7d   *float64 `json:"rate_limit_7d"`
	}{req.Name, req.GroupID, req.CustomKey, req.IPWhitelist, req.IPBlacklist, req.Quota, req.ExpiresInDays, req.RateLimit5h, req.RateLimit1d, req.RateLimit7d}
	executeUserIdempotentJSON(c, "user.api_keys.create", idempotencyRequest, service.DefaultWriteIdempotencyTTL(), func(ctx context.Context) (any, error) {
		key, err := h.apiKeyService.Create(ctx, subject.UserID, svcReq)
		if err != nil {
			return nil, err
		}
		slog.Info("api_key.created", "user_id", subject.UserID, "api_key_id", key.ID, "verification_mode", verificationMode)
		return service.NewIdempotencySensitiveResponse(
			dto.APIKeyFromService(key),
			dto.APIKeyFromServiceMasked(key),
		), nil
	})
}

// Update handles updating an API key
// PUT /api/v1/api-keys/:id
func (h *APIKeyHandler) Update(c *gin.Context) {
	subject, ok := middleware2.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}

	keyID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid key ID")
		return
	}

	var req UpdateAPIKeyRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}

	svcReq := service.UpdateAPIKeyRequest{
		IPWhitelist:         req.IPWhitelist,
		IPBlacklist:         req.IPBlacklist,
		Quota:               req.Quota,
		ResetQuota:          req.ResetQuota,
		RateLimit5h:         req.RateLimit5h,
		RateLimit1d:         req.RateLimit1d,
		RateLimit7d:         req.RateLimit7d,
		ResetRateLimitUsage: req.ResetRateLimitUsage,
	}
	if req.Name != "" {
		svcReq.Name = &req.Name
	}
	svcReq.GroupID = req.GroupID
	if req.Status != "" {
		svcReq.Status = &req.Status
	}
	// Parse expires_at if provided
	if req.ExpiresAt != nil {
		if *req.ExpiresAt == "" {
			// Empty string means clear expiration
			svcReq.ExpiresAt = nil
			svcReq.ClearExpiration = true
		} else {
			t, err := time.Parse(time.RFC3339, *req.ExpiresAt)
			if err != nil {
				response.BadRequest(c, "Invalid expires_at format: "+err.Error())
				return
			}
			svcReq.ExpiresAt = &t
		}
	}
	if service.APIKeyUpdateRequiresStepUp(svcReq) {
		verificationMode, err := h.verifyFreshAPIKeyCredential(
			c.Request.Context(), subject.UserID, req.Verification, service.APIKeyStepUpPurposeUpdate,
		)
		if err != nil {
			response.ErrorFrom(c, err)
			return
		}
		svcReq.StepUpVerified = true
		slog.Info("api_key.update_step_up_verified", "user_id", subject.UserID, "api_key_id", keyID, "verification_mode", verificationMode)
	}

	key, err := h.apiKeyService.Update(c.Request.Context(), keyID, subject.UserID, svcReq)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	response.Success(c, dto.APIKeyFromServiceMasked(key))
}

type revealAPIKeyRequest struct {
	Verification string `json:"verification" binding:"required,max=256"`
}

func (h *APIKeyHandler) verifyFreshAPIKeyCredential(ctx context.Context, userID int64, verification, purpose string) (string, error) {
	mode, err := h.apiKeyService.RevealVerificationMode(ctx, userID)
	if err != nil {
		return "", err
	}
	if mode == "totp" {
		if h.totpService == nil {
			return "", service.ErrAPIKeyRevealUnavailable
		}
		totpPurpose := service.TotpVerifyPurposeAPIKeyReveal
		if purpose == service.APIKeyStepUpPurposeCreate {
			totpPurpose = service.TotpVerifyPurposeAPIKeyCreate
		} else if purpose == service.APIKeyStepUpPurposeUpdate {
			totpPurpose = service.TotpVerifyPurposeAPIKeyUpdate
		}
		if err := h.totpService.VerifyCodeForPurpose(ctx, userID, strings.TrimSpace(verification), totpPurpose); err != nil {
			if errors.Is(err, service.ErrTotpInvalidCode) {
				if purpose == service.APIKeyStepUpPurposeUpdate {
					return "", service.ErrAPIKeyUpdateVerification
				}
				return "", service.ErrAPIKeyRevealVerification
			}
			return "", err
		}
		return mode, nil
	}
	if err := h.apiKeyService.VerifyStepUpPassword(ctx, userID, verification, purpose); err != nil {
		if purpose == service.APIKeyStepUpPurposeUpdate && errors.Is(err, service.ErrAPIKeyRevealVerification) {
			return "", service.ErrAPIKeyUpdateVerification
		}
		return "", err
	}
	return mode, nil
}

// Reveal returns a single plaintext API key only after fresh password or TOTP
// proof. List/get/update/admin responses remain masked.
func (h *APIKeyHandler) Reveal(c *gin.Context) {
	subject, ok := middleware2.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}
	keyID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || keyID <= 0 {
		response.BadRequest(c, "Invalid key ID")
		return
	}
	var req revealAPIKeyRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Fresh password or TOTP verification is required")
		return
	}
	mode, err := h.verifyFreshAPIKeyCredential(c.Request.Context(), subject.UserID, req.Verification, service.APIKeyStepUpPurposeReveal)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	key, err := h.apiKeyService.Reveal(c.Request.Context(), keyID, subject.UserID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	c.Header("Cache-Control", "no-store, max-age=0")
	c.Header("Pragma", "no-cache")
	slog.Info("api_key.revealed", "user_id", subject.UserID, "api_key_id", keyID, "verification_mode", mode)
	response.Success(c, gin.H{"api_key": key})
}

// Delete handles deleting an API key
// DELETE /api/v1/api-keys/:id
func (h *APIKeyHandler) Delete(c *gin.Context) {
	subject, ok := middleware2.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}

	keyID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid key ID")
		return
	}

	err = h.apiKeyService.Delete(c.Request.Context(), keyID, subject.UserID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	response.Success(c, gin.H{"message": "API key deleted successfully"})
}

// GetAvailableGroups 获取用户可以绑定的分组列表
// GET /api/v1/groups/available
func (h *APIKeyHandler) GetAvailableGroups(c *gin.Context) {
	subject, ok := middleware2.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}

	groups, err := h.apiKeyService.GetAvailableGroups(c.Request.Context(), subject.UserID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	out := make([]dto.Group, 0, len(groups))
	for i := range groups {
		out = append(out, *dto.GroupFromService(&groups[i]))
	}
	response.Success(c, out)
}

// GetUserGroupRates 获取当前用户的专属分组倍率配置
// GET /api/v1/groups/rates
func (h *APIKeyHandler) GetUserGroupRates(c *gin.Context) {
	subject, ok := middleware2.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}

	rates, err := h.apiKeyService.GetUserGroupRates(c.Request.Context(), subject.UserID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	response.Success(c, rates)
}

// GetWalletModelRoutes returns the model-name routes available to wallet universal keys.
// GET /api/v1/groups/model-routes
func (h *APIKeyHandler) GetWalletModelRoutes(c *gin.Context) {
	subject, ok := middleware2.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}

	routes := service.DefaultModelRoutes()
	if h.modelRouteProvider != nil {
		routes = h.modelRouteProvider.Routes()
	}

	out, err := h.apiKeyService.GetWalletModelRoutes(c.Request.Context(), subject.UserID, routes)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, out)
}
