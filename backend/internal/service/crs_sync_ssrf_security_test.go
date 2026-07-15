package service

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
)

func TestCRSSyncRejectsPrivateDestinationWhenHostAllowlistDisabled(t *testing.T) {
	svc := NewCRSSyncService(nil, nil, nil, nil, nil, &config.Config{
		Security: config.SecurityConfig{
			URLAllowlist: config.URLAllowlistConfig{
				Enabled:           false,
				AllowPrivateHosts: false,
			},
		},
	})

	for _, raw := range []string{"https://127.0.0.1:8443", "https://169.254.169.254", "https://localhost"} {
		if _, err := svc.fetchCRSExport(context.Background(), raw, "admin", "secret"); err == nil {
			t.Fatalf("private CRS destination %q was accepted", raw)
		}
	}
}

func TestCRSSyncAlwaysRequiresHTTPSForCredentials(t *testing.T) {
	svc := NewCRSSyncService(nil, nil, nil, nil, nil, &config.Config{
		Security: config.SecurityConfig{
			URLAllowlist: config.URLAllowlistConfig{
				Enabled:           false,
				AllowInsecureHTTP: true,
			},
		},
	})

	if _, err := svc.fetchCRSExport(context.Background(), "http://crs.example.com", "admin", "secret"); err == nil {
		t.Fatal("CRS credentials were allowed over plaintext HTTP")
	}
}

func TestCRSSyncEnabledAllowlistFailsClosedWhenEmpty(t *testing.T) {
	svc := NewCRSSyncService(nil, nil, nil, nil, nil, &config.Config{
		Security: config.SecurityConfig{
			URLAllowlist: config.URLAllowlistConfig{Enabled: true},
		},
	})

	if _, err := svc.fetchCRSExport(context.Background(), "https://crs.example.com", "admin", "secret"); err == nil {
		t.Fatal("enabled CRS allowlist accepted an empty host policy")
	}
}
