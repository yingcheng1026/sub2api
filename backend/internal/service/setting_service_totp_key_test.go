package service

import (
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestIsTotpEncryptionKeyConfiguredUsesDedicatedDomainRoot(t *testing.T) {
	cfg := &config.Config{Totp: config.TotpConfig{
		EncryptionKey:           strings.Repeat("a", 64),
		EncryptionKeyConfigured: true,
	}}
	svc := NewSettingService(nil, cfg)
	require.False(t, svc.IsTotpEncryptionKeyConfigured())

	cfg.SecretEncryption.TOTPSecretKey = strings.Repeat("1", 64)
	require.True(t, svc.IsTotpEncryptionKeyConfigured())
}
