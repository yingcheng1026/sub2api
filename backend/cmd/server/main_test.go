package main

import (
	"testing"
	"time"
)

func TestServerShutdownTimeoutDefault(t *testing.T) {
	t.Setenv("SERVER_SHUTDOWN_TIMEOUT_SECONDS", "")

	if got := serverShutdownTimeout(); got != defaultServerShutdownTimeout {
		t.Fatalf("serverShutdownTimeout() = %s, want %s", got, defaultServerShutdownTimeout)
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
