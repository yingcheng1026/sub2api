package service

import (
	"context"
	"strconv"
	"strings"

	"github.com/Wei-Shaw/sub2api/ent/subscriptionplangroup"
)

const (
	monthlyLockedRateGPTKiro     = 1.0
	monthlyLockedRateAntigravity = 3.5
	monthlyLockedRateClaude      = 8.5
)

func monthlyLockedRateForGroup(name, platform string) (float64, bool) {
	n := strings.ToLower(strings.TrimSpace(name))
	p := strings.ToLower(strings.TrimSpace(platform))
	switch {
	case strings.Contains(n, "antigravity") || p == "antigravity":
		return monthlyLockedRateAntigravity, true
	case strings.Contains(n, "kiro") || strings.Contains(n, "cc-default"):
		return monthlyLockedRateGPTKiro, true
	case p == "openai" || strings.Contains(n, "openai") || strings.Contains(n, "gpt"):
		return monthlyLockedRateGPTKiro, true
	case p == "anthropic" || strings.Contains(n, "claude"):
		return monthlyLockedRateClaude, true
	default:
		return 0, false
	}
}

func (s *SubscriptionService) buildMonthlyLockedRatesForPlan(ctx context.Context, input *AssignSubscriptionInput) (map[string]float64, error) {
	if s == nil || s.entClient == nil || input == nil || input.PlanID == nil || input.IsCreditsAssign() {
		return nil, nil
	}

	rows, err := s.entClient.SubscriptionPlanGroup.Query().
		Where(subscriptionplangroup.PlanIDEQ(*input.PlanID)).
		WithGroup().
		All(ctx)
	if err != nil {
		return nil, err
	}

	rates := make(map[string]float64, len(rows))
	for _, row := range rows {
		if row == nil || row.GroupID <= 0 || row.Edges.Group == nil {
			continue
		}
		if rate, ok := monthlyLockedRateForGroup(row.Edges.Group.Name, row.Edges.Group.Platform); ok {
			rates[strconv.FormatInt(row.GroupID, 10)] = rate
		}
	}
	if len(rates) == 0 {
		return nil, nil
	}
	return rates, nil
}
