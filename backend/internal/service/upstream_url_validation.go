package service

import (
	"fmt"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/util/urlvalidator"
)

func validateUpstreamBaseURLFormat(raw string, cfg *config.Config) (string, error) {
	allowInsecureHTTP := false
	if cfg != nil {
		allowInsecureHTTP = cfg.Security.URLAllowlist.AllowInsecureHTTP
	}
	normalized, err := urlvalidator.ValidateURLFormat(raw, allowInsecureHTTP)
	if err != nil {
		return "", fmt.Errorf("invalid base_url: %w", err)
	}
	return normalized, nil
}
