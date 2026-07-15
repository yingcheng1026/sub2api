package service

import (
	"fmt"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/util/urlvalidator"
)

func validateUpstreamBaseURLFormat(raw string, cfg *config.Config) (string, error) {
	allowInsecureHTTP := false
	allowPrivateHosts := false
	if cfg != nil {
		allowInsecureHTTP = cfg.Security.URLAllowlist.AllowInsecureHTTP
		allowPrivateHosts = cfg.Security.URLAllowlist.AllowPrivateHosts
	}
	normalized, err := urlvalidator.ValidateHTTPURL(raw, allowInsecureHTTP, urlvalidator.ValidationOptions{
		AllowPrivate: allowPrivateHosts,
	})
	if err != nil {
		return "", fmt.Errorf("invalid base_url: %w", err)
	}
	return normalized, nil
}
