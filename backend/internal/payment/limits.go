package payment

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"strings"
)

const maxInstanceLimitsBytes = 64 << 10

var validInstanceLimitKeys = map[string]struct{}{
	TypeAlipay:       {},
	TypeWxpay:        {},
	TypeAlipayDirect: {},
	TypeWxpayDirect:  {},
	TypeStripe:       {},
	TypeCard:         {},
	TypeLink:         {},
}

// ParseInstanceLimits strictly decodes per-provider financial limits. An empty
// string intentionally means that only the global payment limits apply.
func ParseInstanceLimits(raw string) (InstanceLimits, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return InstanceLimits{}, nil
	}
	if len(raw) > maxInstanceLimitsBytes {
		return nil, fmt.Errorf("limits exceed %d bytes", maxInstanceLimitsBytes)
	}

	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	limits := InstanceLimits{}
	if err := decoder.Decode(&limits); err != nil {
		return nil, fmt.Errorf("decode limits: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("decode limits: trailing JSON value")
		}
		return nil, fmt.Errorf("decode limits: %w", err)
	}
	if len(limits) == 0 {
		return nil, fmt.Errorf("limits object must contain at least one configured channel")
	}

	for key, channel := range limits {
		if _, ok := validInstanceLimitKeys[key]; !ok {
			return nil, fmt.Errorf("unsupported payment type %q", key)
		}
		if err := validateChannelLimits(key, channel); err != nil {
			return nil, err
		}
	}
	return limits, nil
}

// ValidateInstanceLimits also binds configured channel keys to the provider's
// supported types, preventing dormant or misspelled limits from being saved.
func ValidateInstanceLimits(raw, providerKey, supportedTypes string) error {
	limits, err := ParseInstanceLimits(raw)
	if err != nil {
		return err
	}
	for key := range limits {
		if providerKey == TypeStripe {
			if key != TypeStripe {
				return fmt.Errorf("stripe limits must use the %q key", TypeStripe)
			}
			continue
		}
		if !InstanceSupportsType(supportedTypes, key) {
			return fmt.Errorf("limits key %q is not an enabled supported type", key)
		}
	}
	return nil
}

func validateChannelLimits(key string, limits ChannelLimits) error {
	values := []struct {
		name  string
		value float64
	}{
		{name: "dailyLimit", value: limits.DailyLimit},
		{name: "singleMin", value: limits.SingleMin},
		{name: "singleMax", value: limits.SingleMax},
	}
	configured := false
	for _, item := range values {
		if math.IsNaN(item.value) || math.IsInf(item.value, 0) || item.value < 0 {
			return fmt.Errorf("%s.%s must be a finite non-negative number", key, item.name)
		}
		configured = configured || item.value > 0
	}
	if !configured {
		return fmt.Errorf("%s must configure at least one positive limit", key)
	}
	if limits.SingleMin > 0 && limits.SingleMax > 0 && limits.SingleMin > limits.SingleMax {
		return fmt.Errorf("%s.singleMin must not exceed singleMax", key)
	}
	return nil
}
