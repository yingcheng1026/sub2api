package payment

import (
	"encoding/hex"
	"fmt"
	"strings"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/google/wire"
)

// EncryptionKey is a named type for the payment encryption key (AES-256, 32 bytes).
// Using a named type avoids Wire ambiguity with other []byte parameters.
type EncryptionKey []byte

// ProvideEncryptionKey returns the independent v3 payment-provider root.
func ProvideEncryptionKey(cfg *config.Config) (EncryptionKey, error) {
	if cfg == nil {
		return nil, fmt.Errorf("SECRET_ENCRYPTION_PAYMENT_PROVIDER_KEY must be explicitly configured and stable")
	}
	key, err := decodeConfiguredEncryptionKey(
		"SECRET_ENCRYPTION_PAYMENT_PROVIDER_KEY",
		cfg.SecretEncryption.PaymentProviderKey,
	)
	return EncryptionKey(key), err
}

// ProvideLegacyEncryptionKey returns the optional shared v1/v2 root used only
// by the startup payment-provider migration.
func ProvideLegacyEncryptionKey(cfg *config.Config) (EncryptionKey, error) {
	if cfg == nil || !cfg.Totp.EncryptionKeyConfigured {
		return nil, nil
	}
	key, err := decodeConfiguredEncryptionKey("TOTP_ENCRYPTION_KEY", cfg.Totp.EncryptionKey)
	return EncryptionKey(key), err
}

func decodeConfiguredEncryptionKey(name, value string) ([]byte, error) {
	keyHex := strings.TrimSpace(value)
	if keyHex == "" {
		return nil, fmt.Errorf("%s must be explicitly configured and stable", name)
	}
	key, err := hex.DecodeString(keyHex)
	if err != nil {
		return nil, fmt.Errorf("invalid %s (hex decode): %w", name, err)
	}
	if len(key) != 32 {
		return nil, fmt.Errorf("%s must be 32 bytes, got %d", name, len(key))
	}
	return key, nil
}

// ProvideRegistry creates an empty payment provider registry.
// Providers are registered at runtime after application startup.
func ProvideRegistry() *Registry {
	return NewRegistry()
}

// ProvideDefaultLoadBalancer creates a DefaultLoadBalancer backed by the ent client.
func ProvideDefaultLoadBalancer(client *dbent.Client, key EncryptionKey) *DefaultLoadBalancer {
	return NewDefaultLoadBalancer(client, []byte(key))
}

// ProviderSet is the Wire provider set for the payment package.
var ProviderSet = wire.NewSet(
	ProvideEncryptionKey,
	ProvideRegistry,
	ProvideDefaultLoadBalancer,
	wire.Bind(new(LoadBalancer), new(*DefaultLoadBalancer)),
)
