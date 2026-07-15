package service

import (
	"fmt"
	"strings"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

const reservedGroupPolicyViolationReason = "RESERVED_GROUP_POLICY_VIOLATION"

type reservedBusinessGroupPolicy struct {
	platform    string
	isExclusive bool
}

func reservedBusinessGroupPolicyFor(name string) (reservedBusinessGroupPolicy, bool) {
	switch name {
	case WalletDefaultOpenAIGroupName:
		return reservedBusinessGroupPolicy{
			platform:    PlatformOpenAI,
			isExclusive: false,
		}, true
	case WalletDefaultVIPGroupName:
		return reservedBusinessGroupPolicy{
			platform:    PlatformAnthropic,
			isExclusive: true,
		}, true
	default:
		return reservedBusinessGroupPolicy{}, false
	}
}

func validateReservedBusinessGroupCreate(group *Group) error {
	return validateReservedBusinessGroupState(group, false)
}

func validateReservedBusinessGroupUpdate(current, candidate *Group) error {
	if _, reserved := reservedBusinessGroupPolicyFor(current.Name); reserved {
		if candidate.Name != current.Name {
			return reservedGroupPolicyError(
				current.Name,
				"update",
				[]string{"cannot be renamed"},
			)
		}
	}
	if _, reserved := reservedBusinessGroupPolicyFor(candidate.Name); reserved && candidate.Name != current.Name {
		return reservedGroupPolicyError(
			candidate.Name,
			"update",
			[]string{"cannot be assigned by renaming another group"},
		)
	}
	return validateReservedBusinessGroupState(candidate, true)
}

func rejectReservedBusinessGroupDelete(group *Group) error {
	if group == nil {
		return nil
	}
	if _, reserved := reservedBusinessGroupPolicyFor(group.Name); !reserved {
		return nil
	}
	return reservedGroupPolicyError(group.Name, "delete", []string{"cannot be deleted"})
}

func validateReservedBusinessGroupState(group *Group, requireHydrated bool) error {
	if group == nil {
		return nil
	}
	policy, reserved := reservedBusinessGroupPolicyFor(group.Name)
	if !reserved {
		return nil
	}

	violations := make([]string, 0, 8)
	if group.Status != StatusActive {
		violations = append(violations, "status must be active")
	}
	if requireHydrated && !group.Hydrated {
		violations = append(violations, "hydrated must be true")
	}
	if group.SubscriptionType != SubscriptionTypeStandard {
		violations = append(violations, "subscription_type must be standard")
	}
	if group.IsExclusive != policy.isExclusive {
		violations = append(violations, fmt.Sprintf("is_exclusive must be %t", policy.isExclusive))
	}
	if group.Platform != policy.platform {
		violations = append(violations, fmt.Sprintf("platform must be %s", policy.platform))
	}
	if group.ClaudeCodeOnly {
		violations = append(violations, "claude_code_only must be false")
	}
	if group.FallbackGroupID != nil {
		violations = append(violations, "fallback_group_id must be unset")
	}
	if group.FallbackGroupIDOnInvalidRequest != nil {
		violations = append(violations, "fallback_group_id_on_invalid_request must be unset")
	}
	if len(violations) == 0 {
		return nil
	}
	return reservedGroupPolicyError(group.Name, "write", violations)
}

func reservedGroupPolicyError(groupName, operation string, violations []string) error {
	detail := strings.Join(violations, "; ")
	return infraerrors.BadRequest(
		reservedGroupPolicyViolationReason,
		fmt.Sprintf("reserved group %q policy violation: %s", groupName, detail),
	).WithMetadata(map[string]string{
		"group":      groupName,
		"operation":  operation,
		"violations": detail,
	})
}
