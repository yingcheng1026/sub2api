package service

import "github.com/Wei-Shaw/sub2api/internal/config"

func resolveGatewayMaxLineSize(cfg *config.Config) int {
	limit := defaultMaxLineSize
	if cfg != nil && cfg.Gateway.MaxLineSize > 0 {
		limit = cfg.Gateway.MaxLineSize
	}
	if limit > config.MaxGatewayMaxLineSize {
		return config.MaxGatewayMaxLineSize
	}
	return limit
}
