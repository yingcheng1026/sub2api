package repository

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

const (
	apiKeyEncryptionDerivationLabel = "sub2api/api-key/aes-gcm/v1"
	apiKeyLookupDerivationLabel     = "sub2api/api-key/lookup-hmac/v1"
)

type apiKeyProtector struct {
	encryptionKey []byte
	lookupKey     []byte
}

var _ service.APIKeyProtector = (*apiKeyProtector)(nil)

func NewAPIKeyProtector(cfg *config.Config) (service.APIKeyProtector, error) {
	if cfg == nil || !cfg.APIKey.EncryptionKeyConfigured {
		return nil, fmt.Errorf("API_KEY_ENCRYPTION_KEY must be explicitly configured and stable before API keys can be stored")
	}
	masterKey, err := hex.DecodeString(cfg.APIKey.EncryptionKey)
	if err != nil {
		return nil, fmt.Errorf("invalid API key encryption key: %w", err)
	}
	if len(masterKey) != 32 {
		return nil, fmt.Errorf("API key encryption key must be 32 bytes (64 hex chars), got %d bytes", len(masterKey))
	}
	return &apiKeyProtector{
		encryptionKey: deriveAPIKeySubkey(masterKey, apiKeyEncryptionDerivationLabel),
		lookupKey:     deriveAPIKeySubkey(masterKey, apiKeyLookupDerivationLabel),
	}, nil
}

func deriveAPIKeySubkey(masterKey []byte, label string) []byte {
	mac := hmac.New(sha256.New, masterKey)
	_, _ = mac.Write([]byte(label))
	return mac.Sum(nil)
}

func (p *apiKeyProtector) EncryptAPIKey(plaintext, associatedData string) (string, error) {
	gcm, err := p.gcm()
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("generate API key nonce: %w", err)
	}
	sealed := gcm.Seal(nonce, nonce, []byte(plaintext), []byte(associatedData))
	return base64.RawURLEncoding.EncodeToString(sealed), nil
}

func (p *apiKeyProtector) DecryptAPIKey(ciphertext, associatedData string) (string, error) {
	data, err := base64.RawURLEncoding.DecodeString(ciphertext)
	if err != nil {
		return "", fmt.Errorf("decode API key ciphertext: %w", err)
	}
	gcm, err := p.gcm()
	if err != nil {
		return "", err
	}
	if len(data) < gcm.NonceSize() {
		return "", fmt.Errorf("API key ciphertext is too short")
	}
	nonce, sealed := data[:gcm.NonceSize()], data[gcm.NonceSize():]
	plaintext, err := gcm.Open(nil, nonce, sealed, []byte(associatedData))
	if err != nil {
		return "", fmt.Errorf("authenticate API key ciphertext: %w", err)
	}
	return string(plaintext), nil
}

func (p *apiKeyProtector) LookupLocator(plaintext string) string {
	mac := hmac.New(sha256.New, p.lookupKey)
	_, _ = mac.Write([]byte(plaintext))
	return hex.EncodeToString(mac.Sum(nil))
}

func (p *apiKeyProtector) gcm() (cipher.AEAD, error) {
	if p == nil || len(p.encryptionKey) != 32 {
		return nil, fmt.Errorf("API key encryption key is unavailable")
	}
	block, err := aes.NewCipher(p.encryptionKey)
	if err != nil {
		return nil, fmt.Errorf("create API key cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("create API key GCM: %w", err)
	}
	return gcm, nil
}
