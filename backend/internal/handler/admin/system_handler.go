package admin

import (
	"context"
	"net/http"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

// SystemHandler handles system-related operations
type SystemHandler struct {
	updateSvc systemUpdateService
	lockSvc   *service.SystemOperationLockService
}

type systemUpdateService interface {
	CheckUpdate(ctx context.Context, force bool) (*service.UpdateInfo, error)
	PerformUpdate(ctx context.Context) error
	Rollback() error
	ListRollbackVersions(ctx context.Context) ([]service.RollbackVersion, error)
	RollbackToVersion(ctx context.Context, version string) error
}

// NewSystemHandler creates a new SystemHandler
func NewSystemHandler(updateSvc systemUpdateService, lockSvc *service.SystemOperationLockService) *SystemHandler {
	return &SystemHandler{
		updateSvc: updateSvc,
		lockSvc:   lockSvc,
	}
}

// GetVersion returns the current version
// GET /api/v1/admin/system/version
func (h *SystemHandler) GetVersion(c *gin.Context) {
	info, _ := h.updateSvc.CheckUpdate(c.Request.Context(), false)
	response.Success(c, gin.H{
		"version": info.CurrentVersion,
	})
}

// CheckUpdates checks for available updates
// GET /api/v1/admin/system/check-updates
func (h *SystemHandler) CheckUpdates(c *gin.Context) {
	force := c.Query("force") == "true"
	info, err := h.updateSvc.CheckUpdate(c.Request.Context(), force)
	if err != nil {
		response.Error(c, http.StatusInternalServerError, err.Error())
		return
	}
	response.Success(c, info)
}

// PerformUpdate is disabled for immutable deployments.
// POST /api/v1/admin/system/update
func (h *SystemHandler) PerformUpdate(c *gin.Context) {
	response.ErrorFrom(c, service.ErrImmutableDeploymentRequired)
}

// GetRollbackVersions lists versions available for rollback
// GET /api/v1/admin/system/rollback-versions
func (h *SystemHandler) GetRollbackVersions(c *gin.Context) {
	versions, err := h.updateSvc.ListRollbackVersions(c.Request.Context())
	if err != nil {
		response.Error(c, http.StatusInternalServerError, err.Error())
		return
	}
	response.Success(c, gin.H{
		"versions": versions,
	})
}

// Rollback is disabled for immutable deployments.
// POST /api/v1/admin/system/rollback
func (h *SystemHandler) Rollback(c *gin.Context) {
	response.ErrorFrom(c, service.ErrImmutableDeploymentRequired)
}

// RestartService is disabled for immutable deployments.
// POST /api/v1/admin/system/restart
func (h *SystemHandler) RestartService(c *gin.Context) {
	response.ErrorFrom(c, service.ErrImmutableDeploymentRequired)
}
