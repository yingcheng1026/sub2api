package admin

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/handler/dto"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

const (
	// monitorMaxPageSize 列表分页上限。
	monitorMaxPageSize = 100
	// monitorAPIKeyMaskPrefix 脱敏时保留的明文前缀长度。
	monitorAPIKeyMaskPrefix = 4
	// monitorAPIKeyMaskSuffix 脱敏后追加的占位字符串。
	monitorAPIKeyMaskSuffix = "***"
)

// ChannelMonitorHandler 渠道监控管理后台 handler。
type ChannelMonitorHandler struct {
	monitorService  *service.ChannelMonitorService
	apiKeyService   *service.APIKeyService
	templateService channelMonitorTemplateResolver
}

type channelMonitorTemplateResolver interface {
	Get(ctx context.Context, id int64) (*service.ChannelMonitorRequestTemplate, error)
}

// NewChannelMonitorHandler 创建 handler。
func NewChannelMonitorHandler(monitorService *service.ChannelMonitorService) *ChannelMonitorHandler {
	return &ChannelMonitorHandler{monitorService: monitorService}
}

func ProvideChannelMonitorHandler(
	monitorService *service.ChannelMonitorService,
	apiKeyService *service.APIKeyService,
	templateService *service.ChannelMonitorRequestTemplateService,
) *ChannelMonitorHandler {
	return &ChannelMonitorHandler{
		monitorService:  monitorService,
		apiKeyService:   apiKeyService,
		templateService: templateService,
	}
}

// --- Request / Response ---

type channelMonitorCreateRequest struct {
	Name             string            `json:"name" binding:"required,max=100"`
	Provider         string            `json:"provider" binding:"required,oneof=openai anthropic gemini"`
	Endpoint         string            `json:"endpoint" binding:"required,max=500"`
	APIKey           string            `json:"api_key" binding:"max=2000"`
	APIKeyID         *int64            `json:"api_key_id"`
	PrimaryModel     string            `json:"primary_model" binding:"required,max=200"`
	ExtraModels      []string          `json:"extra_models"`
	GroupName        string            `json:"group_name" binding:"max=100"`
	Enabled          *bool             `json:"enabled"`
	IntervalSeconds  int               `json:"interval_seconds" binding:"required,min=15,max=3600"`
	TemplateID       *int64            `json:"template_id"`
	ExtraHeaders     map[string]string `json:"extra_headers"`
	BodyOverrideMode string            `json:"body_override_mode" binding:"omitempty,oneof=off merge replace"`
	BodyOverride     map[string]any    `json:"body_override"`
}

type channelMonitorUpdateRequest struct {
	Name                        *string            `json:"name" binding:"omitempty,max=100"`
	Provider                    *string            `json:"provider" binding:"omitempty,oneof=openai anthropic gemini"`
	Endpoint                    *string            `json:"endpoint" binding:"omitempty,max=500"`
	APIKey                      *string            `json:"api_key" binding:"omitempty,max=2000"`
	APIKeyID                    *int64             `json:"api_key_id"`
	PrimaryModel                *string            `json:"primary_model" binding:"omitempty,max=200"`
	ExtraModels                 *[]string          `json:"extra_models"`
	GroupName                   *string            `json:"group_name" binding:"omitempty,max=100"`
	Enabled                     *bool              `json:"enabled"`
	IntervalSeconds             *int               `json:"interval_seconds" binding:"omitempty,min=15,max=3600"`
	TemplateID                  *int64             `json:"template_id"`
	ClearTemplate               bool               `json:"clear_template"` // true 时把 template_id 置空，忽略 TemplateID
	ExtraHeaders                *map[string]string `json:"extra_headers"`
	BodyOverrideMode            *string            `json:"body_override_mode" binding:"omitempty,oneof=off merge replace"`
	BodyOverride                *map[string]any    `json:"body_override"`
	ReplaceRequestCustomization bool               `json:"replace_request_customization"`
}

type channelMonitorResponse struct {
	ID                  int64                                `json:"id"`
	Name                string                               `json:"name"`
	Provider            string                               `json:"provider"`
	Endpoint            string                               `json:"endpoint"`
	APIKeyMasked        string                               `json:"api_key_masked"`
	APIKeyDecryptFailed bool                                 `json:"api_key_decrypt_failed"`
	PrimaryModel        string                               `json:"primary_model"`
	ExtraModels         []string                             `json:"extra_models"`
	GroupName           string                               `json:"group_name"`
	Enabled             bool                                 `json:"enabled"`
	IntervalSeconds     int                                  `json:"interval_seconds"`
	LastCheckedAt       *string                              `json:"last_checked_at"`
	CreatedBy           int64                                `json:"created_by"`
	CreatedAt           string                               `json:"created_at"`
	UpdatedAt           string                               `json:"updated_at"`
	PrimaryStatus       string                               `json:"primary_status"`
	PrimaryLatencyMs    *int                                 `json:"primary_latency_ms"`
	Availability7d      float64                              `json:"availability_7d"`
	ExtraModelsStatus   []dto.ChannelMonitorExtraModelStatus `json:"extra_models_status"`
	// 请求自定义字段是 write-only：响应只返回存在性元数据，绝不返回明文。
	TemplateID             *int64 `json:"template_id"`
	ExtraHeadersConfigured bool   `json:"extra_headers_configured"`
	ExtraHeaderCount       int    `json:"extra_header_count"`
	BodyOverrideMode       string `json:"body_override_mode"`
	BodyOverrideConfigured bool   `json:"body_override_configured"`
}

type channelMonitorCheckResultResponse struct {
	Model         string `json:"model"`
	Status        string `json:"status"`
	LatencyMs     *int   `json:"latency_ms"`
	PingLatencyMs *int   `json:"ping_latency_ms"`
	Message       string `json:"message"`
	CheckedAt     string `json:"checked_at"`
}

type channelMonitorHistoryItemResponse struct {
	ID            int64  `json:"id"`
	Model         string `json:"model"`
	Status        string `json:"status"`
	LatencyMs     *int   `json:"latency_ms"`
	PingLatencyMs *int   `json:"ping_latency_ms"`
	Message       string `json:"message"`
	CheckedAt     string `json:"checked_at"`
}

// maskAPIKey 对 API Key 明文做脱敏：前 4 字符 + "***"，长度 ≤ 4 时只显示 "***"。
func maskAPIKey(plain string) string {
	if len(plain) <= monitorAPIKeyMaskPrefix {
		return monitorAPIKeyMaskSuffix
	}
	return plain[:monitorAPIKeyMaskPrefix] + monitorAPIKeyMaskSuffix
}

func channelMonitorToResponse(m *service.ChannelMonitor) *channelMonitorResponse {
	if m == nil {
		return nil
	}
	extras := m.ExtraModels
	if extras == nil {
		extras = []string{}
	}
	resp := &channelMonitorResponse{
		ID:                     m.ID,
		Name:                   m.Name,
		Provider:               m.Provider,
		Endpoint:               m.Endpoint,
		APIKeyMasked:           maskAPIKey(m.APIKey),
		APIKeyDecryptFailed:    m.APIKeyDecryptFailed,
		PrimaryModel:           m.PrimaryModel,
		ExtraModels:            extras,
		GroupName:              m.GroupName,
		Enabled:                m.Enabled,
		IntervalSeconds:        m.IntervalSeconds,
		CreatedBy:              m.CreatedBy,
		CreatedAt:              m.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt:              m.UpdatedAt.UTC().Format(time.RFC3339),
		TemplateID:             m.TemplateID,
		ExtraHeadersConfigured: len(m.ExtraHeaders) > 0,
		ExtraHeaderCount:       len(m.ExtraHeaders),
		BodyOverrideMode:       m.BodyOverrideMode,
		BodyOverrideConfigured: len(m.BodyOverride) > 0,
		// PrimaryStatus / PrimaryLatencyMs / Availability7d 由 List handler 在批量聚合后填充。
	}
	if m.LastCheckedAt != nil {
		s := m.LastCheckedAt.UTC().Format(time.RFC3339)
		resp.LastCheckedAt = &s
	}
	return resp
}

func checkResultToResponse(r *service.CheckResult) channelMonitorCheckResultResponse {
	return channelMonitorCheckResultResponse{
		Model:         r.Model,
		Status:        r.Status,
		LatencyMs:     r.LatencyMs,
		PingLatencyMs: r.PingLatencyMs,
		Message:       r.Message,
		CheckedAt:     r.CheckedAt.UTC().Format(time.RFC3339),
	}
}

func historyEntryToResponse(e *service.ChannelMonitorHistoryEntry) channelMonitorHistoryItemResponse {
	return channelMonitorHistoryItemResponse{
		ID:            e.ID,
		Model:         e.Model,
		Status:        e.Status,
		LatencyMs:     e.LatencyMs,
		PingLatencyMs: e.PingLatencyMs,
		Message:       e.Message,
		CheckedAt:     e.CheckedAt.UTC().Format(time.RFC3339),
	}
}

// ParseChannelMonitorID 提取并校验路径参数 :id（admin 与 user handler 共享）。
// 校验失败时已写入 4xx 响应，调用方只需 return。
func ParseChannelMonitorID(c *gin.Context) (int64, bool) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		response.ErrorFrom(c, infraerrors.BadRequest("INVALID_MONITOR_ID", "invalid monitor id"))
		return 0, false
	}
	return id, true
}

// parseListEnabled 解析 enabled query 参数：true/false 转为 *bool，空或非法则返回 nil。
func parseListEnabled(raw string) *bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "true", "1", "yes":
		v := true
		return &v
	case "false", "0", "no":
		v := false
		return &v
	default:
		return nil
	}
}

// --- Handlers ---

// List GET /api/v1/admin/channel-monitors
func (h *ChannelMonitorHandler) List(c *gin.Context) {
	page, pageSize := response.ParsePagination(c)
	if pageSize > monitorMaxPageSize {
		pageSize = monitorMaxPageSize
	}

	params := service.ChannelMonitorListParams{
		Page:     page,
		PageSize: pageSize,
		Provider: strings.TrimSpace(c.Query("provider")),
		Enabled:  parseListEnabled(c.Query("enabled")),
		Search:   strings.TrimSpace(c.Query("search")),
	}

	items, total, err := h.monitorService.List(c.Request.Context(), params)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	summaries := h.batchSummaryFor(c, items)
	out := make([]*channelMonitorResponse, 0, len(items))
	for _, m := range items {
		out = append(out, buildListItemResponse(m, summaries[m.ID]))
	}
	response.Paginated(c, out, total, page, pageSize)
}

// batchSummaryFor 批量聚合 latest + 7d 可用率，避免每行 2 次 SQL（消除 N+1）。
func (h *ChannelMonitorHandler) batchSummaryFor(c *gin.Context, items []*service.ChannelMonitor) map[int64]service.MonitorStatusSummary {
	ids := make([]int64, 0, len(items))
	primaryByID := make(map[int64]string, len(items))
	extrasByID := make(map[int64][]string, len(items))
	for _, m := range items {
		ids = append(ids, m.ID)
		primaryByID[m.ID] = m.PrimaryModel
		extrasByID[m.ID] = m.ExtraModels
	}
	return h.monitorService.BatchMonitorStatusSummary(c.Request.Context(), ids, primaryByID, extrasByID)
}

// buildListItemResponse 把 monitor + summary 装成 admin list 的响应行。
func buildListItemResponse(m *service.ChannelMonitor, summary service.MonitorStatusSummary) *channelMonitorResponse {
	resp := channelMonitorToResponse(m)
	resp.PrimaryStatus = summary.PrimaryStatus
	resp.PrimaryLatencyMs = summary.PrimaryLatencyMs
	resp.Availability7d = summary.Availability7d
	resp.ExtraModelsStatus = make([]dto.ChannelMonitorExtraModelStatus, 0, len(summary.ExtraModels))
	for _, e := range summary.ExtraModels {
		resp.ExtraModelsStatus = append(resp.ExtraModelsStatus, dto.ChannelMonitorExtraModelStatus{
			Model:     e.Model,
			Status:    e.Status,
			LatencyMs: e.LatencyMs,
		})
	}
	return resp
}

// Get GET /api/v1/admin/channel-monitors/:id
func (h *ChannelMonitorHandler) Get(c *gin.Context) {
	id, ok := ParseChannelMonitorID(c)
	if !ok {
		return
	}
	m, err := h.monitorService.Get(c.Request.Context(), id)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, channelMonitorToResponse(m))
}

// Create POST /api/v1/admin/channel-monitors
func (h *ChannelMonitorHandler) Create(c *gin.Context) {
	var req channelMonitorCreateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.ErrorFrom(c, infraerrors.BadRequest("VALIDATION_ERROR", err.Error()))
		return
	}

	subject, _ := middleware2.GetAuthSubjectFromContext(c)
	apiKey, err := h.resolveOwnedAPIKey(c.Request.Context(), subject.UserID, req.APIKeyID, req.APIKey)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	extraHeaders, bodyMode, bodyOverride, err := h.resolveCreateRequestCustomization(c.Request.Context(), &req)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	m, err := h.monitorService.Create(c.Request.Context(), service.ChannelMonitorCreateParams{
		Name:             req.Name,
		Provider:         req.Provider,
		Endpoint:         req.Endpoint,
		APIKey:           apiKey,
		PrimaryModel:     req.PrimaryModel,
		ExtraModels:      req.ExtraModels,
		GroupName:        req.GroupName,
		Enabled:          enabled,
		IntervalSeconds:  req.IntervalSeconds,
		CreatedBy:        subject.UserID,
		TemplateID:       req.TemplateID,
		ExtraHeaders:     extraHeaders,
		BodyOverrideMode: bodyMode,
		BodyOverride:     bodyOverride,
	})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Created(c, channelMonitorToResponse(m))
}

// Update PUT /api/v1/admin/channel-monitors/:id
func (h *ChannelMonitorHandler) Update(c *gin.Context) {
	id, ok := ParseChannelMonitorID(c)
	if !ok {
		return
	}
	var req channelMonitorUpdateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.ErrorFrom(c, infraerrors.BadRequest("VALIDATION_ERROR", err.Error()))
		return
	}
	if err := h.prepareMonitorCustomizationUpdate(c.Request.Context(), &req); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	subject, _ := middleware2.GetAuthSubjectFromContext(c)
	apiKey := req.APIKey
	if req.APIKeyID != nil {
		resolved, err := h.resolveOwnedAPIKey(c.Request.Context(), subject.UserID, req.APIKeyID, "")
		if err != nil {
			response.ErrorFrom(c, err)
			return
		}
		apiKey = &resolved
	}

	m, err := h.monitorService.Update(c.Request.Context(), id, service.ChannelMonitorUpdateParams{
		Name:             req.Name,
		Provider:         req.Provider,
		Endpoint:         req.Endpoint,
		APIKey:           apiKey,
		PrimaryModel:     req.PrimaryModel,
		ExtraModels:      req.ExtraModels,
		GroupName:        req.GroupName,
		Enabled:          req.Enabled,
		IntervalSeconds:  req.IntervalSeconds,
		TemplateID:       req.TemplateID,
		ClearTemplate:    req.ClearTemplate,
		ExtraHeaders:     req.ExtraHeaders,
		BodyOverrideMode: req.BodyOverrideMode,
		BodyOverride:     req.BodyOverride,
	})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, channelMonitorToResponse(m))
}

func (h *ChannelMonitorHandler) resolveOwnedAPIKey(ctx context.Context, userID int64, apiKeyID *int64, raw string) (string, error) {
	if apiKeyID == nil {
		return strings.TrimSpace(raw), nil
	}
	if *apiKeyID <= 0 || h.apiKeyService == nil {
		return "", infraerrors.BadRequest("INVALID_API_KEY_ID", "invalid API key id")
	}
	key, err := h.apiKeyService.GetByID(ctx, *apiKeyID)
	if err != nil {
		return "", err
	}
	if key.UserID != userID {
		return "", service.ErrInsufficientPerms
	}
	if !key.IsActive() || key.IsExpired() || key.IsQuotaExhausted() {
		return "", infraerrors.BadRequest("API_KEY_NOT_USABLE", "selected API key is not active and usable")
	}
	return key.Key, nil
}

func (h *ChannelMonitorHandler) resolveCreateRequestCustomization(
	ctx context.Context,
	req *channelMonitorCreateRequest,
) (map[string]string, string, map[string]any, error) {
	if req.TemplateID == nil {
		return req.ExtraHeaders, req.BodyOverrideMode, req.BodyOverride, nil
	}
	return h.resolveTemplateSnapshot(ctx, *req.TemplateID, req.Provider)
}

func (h *ChannelMonitorHandler) prepareMonitorCustomizationUpdate(
	ctx context.Context,
	req *channelMonitorUpdateRequest,
) error {
	hasPayload := req.ExtraHeaders != nil || req.BodyOverrideMode != nil || req.BodyOverride != nil
	if !req.ReplaceRequestCustomization {
		if hasPayload || req.TemplateID != nil {
			return infraerrors.BadRequest(
				"REQUEST_CUSTOMIZATION_REPLACEMENT_REQUIRED",
				"set replace_request_customization=true to replace write-only request customization",
			)
		}
		return nil
	}
	if req.ClearTemplate && req.TemplateID != nil {
		return infraerrors.BadRequest("INVALID_TEMPLATE_UPDATE", "template_id and clear_template cannot be used together")
	}
	if req.TemplateID != nil {
		if req.Provider == nil || strings.TrimSpace(*req.Provider) == "" {
			return infraerrors.BadRequest("MONITOR_PROVIDER_REQUIRED", "provider is required when applying a request template")
		}
		headers, mode, body, err := h.resolveTemplateSnapshot(ctx, *req.TemplateID, *req.Provider)
		if err != nil {
			return err
		}
		req.ExtraHeaders = &headers
		req.BodyOverrideMode = &mode
		req.BodyOverride = &body
		return nil
	}
	if req.ExtraHeaders == nil {
		headers := map[string]string{}
		req.ExtraHeaders = &headers
	}
	if req.BodyOverrideMode == nil {
		mode := service.MonitorBodyOverrideModeOff
		req.BodyOverrideMode = &mode
	}
	if req.BodyOverride == nil {
		var body map[string]any
		req.BodyOverride = &body
	}
	// A direct payload replacement is no longer a snapshot of the previously
	// associated template. Detach it server-side so stale/non-browser clients
	// cannot leave a misleading association that a later template apply would
	// silently overwrite.
	req.ClearTemplate = true
	return nil
}

func (h *ChannelMonitorHandler) resolveTemplateSnapshot(
	ctx context.Context,
	templateID int64,
	provider string,
) (map[string]string, string, map[string]any, error) {
	if templateID <= 0 || h.templateService == nil {
		return nil, "", nil, infraerrors.BadRequest("INVALID_TEMPLATE_ID", "invalid request template id")
	}
	tpl, err := h.templateService.Get(ctx, templateID)
	if err != nil {
		return nil, "", nil, err
	}
	if tpl.Provider != strings.TrimSpace(provider) {
		return nil, "", nil, service.ErrChannelMonitorTemplateProviderMismatch
	}
	return tpl.ExtraHeaders, tpl.BodyOverrideMode, tpl.BodyOverride, nil
}

// Delete DELETE /api/v1/admin/channel-monitors/:id
func (h *ChannelMonitorHandler) Delete(c *gin.Context) {
	id, ok := ParseChannelMonitorID(c)
	if !ok {
		return
	}
	if err := h.monitorService.Delete(c.Request.Context(), id); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, nil)
}

// Run POST /api/v1/admin/channel-monitors/:id/run
func (h *ChannelMonitorHandler) Run(c *gin.Context) {
	id, ok := ParseChannelMonitorID(c)
	if !ok {
		return
	}
	results, err := h.monitorService.RunCheck(c.Request.Context(), id)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	out := make([]channelMonitorCheckResultResponse, 0, len(results))
	for _, r := range results {
		out = append(out, checkResultToResponse(r))
	}
	response.Success(c, gin.H{"results": out})
}

// History GET /api/v1/admin/channel-monitors/:id/history
func (h *ChannelMonitorHandler) History(c *gin.Context) {
	id, ok := ParseChannelMonitorID(c)
	if !ok {
		return
	}
	limit := parseHistoryLimit(c.Query("limit"))
	model := strings.TrimSpace(c.Query("model"))

	entries, err := h.monitorService.ListHistory(c.Request.Context(), id, model, limit)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	out := make([]channelMonitorHistoryItemResponse, 0, len(entries))
	for _, e := range entries {
		out = append(out, historyEntryToResponse(e))
	}
	response.Success(c, gin.H{"items": out})
}

// parseHistoryLimit 解析 history 接口的 limit query。
// 使用 service 包的统一上下限常量，避免在 handler 重复定义同名魔法值。
func parseHistoryLimit(raw string) int {
	if strings.TrimSpace(raw) == "" {
		return service.MonitorHistoryDefaultLimit
	}
	v, err := strconv.Atoi(raw)
	if err != nil || v <= 0 {
		return service.MonitorHistoryDefaultLimit
	}
	if v > service.MonitorHistoryMaxLimit {
		return service.MonitorHistoryMaxLimit
	}
	return v
}
