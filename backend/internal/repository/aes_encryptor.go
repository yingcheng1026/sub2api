package repository

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

const (
	legacySecretDomainCiphertextPrefixV2 = "sd:v2:"
	legacySecretDomainKDFContextV2       = "sub2api/secret-domain/v2\x00"
	secretDomainCiphertextPrefix         = "sd:v3:"
	secretDomainKDFContext               = "sub2api/secret-domain/v3\x00"
)

// AESEncryptor implements SecretEncryptor using AES-256-GCM
type AESEncryptor struct {
	legacyKey  []byte
	domainKeys map[string][]byte
}

var _ service.DomainSecretEncryptor = (*AESEncryptor)(nil)

// NewAESEncryptor creates a new AES encryptor
func NewAESEncryptor(cfg *config.Config) (service.SecretEncryptor, error) {
	if cfg == nil {
		return nil, fmt.Errorf("secret encryption configuration is required")
	}
	domainKeys, err := configuredDomainKeys(cfg)
	if err != nil {
		return nil, err
	}
	legacyKey, err := configuredLegacySecretKey(cfg)
	if err != nil {
		return nil, err
	}
	if err := rejectReusedSecretRoots(cfg, legacyKey, domainKeys); err != nil {
		return nil, err
	}
	return &AESEncryptor{legacyKey: legacyKey, domainKeys: domainKeys}, nil
}

// Encrypt encrypts plaintext using AES-256-GCM
// Output format: base64(nonce + ciphertext + tag)
func (e *AESEncryptor) Encrypt(plaintext string) (string, error) {
	if len(e.legacyKey) == 0 {
		return "", fmt.Errorf("legacy TOTP_ENCRYPTION_KEY is unavailable")
	}
	return encryptAESGCM(e.legacyKey, nil, plaintext)
}

// Decrypt decrypts ciphertext using AES-256-GCM
func (e *AESEncryptor) Decrypt(ciphertext string) (string, error) {
	if isDomainCiphertext(ciphertext) {
		return "", fmt.Errorf("domain-bound ciphertext requires DecryptForDomain")
	}
	if len(e.legacyKey) == 0 {
		return "", fmt.Errorf("legacy TOTP_ENCRYPTION_KEY is unavailable")
	}
	return decryptAESGCM(e.legacyKey, nil, ciphertext)
}

// EncryptForDomain derives an independent AES key and authenticates the domain
// as GCM associated data. The expected domain is deliberately not encoded in
// the envelope, so moving ciphertext cannot select its original decryptor.
func (e *AESEncryptor) EncryptForDomain(domain, plaintext string) (string, error) {
	domain, err := validateSecretDomain(domain)
	if err != nil {
		return "", err
	}
	domainKey, err := e.deriveDomainKey(domain)
	if err != nil {
		return "", err
	}
	encoded, err := encryptAESGCM(domainKey, secretDomainAAD(domain), plaintext)
	if err != nil {
		return "", err
	}
	return secretDomainCiphertextPrefix + encoded, nil
}

// DecryptForDomain rejects legacy unbound ciphertext. Legacy data must be
// migrated before workers and HTTP listeners start.
func (e *AESEncryptor) DecryptForDomain(domain, ciphertext string) (string, error) {
	domain, err := validateSecretDomain(domain)
	if err != nil {
		return "", err
	}
	if !strings.HasPrefix(ciphertext, secretDomainCiphertextPrefix) {
		if strings.HasPrefix(ciphertext, legacySecretDomainCiphertextPrefixV2) {
			return "", fmt.Errorf("v2 shared-root ciphertext requires security migration")
		}
		return "", fmt.Errorf("legacy unbound ciphertext requires security migration")
	}
	encoded := strings.TrimPrefix(ciphertext, secretDomainCiphertextPrefix)
	domainKey, err := e.deriveDomainKey(domain)
	if err != nil {
		return "", err
	}
	return decryptAESGCM(domainKey, secretDomainAAD(domain), encoded)
}

func (e *AESEncryptor) deriveDomainKey(domain string) ([]byte, error) {
	root, ok := e.domainKeys[domain]
	if !ok || len(root) == 0 {
		return nil, fmt.Errorf("unsupported or unconfigured secret domain %q", domain)
	}
	mac := hmac.New(sha256.New, root)
	_, _ = mac.Write([]byte(secretDomainKDFContext))
	_, _ = mac.Write([]byte(domain))
	return mac.Sum(nil), nil
}

func (e *AESEncryptor) decryptV2ForMigration(domain, ciphertext string) (string, error) {
	domain, err := validateSecretDomain(domain)
	if err != nil {
		return "", err
	}
	if !strings.HasPrefix(ciphertext, legacySecretDomainCiphertextPrefixV2) {
		return "", fmt.Errorf("not a v2 shared-root ciphertext")
	}
	if len(e.legacyKey) == 0 {
		return "", fmt.Errorf("legacy TOTP_ENCRYPTION_KEY is unavailable")
	}
	encoded := strings.TrimPrefix(ciphertext, legacySecretDomainCiphertextPrefixV2)
	return decryptAESGCM(deriveLegacyDomainKeyV2(e.legacyKey, domain), legacySecretDomainAADV2(domain), encoded)
}

func deriveLegacyDomainKeyV2(root []byte, domain string) []byte {
	mac := hmac.New(sha256.New, root)
	_, _ = mac.Write([]byte(legacySecretDomainKDFContextV2))
	_, _ = mac.Write([]byte(domain))
	return mac.Sum(nil)
}

func legacySecretDomainAADV2(domain string) []byte {
	return []byte(legacySecretDomainKDFContextV2 + domain)
}

func configuredDomainKeys(cfg *config.Config) (map[string][]byte, error) {
	configured := []struct {
		domain string
		name   string
		value  string
	}{
		{service.SecretDomainTOTP, "SECRET_ENCRYPTION_TOTP_SECRET_KEY", cfg.SecretEncryption.TOTPSecretKey},
		{service.SecretDomainTOTPCache, "SECRET_ENCRYPTION_TOTP_CACHE_KEY", cfg.SecretEncryption.TOTPCacheKey},
		{service.SecretDomainAccountCredential, "SECRET_ENCRYPTION_ACCOUNT_CREDENTIAL_KEY", cfg.SecretEncryption.AccountCredentialKey},
		{service.SecretDomainBackupS3, "SECRET_ENCRYPTION_BACKUP_S3_KEY", cfg.SecretEncryption.BackupS3Key},
		{service.SecretDomainContentModeration, "SECRET_ENCRYPTION_CONTENT_MODERATION_KEY", cfg.SecretEncryption.ContentModerationKey},
		{service.SecretDomainChannelMonitor, "SECRET_ENCRYPTION_CHANNEL_MONITOR_KEY", cfg.SecretEncryption.ChannelMonitorKey},
		{service.SecretDomainPaymentProvider, "SECRET_ENCRYPTION_PAYMENT_PROVIDER_KEY", cfg.SecretEncryption.PaymentProviderKey},
		{service.SecretDomainProxyCredential, "SECRET_ENCRYPTION_PROXY_CREDENTIAL_KEY", cfg.SecretEncryption.ProxyCredentialKey},
		{service.SecretDomainSchedulerCache, "SECRET_ENCRYPTION_SCHEDULER_CACHE_KEY", cfg.SecretEncryption.SchedulerCacheKey},
		{service.SecretDomainOAuthTokenCache, "SECRET_ENCRYPTION_OAUTH_TOKEN_CACHE_KEY", cfg.SecretEncryption.OAuthTokenCacheKey},
		{service.SecretDomainJWTHMAC, "SECRET_ENCRYPTION_JWT_HMAC_KEY", cfg.SecretEncryption.JWTHMACKey},
		{service.SecretDomainSettingSecret, "SECRET_ENCRYPTION_SETTING_SECRET_KEY", cfg.SecretEncryption.SettingSecretKey},
	}
	keys := make(map[string][]byte, len(configured))
	for _, item := range configured {
		key, err := decodeSecretRoot(item.name, item.value)
		if err != nil {
			return nil, err
		}
		keys[item.domain] = key
	}
	return keys, nil
}

func configuredLegacySecretKey(cfg *config.Config) ([]byte, error) {
	if !cfg.Totp.EncryptionKeyConfigured {
		return nil, nil
	}
	return decodeSecretRoot("TOTP_ENCRYPTION_KEY", cfg.Totp.EncryptionKey)
}

func decodeSecretRoot(name, value string) ([]byte, error) {
	if strings.TrimSpace(value) == "" {
		return nil, fmt.Errorf("%s must be explicitly configured and stable", name)
	}
	key, err := hex.DecodeString(value)
	if err != nil {
		return nil, fmt.Errorf("invalid %s: %w", name, err)
	}
	if len(key) != 32 {
		return nil, fmt.Errorf("%s must be 32 bytes (64 hex chars), got %d bytes", name, len(key))
	}
	return key, nil
}

func rejectReusedSecretRoots(cfg *config.Config, legacyKey []byte, domainKeys map[string][]byte) error {
	jwtSecret := strings.TrimSpace(cfg.JWT.Secret)
	if jwtSecret != "" && subtle.ConstantTimeCompare(
		[]byte(jwtSecret),
		[]byte(strings.TrimSpace(cfg.SecretEncryption.JWTHMACKey)),
	) == 1 {
		return fmt.Errorf("SECRET_ENCRYPTION_JWT_HMAC_KEY must be distinct from JWT_SECRET")
	}
	seen := make(map[string]string, len(domainKeys)+2)
	if len(legacyKey) > 0 {
		seen[string(legacyKey)] = "TOTP_ENCRYPTION_KEY"
	}
	if cfg.APIKey.EncryptionKeyConfigured {
		apiKeyRoot, err := decodeSecretRoot("API_KEY_ENCRYPTION_KEY", cfg.APIKey.EncryptionKey)
		if err != nil {
			return err
		}
		seen[string(apiKeyRoot)] = "API_KEY_ENCRYPTION_KEY"
	}
	for domain, key := range domainKeys {
		if previous, ok := seen[string(key)]; ok {
			return fmt.Errorf("secret root for %s must be distinct from %s", domain, previous)
		}
		seen[string(key)] = domain
	}
	return nil
}

func isDomainCiphertext(ciphertext string) bool {
	return strings.HasPrefix(ciphertext, secretDomainCiphertextPrefix) ||
		strings.HasPrefix(ciphertext, legacySecretDomainCiphertextPrefixV2)
}

func validateSecretDomain(domain string) (string, error) {
	trimmed := strings.TrimSpace(domain)
	if trimmed == "" || trimmed != domain {
		return "", fmt.Errorf("secret domain must be non-empty and canonical")
	}
	return domain, nil
}

func secretDomainAAD(domain string) []byte {
	return []byte(secretDomainKDFContext + domain)
}

func encryptAESGCM(key, aad []byte, plaintext string) (string, error) {
	gcm, err := newGCM(key)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("generate nonce: %w", err)
	}
	sealed := gcm.Seal(nonce, nonce, []byte(plaintext), aad)
	return base64.StdEncoding.EncodeToString(sealed), nil
}

func decryptAESGCM(key, aad []byte, ciphertext string) (string, error) {
	data, err := base64.StdEncoding.DecodeString(ciphertext)
	if err != nil {
		return "", fmt.Errorf("decode base64: %w", err)
	}
	gcm, err := newGCM(key)
	if err != nil {
		return "", err
	}

	nonceSize := gcm.NonceSize()
	if len(data) < nonceSize {
		return "", fmt.Errorf("ciphertext too short")
	}
	nonce, ciphertextData := data[:nonceSize], data[nonceSize:]
	plaintext, err := gcm.Open(nil, nonce, ciphertextData, aad)
	if err != nil {
		return "", fmt.Errorf("decrypt: %w", err)
	}
	return string(plaintext), nil
}

func newGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("create cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("create gcm: %w", err)
	}
	return gcm, nil
}
