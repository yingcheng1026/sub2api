package admin

import (
	"context"
	"log/slog"
	"strconv"

	"github.com/Wei-Shaw/sub2api/internal/handler/dto"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

// toResponsePagination converts pagination.PaginationResult to response.PaginationResult
func toResponsePagination(p *pagination.PaginationResult) *response.PaginationResult {
	if p == nil {
		return nil
	}
	return &response.PaginationResult{
		Total:    p.Total,
		Page:     p.Page,
		PageSize: p.PageSize,
		Pages:    p.Pages,
	}
}

// SubscriptionHandler handles admin subscription management
type SubscriptionHandler struct {
	subscriptionService *service.SubscriptionService
	affiliateService    *service.AffiliateService

	// Narrow assignment dependencies keep the high-risk write path testable
	// without weakening the concrete service used by the read/update handlers.
	subscriptionAssigner        subscriptionAssigner
	subscriptionBulkAssigner    subscriptionBulkAssigner
	assignmentTransactionRunner assignmentTransactionRunner
	affiliateRebateAccruer      affiliateRebateAccruer
}

type subscriptionAssigner interface {
	AssignAdminSubscription(context.Context, *service.AssignSubscriptionInput) (*service.UserSubscription, error)
}

type subscriptionBulkAssigner interface {
	BulkAssignSubscription(context.Context, *service.BulkAssignSubscriptionInput) (*service.BulkAssignResult, error)
}

type assignmentTransactionRunner interface {
	RunAssignmentTransaction(context.Context, func(context.Context) error) error
}

type affiliateRebateAccruer interface {
	AccrueInviteRebateForOrderWithOverride(context.Context, int64, float64, *float64, *int64) (float64, error)
}

// NewSubscriptionHandler creates a new admin subscription handler
func NewSubscriptionHandler(subscriptionService *service.SubscriptionService, affiliateService *service.AffiliateService) *SubscriptionHandler {
	return &SubscriptionHandler{
		subscriptionService:         subscriptionService,
		affiliateService:            affiliateService,
		subscriptionAssigner:        subscriptionService,
		subscriptionBulkAssigner:    subscriptionService,
		assignmentTransactionRunner: subscriptionService,
		affiliateRebateAccruer:      affiliateService,
	}
}

// AssignSubscriptionRequest represents assign subscription request.
//
// 三种模式三选一：
//   - Plan 模式：只填 plan_id，由 plan 读取额度/有效期
//   - Group 模式（v3）：填 group_id，可填 validity_days
//   - 钱包充值模式 (credits)：只填 wallet_initial_usd（>0），创建或充值用户级永久 credits 钱包
type AssignSubscriptionRequest struct {
	UserID           int64    `json:"user_id" binding:"required"`
	GroupID          int64    `json:"group_id"`
	ValidityDays     int      `json:"validity_days" binding:"omitempty,max=36500"` // max 100 years
	Notes            string   `json:"notes"`
	WalletInitialUSD *float64 `json:"wallet_initial_usd" binding:"omitempty,gt=0,lte=10000000"`
	PlanID           *int64   `json:"plan_id" binding:"omitempty,gt=0"`
}

type adminAssignIdempotencyPayload struct {
	Mode             string  `json:"mode"`
	UserID           int64   `json:"user_id"`
	GroupID          int64   `json:"group_id,omitempty"`
	ValidityDays     int     `json:"validity_days,omitempty"`
	Notes            string  `json:"notes,omitempty"`
	WalletInitialUSD float64 `json:"wallet_initial_usd,omitempty"`
	PlanID           int64   `json:"plan_id,omitempty"`
}

func assignSubscriptionInputFromRequest(req AssignSubscriptionRequest, adminID int64) *service.AssignSubscriptionInput {
	planType := ""
	if req.WalletInitialUSD != nil {
		planType = service.PlanTypeCredits
	}

	return &service.AssignSubscriptionInput{
		UserID:           req.UserID,
		GroupID:          req.GroupID,
		ValidityDays:     req.ValidityDays,
		AssignedBy:       adminID,
		Notes:            req.Notes,
		WalletInitialUSD: req.WalletInitialUSD,
		PlanID:           req.PlanID,
		PlanType:         planType,
	}
}

func adminAssignPayloadFromInput(input *service.AssignSubscriptionInput) adminAssignIdempotencyPayload {
	payload := adminAssignIdempotencyPayload{
		UserID:       input.UserID,
		ValidityDays: input.ValidityDays,
		Notes:        input.Notes,
	}
	switch {
	case input.PlanID != nil:
		payload.Mode = "plan"
		payload.PlanID = *input.PlanID
	case input.WalletInitialUSD != nil:
		payload.Mode = "wallet"
		payload.WalletInitialUSD = *input.WalletInitialUSD
	default:
		payload.Mode = "group"
		payload.GroupID = input.GroupID
	}
	return payload
}

// BulkAssignSubscriptionRequest represents bulk assign subscription request
type BulkAssignSubscriptionRequest struct {
	UserIDs      []int64 `json:"user_ids" binding:"required,min=1"`
	GroupID      int64   `json:"group_id" binding:"required"`
	ValidityDays int     `json:"validity_days" binding:"omitempty,max=36500"` // max 100 years
	Notes        string  `json:"notes"`
}

// AdjustSubscriptionRequest represents adjust subscription request (extend or shorten)
type AdjustSubscriptionRequest struct {
	Days int `json:"days" binding:"required,min=-36500,max=36500"` // negative to shorten, positive to extend
}

// List handles listing all subscriptions with pagination and filters
// GET /api/v1/admin/subscriptions
func (h *SubscriptionHandler) List(c *gin.Context) {
	page, pageSize := response.ParsePagination(c)

	// Parse optional filters
	var userID, groupID *int64
	if userIDStr := c.Query("user_id"); userIDStr != "" {
		if id, err := strconv.ParseInt(userIDStr, 10, 64); err == nil {
			userID = &id
		}
	}
	if groupIDStr := c.Query("group_id"); groupIDStr != "" {
		if id, err := strconv.ParseInt(groupIDStr, 10, 64); err == nil {
			groupID = &id
		}
	}
	status := c.Query("status")
	platform := c.Query("platform")

	// Parse sorting parameters
	sortBy := c.DefaultQuery("sort_by", "created_at")
	sortOrder := c.DefaultQuery("sort_order", "desc")

	subscriptions, pagination, err := h.subscriptionService.List(c.Request.Context(), page, pageSize, userID, groupID, status, platform, sortBy, sortOrder)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	out := make([]dto.AdminUserSubscription, 0, len(subscriptions))
	for i := range subscriptions {
		out = append(out, *dto.UserSubscriptionFromServiceAdmin(&subscriptions[i]))
	}
	response.PaginatedWithResult(c, out, toResponsePagination(pagination))
}

// GetByID handles getting a subscription by ID
// GET /api/v1/admin/subscriptions/:id
func (h *SubscriptionHandler) GetByID(c *gin.Context) {
	subscriptionID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid subscription ID")
		return
	}

	subscription, err := h.subscriptionService.GetByID(c.Request.Context(), subscriptionID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	response.Success(c, dto.UserSubscriptionFromServiceAdmin(subscription))
}

// GetProgress handles getting subscription usage progress
// GET /api/v1/admin/subscriptions/:id/progress
func (h *SubscriptionHandler) GetProgress(c *gin.Context) {
	subscriptionID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid subscription ID")
		return
	}

	progress, err := h.subscriptionService.GetSubscriptionProgress(c.Request.Context(), subscriptionID)
	if err != nil {
		response.NotFound(c, "Subscription not found")
		return
	}

	response.Success(c, progress)
}

// Assign handles assigning a subscription to a user
// POST /api/v1/admin/subscriptions/assign
func (h *SubscriptionHandler) Assign(c *gin.Context) {
	var req AssignSubscriptionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}

	// Get admin user ID from context
	adminID := getAdminIDFromContext(c)
	adminInput, err := service.NormalizeAdminSubscriptionAssignInput(assignSubscriptionInputFromRequest(req, adminID))
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	idempotencyPayload := adminAssignPayloadFromInput(adminInput)

	assigner := h.subscriptionAssigner
	if assigner == nil {
		assigner = h.subscriptionService
	}
	transactionRunner := h.assignmentTransactionRunner
	if transactionRunner == nil {
		transactionRunner = h.subscriptionService
	}
	rebateAccruer := h.affiliateRebateAccruer
	if rebateAccruer == nil {
		rebateAccruer = h.affiliateService
	}
	var runInTransaction func(context.Context, func(context.Context) error) error
	if transactionRunner != nil {
		runInTransaction = transactionRunner.RunAssignmentTransaction
	}

	executeAdminStrictIdempotentJSON(c, "admin.subscriptions.assign", idempotencyPayload, service.DefaultWriteIdempotencyTTL(), runInTransaction, func(ctx context.Context) (any, error) {
		subscription, err := assigner.AssignAdminSubscription(ctx, adminInput)
		if err != nil {
			return nil, err
		}

		// Keep rebate accrual inside the same idempotency boundary as the wallet or
		// monthly assignment. A replay returns the stored response and reaches
		// neither side effect again.
		if rebateAccruer != nil {
			if baseAmount := adminAssignBaseAmount(adminInput, subscription); baseAmount > 0 {
				// 差异化返利：余额卡 10% / 月卡及其它 0%（与兑换码口径一致，月卡不给佣金）。
				override := service.AffiliateRebateOverrideForAdminAssign(adminInput.PlanID, subscription.AssignmentPlanType)
				if _, rebateErr := rebateAccruer.AccrueInviteRebateForOrderWithOverride(ctx, subscription.UserID, baseAmount, override, nil); rebateErr != nil {
					slog.Warn("admin assign: affiliate rebate failed", "userID", subscription.UserID, "subscriptionID", subscription.ID, "err", rebateErr)
					return nil, rebateErr
				}
			}
		}

		return dto.UserSubscriptionFromServiceAdmin(subscription), nil
	})
}

// BulkAssign handles bulk assigning subscriptions to multiple users
// POST /api/v1/admin/subscriptions/bulk-assign
func (h *SubscriptionHandler) BulkAssign(c *gin.Context) {
	var req BulkAssignSubscriptionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}

	// Get admin user ID from context
	adminID := getAdminIDFromContext(c)

	assigner := h.subscriptionBulkAssigner
	if assigner == nil {
		assigner = h.subscriptionService
	}
	executeAdminStrictIdempotentJSONNonTransactional(c, "admin.subscriptions.bulk_assign", req, service.DefaultWriteIdempotencyTTL(), func(ctx context.Context) (any, error) {
		result, err := assigner.BulkAssignSubscription(ctx, &service.BulkAssignSubscriptionInput{
			UserIDs:      req.UserIDs,
			GroupID:      req.GroupID,
			ValidityDays: req.ValidityDays,
			AssignedBy:   adminID,
			Notes:        req.Notes,
		})
		if err != nil {
			return nil, err
		}
		return dto.BulkAssignResultFromService(result), nil
	})
}

// Extend handles adjusting a subscription (extend or shorten)
// POST /api/v1/admin/subscriptions/:id/extend
func (h *SubscriptionHandler) Extend(c *gin.Context) {
	subscriptionID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid subscription ID")
		return
	}

	var req AdjustSubscriptionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}

	idempotencyPayload := struct {
		SubscriptionID int64                     `json:"subscription_id"`
		Body           AdjustSubscriptionRequest `json:"body"`
	}{
		SubscriptionID: subscriptionID,
		Body:           req,
	}
	transactionRunner := h.assignmentTransactionRunner
	if transactionRunner == nil {
		transactionRunner = h.subscriptionService
	}
	var runInTransaction func(context.Context, func(context.Context) error) error
	if transactionRunner != nil {
		runInTransaction = transactionRunner.RunAssignmentTransaction
	}
	executeAdminStrictIdempotentJSONWithPostCommit(c, "admin.subscriptions.extend", idempotencyPayload, service.DefaultWriteIdempotencyTTL(), runInTransaction, func(ctx context.Context) error {
		return h.subscriptionService.InvalidateSubscriptionCachesAfterCommit(ctx, subscriptionID)
	}, func(ctx context.Context) (any, error) {
		subscription, execErr := h.subscriptionService.ExtendSubscription(ctx, subscriptionID, req.Days)
		if execErr != nil {
			return nil, execErr
		}
		return dto.UserSubscriptionFromServiceAdmin(subscription), nil
	})
}

// ResetSubscriptionQuotaRequest represents the reset quota request
type ResetSubscriptionQuotaRequest struct {
	Daily   bool `json:"daily"`
	Weekly  bool `json:"weekly"`
	Monthly bool `json:"monthly"`
}

// ResetQuota resets daily, weekly, and/or monthly usage for a subscription.
// POST /api/v1/admin/subscriptions/:id/reset-quota
func (h *SubscriptionHandler) ResetQuota(c *gin.Context) {
	subscriptionID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid subscription ID")
		return
	}
	var req ResetSubscriptionQuotaRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	if !req.Daily && !req.Weekly && !req.Monthly {
		response.BadRequest(c, "At least one of 'daily', 'weekly', or 'monthly' must be true")
		return
	}
	sub, err := h.subscriptionService.AdminResetQuota(c.Request.Context(), subscriptionID, req.Daily, req.Weekly, req.Monthly)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, dto.UserSubscriptionFromServiceAdmin(sub))
}

// Revoke handles revoking a subscription
// DELETE /api/v1/admin/subscriptions/:id
func (h *SubscriptionHandler) Revoke(c *gin.Context) {
	subscriptionID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid subscription ID")
		return
	}

	err = h.subscriptionService.RevokeSubscription(c.Request.Context(), subscriptionID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	response.Success(c, gin.H{"message": "Subscription revoked successfully"})
}

// ListByGroup handles listing subscriptions for a specific group
// GET /api/v1/admin/groups/:id/subscriptions
func (h *SubscriptionHandler) ListByGroup(c *gin.Context) {
	groupID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid group ID")
		return
	}

	page, pageSize := response.ParsePagination(c)

	subscriptions, pagination, err := h.subscriptionService.ListGroupSubscriptions(c.Request.Context(), groupID, page, pageSize)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	out := make([]dto.AdminUserSubscription, 0, len(subscriptions))
	for i := range subscriptions {
		out = append(out, *dto.UserSubscriptionFromServiceAdmin(&subscriptions[i]))
	}
	response.PaginatedWithResult(c, out, toResponsePagination(pagination))
}

// ListByUser handles listing subscriptions for a specific user
// GET /api/v1/admin/users/:id/subscriptions
func (h *SubscriptionHandler) ListByUser(c *gin.Context) {
	userID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid user ID")
		return
	}

	subscriptions, err := h.subscriptionService.ListUserSubscriptions(c.Request.Context(), userID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	out := make([]dto.AdminUserSubscription, 0, len(subscriptions))
	for i := range subscriptions {
		out = append(out, *dto.UserSubscriptionFromServiceAdmin(&subscriptions[i]))
	}
	response.Success(c, out)
}

// Helper function to get admin ID from context
func getAdminIDFromContext(c *gin.Context) int64 {
	subject, ok := middleware2.GetAuthSubjectFromContext(c)
	if !ok {
		return 0
	}
	return subject.UserID
}

// adminAssignBaseAmount returns the USD value applied by this admin assignment.
// A wallet's wallet_initial_usd is cumulative after top-ups, so it must never be
// used as the rebate base for a credits plan. Manual wallet assignments use the
// request delta; plan wallet assignments use the runtime delta produced by the
// subscription service. Group subscriptions keep their existing monthly-quota
// semantics. Returns 0 when the current-operation value cannot be determined.
func adminAssignBaseAmount(input *service.AssignSubscriptionInput, sub *service.UserSubscription) float64 {
	if input == nil || sub == nil {
		return 0
	}
	if input.WalletInitialUSD != nil && *input.WalletInitialUSD > 0 {
		return *input.WalletInitialUSD
	}
	if input.PlanID != nil && sub.WalletInitialUSD != nil {
		if sub.WalletCreditDeltaUSD != nil && *sub.WalletCreditDeltaUSD > 0 {
			return *sub.WalletCreditDeltaUSD
		}
		return 0
	}
	if sub.Group != nil && sub.Group.MonthlyLimitUSD != nil && *sub.Group.MonthlyLimitUSD > 0 {
		return *sub.Group.MonthlyLimitUSD
	}
	return 0
}
