package config

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestKiroCanaryDoesNotDefaultToInsecureSimpleMode(t *testing.T) {
	raw, err := os.ReadFile("../../../deploy/docker-compose.kiro-canary.yml")
	require.NoError(t, err)
	require.Contains(t, string(raw), "RUN_MODE=${RUN_MODE:-standard}")
	require.NotContains(t, string(raw), "RUN_MODE=${RUN_MODE:-simple}")
}
