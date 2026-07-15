package payment

import (
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
)

func TestProvideEncryptionKeyRejectsMissingIndependentRoot(t *testing.T) {
	t.Parallel()

	_, err := ProvideEncryptionKey(&config.Config{})
	if err == nil || !strings.Contains(err.Error(), "SECRET_ENCRYPTION_PAYMENT_PROVIDER_KEY") {
		t.Fatalf("ProvideEncryptionKey error = %v", err)
	}
}

func TestProvideEncryptionKeyUsesIndependentPaymentProviderRoot(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		SecretEncryption: config.SecretEncryptionConfig{
			PaymentProviderKey: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		},
	}

	key, err := ProvideEncryptionKey(cfg)
	if err != nil {
		t.Fatalf("ProvideEncryptionKey returned error: %v", err)
	}
	if len(key) != 32 {
		t.Fatalf("encryption key len = %d, want 32", len(key))
	}
}

func TestProvideEncryptionKeyRejectsConfiguredInvalidLength(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		SecretEncryption: config.SecretEncryptionConfig{
			PaymentProviderKey: "abcd",
		},
	}

	_, err := ProvideEncryptionKey(cfg)
	if err == nil {
		t.Fatal("expected error for invalid key length")
	}
}

func TestProvideLegacyEncryptionKeyIsMigrationOnly(t *testing.T) {
	t.Parallel()

	missing, err := ProvideLegacyEncryptionKey(&config.Config{})
	if err != nil || len(missing) != 0 {
		t.Fatalf("missing legacy key = %x, err=%v", missing, err)
	}
	cfg := &config.Config{Totp: config.TotpConfig{
		EncryptionKey:           strings.Repeat("a", 64),
		EncryptionKeyConfigured: true,
	}}
	key, err := ProvideLegacyEncryptionKey(cfg)
	if err != nil || len(key) != 32 {
		t.Fatalf("legacy key len = %d, err=%v", len(key), err)
	}
}
