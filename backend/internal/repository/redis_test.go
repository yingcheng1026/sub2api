package repository

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func newTotpCacheTestEncryptor() *AESEncryptor {
	return &AESEncryptor{domainKeys: map[string][]byte{
		service.SecretDomainTOTPCache: bytes.Repeat([]byte{0x42}, 32),
	}}
}

func TestBuildRedisOptions(t *testing.T) {
	cfg := &config.Config{
		Redis: config.RedisConfig{
			Host:                "localhost",
			Port:                6379,
			Password:            "secret",
			DB:                  2,
			DialTimeoutSeconds:  5,
			ReadTimeoutSeconds:  3,
			WriteTimeoutSeconds: 4,
			PoolSize:            100,
			MinIdleConns:        10,
		},
	}

	opts := buildRedisOptions(cfg)
	require.Equal(t, "localhost:6379", opts.Addr)
	require.Equal(t, "secret", opts.Password)
	require.Equal(t, 2, opts.DB)
	require.Equal(t, 5*time.Second, opts.DialTimeout)
	require.Equal(t, 3*time.Second, opts.ReadTimeout)
	require.Equal(t, 4*time.Second, opts.WriteTimeout)
	require.Equal(t, 100, opts.PoolSize)
	require.Equal(t, 10, opts.MinIdleConns)
	require.Nil(t, opts.TLSConfig)

	// Test case with TLS enabled
	cfgTLS := &config.Config{
		Redis: config.RedisConfig{
			Host:      "localhost",
			EnableTLS: true,
		},
	}
	optsTLS := buildRedisOptions(cfgTLS)
	require.NotNil(t, optsTLS.TLSConfig)
	require.Equal(t, "localhost", optsTLS.TLSConfig.ServerName)
}

func TestEncodeTotpSetupSessionEncryptsSecretAndSetupToken(t *testing.T) {
	session := &service.TotpSetupSession{
		Secret:     "JBSWY3DPEHPK3PXP",
		SetupToken: "setup-token-that-must-not-be-plaintext",
		CreatedAt:  time.Date(2026, 7, 13, 1, 2, 3, 0, time.UTC),
	}

	encryptor := newTotpCacheTestEncryptor()
	encoded, err := encodeTotpSetupSession(session, encryptor)
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(string(encoded), totpSetupEncryptedV2Prefix))
	require.NotContains(t, string(encoded), session.Secret)
	require.NotContains(t, string(encoded), session.SetupToken)

	decoded, err := decodeTotpSetupSession(encoded, encryptor)
	require.NoError(t, err)
	require.Equal(t, session, decoded)
}

func TestDecodeTotpSetupSessionRejectsLegacyPlaintextJSON(t *testing.T) {
	legacy := &service.TotpSetupSession{
		Secret:     "LEGACYSECRET",
		SetupToken: "legacy-setup-token",
		CreatedAt:  time.Date(2026, 7, 13, 1, 2, 3, 0, time.UTC),
	}
	data, err := json.Marshal(legacy)
	require.NoError(t, err)

	_, err = decodeTotpSetupSession(data, newTotpCacheTestEncryptor())
	require.Error(t, err)
}

func TestDecodeTotpSetupSessionRejectsInvalidCiphertext(t *testing.T) {
	_, err := decodeTotpSetupSession([]byte(totpSetupEncryptedV2Prefix+"not-base64"), newTotpCacheTestEncryptor())
	require.Error(t, err)
}

func TestEncodeTotpSetupSessionFailsClosedWithoutEncryptor(t *testing.T) {
	_, err := encodeTotpSetupSession(&service.TotpSetupSession{
		Secret:     "must-not-fall-back-to-plaintext",
		SetupToken: "must-not-fall-back-to-plaintext",
	}, nil)
	require.Error(t, err)
}
