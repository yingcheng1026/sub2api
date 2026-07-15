package service

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
)

func TestValidateUpstreamBaseURLFormatIgnoresHostAllowlist(t *testing.T) {
	cfg := &config.Config{
		Security: config.SecurityConfig{
			URLAllowlist: config.URLAllowlistConfig{
				Enabled:       true,
				UpstreamHosts: []string{"api.openai.com"},
			},
		},
	}

	normalized, err := validateUpstreamBaseURLFormat("https://custom-upstream.example.com/v1/", cfg)
	if err != nil {
		t.Fatalf("expected custom upstream host to pass without allowlist, got %v", err)
	}
	if normalized != "https://custom-upstream.example.com/v1" {
		t.Fatalf("expected normalized URL, got %q", normalized)
	}
}

func TestValidateUpstreamBaseURLFormatStillRejectsInvalidURL(t *testing.T) {
	if _, err := validateUpstreamBaseURLFormat("://bad", &config.Config{}); err == nil {
		t.Fatalf("expected invalid URL to fail")
	}
}

func TestValidateUpstreamBaseURLFormatHonorsHTTPPolicy(t *testing.T) {
	strictCfg := &config.Config{
		Security: config.SecurityConfig{
			URLAllowlist: config.URLAllowlistConfig{AllowInsecureHTTP: false},
		},
	}
	if _, err := validateUpstreamBaseURLFormat("http://custom-upstream.example.com", strictCfg); err == nil {
		t.Fatalf("expected http URL to fail when allow_insecure_http is false")
	}

	relaxedCfg := &config.Config{
		Security: config.SecurityConfig{
			URLAllowlist: config.URLAllowlistConfig{AllowInsecureHTTP: true},
		},
	}
	if _, err := validateUpstreamBaseURLFormat("http://custom-upstream.example.com", relaxedCfg); err != nil {
		t.Fatalf("expected http URL to pass when allow_insecure_http is true, got %v", err)
	}
}

func TestValidateUpstreamBaseURLFormatRejectsPrivateHostsWithoutAllowlistMode(t *testing.T) {
	cfg := &config.Config{
		Security: config.SecurityConfig{
			URLAllowlist: config.URLAllowlistConfig{
				Enabled:           false,
				AllowPrivateHosts: false,
			},
		},
	}
	for _, raw := range []string{"https://localhost", "https://127.0.0.1", "https://169.254.169.254"} {
		if _, err := validateUpstreamBaseURLFormat(raw, cfg); err == nil {
			t.Fatalf("private upstream %q was accepted", raw)
		}
	}

	cfg.Security.URLAllowlist.AllowPrivateHosts = true
	if _, err := validateUpstreamBaseURLFormat("https://127.0.0.1", cfg); err != nil {
		t.Fatalf("explicit private-host opt-in was not honored: %v", err)
	}
}
