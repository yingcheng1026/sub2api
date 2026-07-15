package service

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseDebugEnvBool(t *testing.T) {
	t.Run("empty is false", func(t *testing.T) {
		if parseDebugEnvBool("") {
			t.Fatalf("expected false for empty string")
		}
	})

	t.Run("true-like values", func(t *testing.T) {
		for _, value := range []string{"1", "true", "TRUE", "yes", "on"} {
			t.Run(value, func(t *testing.T) {
				if !parseDebugEnvBool(value) {
					t.Fatalf("expected true for %q", value)
				}
			})
		}
	})

	t.Run("false-like values", func(t *testing.T) {
		for _, value := range []string{"0", "false", "off", "debug"} {
			t.Run(value, func(t *testing.T) {
				if parseDebugEnvBool(value) {
					t.Fatalf("expected false for %q", value)
				}
			})
		}
	})
}

func TestGatewayBodyDebugIsDisabledInReleaseMode(t *testing.T) {
	if gatewayBodyDebugAllowed("release") {
		t.Fatal("full gateway body logging must be disabled in release mode")
	}
	if !gatewayBodyDebugAllowed("debug") {
		t.Fatal("debug mode should permit explicitly configured body logging")
	}
}

func TestInitDebugGatewayBodyFileUsesPrivatePermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gateway-debug", "requests.log")
	svc := &GatewayService{}
	svc.initDebugGatewayBodyFile(path)
	f := svc.debugGatewayBodyFile.Load()
	if f == nil {
		t.Fatal("debug file was not initialized")
	}
	t.Cleanup(func() { _ = f.Close() })

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("debug file mode=%#o, want 0600", got)
	}
	dirInfo, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if got := dirInfo.Mode().Perm(); got != 0o700 {
		t.Fatalf("debug directory mode=%#o, want 0700", got)
	}
}

func TestSafeHeaderValueForLogRedactsCredentialHeaders(t *testing.T) {
	for _, key := range []string{
		"Authorization", "X-Api-Key", "Api-Key", "X-Goog-Api-Key",
		"Cookie", "Set-Cookie", "Proxy-Authorization", "X-Auth-Token",
	} {
		t.Run(key, func(t *testing.T) {
			if got := safeHeaderValueForLog(key, "super-secret"); got != "[redacted]" {
				t.Fatalf("%s=%q, want redacted", key, got)
			}
		})
	}
}
