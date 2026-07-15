package handler

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/handler/dto"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

const (
	maxUserUsagePage      = 1000
	maxUserUsagePageSize  = 100
	maxUserUsageOffset    = 10_000
	maxUserUsageRangeDays = 366
	maxUserUsageQueryTime = 10 * time.Second
)

// UsageHandler handles usage-related requests
type UsageHandler struct {
	usageService  *service.UsageService
	apiKeyService *service.APIKeyService
}

// NewUsageHandler creates a new UsageHandler
func NewUsageHandler(usageService *service.UsageService, apiKeyService *service.APIKeyService) *UsageHandler {
	return &UsageHandler{
		usageService:  usageService,
		apiKeyService: apiKeyService,
	}
}

// List handles listing usage records with pagination
// GET /api/v1/usage
func (h *UsageHandler) List(c *gin.Context) {
	subject, ok := middleware2.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}
	requestCtx, cancel := boundedUsageContext(c.Request.Context())
	defer cancel()

	page, pageSize, err := parseUsagePagination(c)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}

	var apiKeyID int64
	if apiKeyIDStr := c.Query("api_key_id"); apiKeyIDStr != "" {
		id, err := strconv.ParseInt(apiKeyIDStr, 10, 64)
		if err != nil {
			response.BadRequest(c, "Invalid api_key_id")
			return
		}

		// [Security Fix] Verify API Key ownership to prevent horizontal privilege escalation
		apiKey, err := h.apiKeyService.GetByID(requestCtx, id)
		if err != nil {
			response.ErrorFrom(c, err)
			return
		}
		if apiKey.UserID != subject.UserID {
			response.Forbidden(c, "Not authorized to access this API key's usage records")
			return
		}

		apiKeyID = id
	}

	// Parse additional filters
	model := c.Query("model")

	var requestType *int16
	var stream *bool
	if requestTypeStr := strings.TrimSpace(c.Query("request_type")); requestTypeStr != "" {
		parsed, err := service.ParseUsageRequestType(requestTypeStr)
		if err != nil {
			response.BadRequest(c, err.Error())
			return
		}
		value := int16(parsed)
		requestType = &value
	} else if streamStr := c.Query("stream"); streamStr != "" {
		val, err := strconv.ParseBool(streamStr)
		if err != nil {
			response.BadRequest(c, "Invalid stream value, use true or false")
			return
		}
		stream = &val
	}

	var billingType *int8
	if billingTypeStr := c.Query("billing_type"); billingTypeStr != "" {
		val, err := strconv.ParseInt(billingTypeStr, 10, 8)
		if err != nil {
			response.BadRequest(c, "Invalid billing_type")
			return
		}
		bt := int8(val)
		billingType = &bt
	}

	startTime, endTime, err := parseBoundedUsageTimeRange(c, 365, false)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}

	params := pagination.PaginationParams{
		Page:      page,
		PageSize:  pageSize,
		SortBy:    c.DefaultQuery("sort_by", "created_at"),
		SortOrder: c.DefaultQuery("sort_order", "desc"),
	}
	filters := usagestats.UsageLogFilters{
		UserID:      subject.UserID, // Always filter by current user for security
		APIKeyID:    apiKeyID,
		Model:       model,
		RequestType: requestType,
		Stream:      stream,
		BillingType: billingType,
		StartTime:   &startTime,
		EndTime:     &endTime,
	}

	records, result, err := h.usageService.ListWithFilters(requestCtx, params, filters)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	out := make([]dto.UsageLog, 0, len(records))
	for i := range records {
		out = append(out, *dto.UsageLogFromService(&records[i]))
	}
	response.Paginated(c, out, result.Total, page, pageSize)
}

// GetByID handles getting a single usage record
// GET /api/v1/usage/:id
func (h *UsageHandler) GetByID(c *gin.Context) {
	subject, ok := middleware2.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}
	requestCtx, cancel := boundedUsageContext(c.Request.Context())
	defer cancel()

	usageID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid usage ID")
		return
	}

	record, err := h.usageService.GetByIDForUser(requestCtx, usageID, subject.UserID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	response.Success(c, dto.UsageLogFromService(record))
}

// Stats handles getting usage statistics
// GET /api/v1/usage/stats
func (h *UsageHandler) Stats(c *gin.Context) {
	subject, ok := middleware2.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}
	requestCtx, cancel := boundedUsageContext(c.Request.Context())
	defer cancel()

	var apiKeyID int64
	if apiKeyIDStr := c.Query("api_key_id"); apiKeyIDStr != "" {
		id, err := strconv.ParseInt(apiKeyIDStr, 10, 64)
		if err != nil {
			response.BadRequest(c, "Invalid api_key_id")
			return
		}

		// [Security Fix] Verify API Key ownership to prevent horizontal privilege escalation
		apiKey, err := h.apiKeyService.GetByID(requestCtx, id)
		if err != nil {
			response.NotFound(c, "API key not found")
			return
		}
		if apiKey.UserID != subject.UserID {
			response.Forbidden(c, "Not authorized to access this API key's statistics")
			return
		}

		apiKeyID = id
	}

	// 获取时间范围参数
	userTZ := c.Query("timezone") // Get user's timezone from request
	now := timezone.NowInUserLocation(userTZ)
	var startTime, endTime time.Time

	// 优先使用 start_date 和 end_date 参数
	startDateStr := c.Query("start_date")
	endDateStr := c.Query("end_date")

	if startDateStr != "" || endDateStr != "" {
		var err error
		startTime, endTime, err = parseBoundedUsageTimeRange(c, 365, true)
		if err != nil {
			response.BadRequest(c, err.Error())
			return
		}
	} else {
		// 使用 period 参数
		period := c.DefaultQuery("period", "today")
		switch period {
		case "today":
			startTime = timezone.StartOfDayInUserLocation(now, userTZ)
		case "week":
			startTime = now.AddDate(0, 0, -7)
		case "month":
			startTime = now.AddDate(0, -1, 0)
		default:
			startTime = timezone.StartOfDayInUserLocation(now, userTZ)
		}
		endTime = now
	}

	var stats *service.UsageStats
	var err error
	if apiKeyID > 0 {
		stats, err = h.usageService.GetStatsByAPIKey(requestCtx, apiKeyID, startTime, endTime)
	} else {
		stats, err = h.usageService.GetStatsByUser(requestCtx, subject.UserID, startTime, endTime)
	}
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	response.Success(c, stats)
}

func parseUsagePagination(c *gin.Context) (int, int, error) {
	parse := func(name string, defaultValue int) (int, error) {
		raw := strings.TrimSpace(c.Query(name))
		if raw == "" {
			return defaultValue, nil
		}
		value, err := strconv.ParseInt(raw, 10, 32)
		if err != nil || value <= 0 {
			return 0, fmt.Errorf("invalid %s", name)
		}
		return int(value), nil
	}

	page, err := parse("page", 1)
	if err != nil {
		return 0, 0, err
	}
	if page > maxUserUsagePage {
		return 0, 0, fmt.Errorf("page exceeds maximum %d", maxUserUsagePage)
	}

	sizeName := "page_size"
	if strings.TrimSpace(c.Query(sizeName)) == "" && strings.TrimSpace(c.Query("limit")) != "" {
		sizeName = "limit"
	}
	pageSize, err := parse(sizeName, 20)
	if err != nil {
		return 0, 0, err
	}
	if pageSize > maxUserUsagePageSize {
		return 0, 0, fmt.Errorf("%s exceeds maximum %d", sizeName, maxUserUsagePageSize)
	}
	if page > 1 && page-1 > maxUserUsageOffset/pageSize {
		return 0, 0, fmt.Errorf("page offset exceeds maximum %d", maxUserUsageOffset)
	}
	return page, pageSize, nil
}

func boundedUsageContext(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(parent, maxUserUsageQueryTime)
}

// parseBoundedUsageTimeRange returns a half-open, calendar-day-aligned range.
func parseBoundedUsageTimeRange(c *gin.Context, defaultLookbackDays int, requirePair bool) (time.Time, time.Time, error) {
	userTZ := c.Query("timezone")
	startRaw := strings.TrimSpace(c.Query("start_date"))
	endRaw := strings.TrimSpace(c.Query("end_date"))
	if requirePair && (startRaw == "") != (endRaw == "") {
		return time.Time{}, time.Time{}, fmt.Errorf("start_date and end_date must be provided together")
	}

	now := timezone.NowInUserLocation(userTZ)
	endTime := timezone.StartOfDayInUserLocation(now.AddDate(0, 0, 1), userTZ)
	startTime := endTime.AddDate(0, 0, -defaultLookbackDays)
	var err error
	if startRaw != "" {
		startTime, err = timezone.ParseInUserLocation("2006-01-02", startRaw, userTZ)
		if err != nil {
			return time.Time{}, time.Time{}, fmt.Errorf("invalid start_date format, use YYYY-MM-DD")
		}
	}
	if endRaw != "" {
		endTime, err = timezone.ParseInUserLocation("2006-01-02", endRaw, userTZ)
		if err != nil {
			return time.Time{}, time.Time{}, fmt.Errorf("invalid end_date format, use YYYY-MM-DD")
		}
		endTime = endTime.AddDate(0, 0, 1)
	}
	if startRaw == "" {
		startTime = endTime.AddDate(0, 0, -defaultLookbackDays)
	}
	if !startTime.Before(endTime) {
		return time.Time{}, time.Time{}, fmt.Errorf("start_date must be before or equal to end_date")
	}
	if endTime.After(startTime.AddDate(0, 0, maxUserUsageRangeDays)) {
		return time.Time{}, time.Time{}, fmt.Errorf("usage date range exceeds maximum %d days", maxUserUsageRangeDays)
	}
	return startTime, endTime, nil
}

// DashboardStats handles getting user dashboard statistics
// GET /api/v1/usage/dashboard/stats
func (h *UsageHandler) DashboardStats(c *gin.Context) {
	subject, ok := middleware2.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}
	requestCtx, cancel := boundedUsageContext(c.Request.Context())
	defer cancel()

	stats, err := h.usageService.GetUserDashboardStats(requestCtx, subject.UserID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	response.Success(c, stats)
}

// DashboardTrend handles getting user usage trend data
// GET /api/v1/usage/dashboard/trend
func (h *UsageHandler) DashboardTrend(c *gin.Context) {
	subject, ok := middleware2.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}
	requestCtx, cancel := boundedUsageContext(c.Request.Context())
	defer cancel()

	startTime, endTime, err := parseBoundedUsageTimeRange(c, 8, false)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	granularity := c.DefaultQuery("granularity", "day")

	trend, err := h.usageService.GetUserUsageTrendByUserID(requestCtx, subject.UserID, startTime, endTime, granularity)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	response.Success(c, gin.H{
		"trend":       trend,
		"start_date":  startTime.Format("2006-01-02"),
		"end_date":    endTime.AddDate(0, 0, -1).Format("2006-01-02"),
		"granularity": granularity,
	})
}

// DashboardModels handles getting user model usage statistics
// GET /api/v1/usage/dashboard/models
func (h *UsageHandler) DashboardModels(c *gin.Context) {
	subject, ok := middleware2.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}
	requestCtx, cancel := boundedUsageContext(c.Request.Context())
	defer cancel()

	startTime, endTime, err := parseBoundedUsageTimeRange(c, 8, false)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}

	stats, err := h.usageService.GetUserModelStats(requestCtx, subject.UserID, startTime, endTime)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	response.Success(c, gin.H{
		"models":     stats,
		"start_date": startTime.Format("2006-01-02"),
		"end_date":   endTime.AddDate(0, 0, -1).Format("2006-01-02"),
	})
}

// BatchAPIKeysUsageRequest represents the request for batch API keys usage
type BatchAPIKeysUsageRequest struct {
	APIKeyIDs []int64 `json:"api_key_ids" binding:"required"`
}

// DashboardAPIKeysUsage handles getting usage stats for user's own API keys
// POST /api/v1/usage/dashboard/api-keys-usage
func (h *UsageHandler) DashboardAPIKeysUsage(c *gin.Context) {
	subject, ok := middleware2.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}
	requestCtx, cancel := boundedUsageContext(c.Request.Context())
	defer cancel()

	var req BatchAPIKeysUsageRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}

	if len(req.APIKeyIDs) == 0 {
		response.Success(c, gin.H{"stats": map[string]any{}})
		return
	}

	// Limit the number of API key IDs to prevent SQL parameter overflow
	if len(req.APIKeyIDs) > 100 {
		response.BadRequest(c, "Too many API key IDs (maximum 100 allowed)")
		return
	}

	validAPIKeyIDs, err := h.apiKeyService.VerifyOwnership(requestCtx, subject.UserID, req.APIKeyIDs)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	if len(validAPIKeyIDs) == 0 {
		response.Success(c, gin.H{"stats": map[string]any{}})
		return
	}

	startTime, endTime, err := parseBoundedUsageTimeRange(c, 365, false)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	stats, err := h.usageService.GetBatchAPIKeyUsageStats(requestCtx, validAPIKeyIDs, startTime, endTime)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	response.Success(c, gin.H{"stats": stats})
}
