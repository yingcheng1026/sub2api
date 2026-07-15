package main

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestServerShutdownTimeoutDefault(t *testing.T) {
	t.Setenv("SERVER_SHUTDOWN_TIMEOUT_SECONDS", "")

	if got := serverShutdownTimeout(); got != defaultServerShutdownTimeout {
		t.Fatalf("serverShutdownTimeout() = %s, want %s", got, defaultServerShutdownTimeout)
	}
}

func TestSecurityMigrationPrecedesWorkerGraphConstruction(t *testing.T) {
	source, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	migration := strings.Index(text, "migrateSecuritySecretsBeforeWorkers(cfg)")
	workers := strings.Index(text, "initializeApplication(buildInfo)")
	if migration < 0 || workers < 0 || migration >= workers {
		t.Fatalf("security migration must run before Wire constructs background workers")
	}
	if !strings.Contains(text, "repository.MigrateDomainBoundSecrets(domainCtx, client, secretEncryptor)") {
		t.Fatal("security preflight must migrate domain-bound secrets before workers start")
	}
	if !strings.Contains(text, "migratedDomainSecrets.ProxyCredentials") {
		t.Fatal("security preflight must report migrated proxy credentials")
	}
	purge := strings.Index(text, "prepareEncryptedSchedulerCacheBeforeWorkers(cfg)")
	if purge < 0 || purge >= workers {
		t.Fatal("legacy plaintext scheduler-cache purge must run before Wire constructs background workers")
	}
	oauthPurge := strings.Index(text, "prepareEncryptedOAuthTokenCacheBeforeWorkers(cfg)")
	if oauthPurge < 0 || oauthPurge >= workers {
		t.Fatal("legacy plaintext oauth-token-cache purge must run before Wire constructs background workers")
	}
}

func TestServerShutdownTimeoutFromEnv(t *testing.T) {
	t.Setenv("SERVER_SHUTDOWN_TIMEOUT_SECONDS", "42")

	if got := serverShutdownTimeout(); got != 42*time.Second {
		t.Fatalf("serverShutdownTimeout() = %s, want 42s", got)
	}
}

func TestServerShutdownTimeoutInvalidEnvFallsBack(t *testing.T) {
	for _, value := range []string{"0", "-1", "not-a-number"} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("SERVER_SHUTDOWN_TIMEOUT_SECONDS", value)

			if got := serverShutdownTimeout(); got != defaultServerShutdownTimeout {
				t.Fatalf("serverShutdownTimeout() = %s, want %s", got, defaultServerShutdownTimeout)
			}
		})
	}
}

func TestSetupServerAddressIgnoresPublicHostAndBindsLoopback(t *testing.T) {
	t.Setenv("SERVER_HOST", "0.0.0.0")
	t.Setenv("SERVER_PORT", "9123")

	if got := setupServerAddress(); got != "127.0.0.1:9123" {
		t.Fatalf("setupServerAddress()=%q, want loopback-only address", got)
	}
}
