package service

import (
	"testing"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

// TestValidatePlanType 验证 B2.5 plan_type 兜底：空串 → subscription；
// 取值非法 → BadRequest PLAN_TYPE_INVALID。
func TestValidatePlanType(t *testing.T) {
	t.Run("空串默认 subscription", func(t *testing.T) {
		got, err := validatePlanType("")
		require.NoError(t, err)
		require.Equal(t, PlanTypeSubscription, got)
	})
	t.Run("空白也算空串", func(t *testing.T) {
		got, err := validatePlanType("   ")
		require.NoError(t, err)
		require.Equal(t, PlanTypeSubscription, got)
	})
	t.Run("subscription 合法", func(t *testing.T) {
		got, err := validatePlanType("subscription")
		require.NoError(t, err)
		require.Equal(t, PlanTypeSubscription, got)
	})
	t.Run("credits 合法", func(t *testing.T) {
		got, err := validatePlanType("credits")
		require.NoError(t, err)
		require.Equal(t, PlanTypeCredits, got)
	})
	t.Run("其他取值被拒", func(t *testing.T) {
		_, err := validatePlanType("trial")
		require.Error(t, err)
		require.Equal(t, "PLAN_TYPE_INVALID", infraerrors.Reason(err))
	})
}

// TestValidatePlanPatchPlanType 验证 patch 路径下 plan_type 也走相同校验。
func TestValidatePlanPatchPlanType(t *testing.T) {
	bad := "garbage"
	err := validatePlanPatch(UpdatePlanRequest{PlanType: &bad})
	require.Error(t, err)
	require.Equal(t, "PLAN_TYPE_INVALID", infraerrors.Reason(err))

	ok := "credits"
	require.NoError(t, validatePlanPatch(UpdatePlanRequest{PlanType: &ok}))
}
