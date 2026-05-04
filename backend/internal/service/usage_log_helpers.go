package service

import "strings"

func optionalTrimmedStringPtr(raw string) *string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil
	}
	return &trimmed
}

// optionalNonEqualStringPtr returns a pointer to value if it is non-empty and
// differs from compare; otherwise nil. Used to store upstream_model only when
// it differs from the requested model.
func optionalNonEqualStringPtr(value, compare string) *string {
	if value == "" || value == compare {
		return nil
	}
	return &value
}

func forwardResultBillingModel(requestedModel, upstreamModel string) string {
	if trimmed := strings.TrimSpace(requestedModel); trimmed != "" {
		return trimmed
	}
	return strings.TrimSpace(upstreamModel)
}

func mappedBillingModelOverRequested(originalModel string, candidates ...string) string {
	original := normalizeBillingGuardModel(originalModel)
	if !isClaudeCompatBillingModel(original) {
		return ""
	}

	for _, candidate := range candidates {
		normalized := normalizeBillingGuardModel(candidate)
		if normalized == "" || normalized == original || strings.HasPrefix(normalized, "claude") {
			continue
		}
		return strings.TrimSpace(candidate)
	}
	return ""
}

func isClaudeCompatBillingModel(normalized string) bool {
	if strings.HasPrefix(normalized, "claude") {
		return true
	}
	switch normalized {
	case "opus", "sonnet", "default", "haiku":
		return true
	default:
		return false
	}
}

func normalizeBillingGuardModel(model string) string {
	normalized := strings.ToLower(strings.TrimSpace(model))
	if normalized == "" {
		return ""
	}
	if strings.Contains(normalized, "/") {
		parts := strings.Split(normalized, "/")
		normalized = strings.TrimSpace(parts[len(parts)-1])
	}
	normalized = strings.TrimSuffix(normalized, "[1m]")
	return strings.TrimSpace(normalized)
}

func optionalInt64Ptr(v int64) *int64 {
	if v == 0 {
		return nil
	}
	return &v
}
