package payment

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// AES256KeySize is the required key length (in bytes) for AES-256-GCM.
const AES256KeySize = 32

// EncryptProviderConfig serializes and encrypts a payment-provider config.
// Provider credentials must never be persisted as plaintext JSON.
func EncryptProviderConfig(config map[string]string, key []byte) (string, error) {
	if config == nil {
		config = map[string]string{}
	}
	plaintext, err := json.Marshal(config)
	if err != nil {
		return "", fmt.Errorf("marshal provider config: %w", err)
	}
	encrypted, err := Encrypt(string(plaintext), key)
	if err != nil {
		return "", fmt.Errorf("encrypt provider config: %w", err)
	}
	return encrypted, nil
}

// DecryptProviderConfig reads encrypted provider config and, during the
// controlled migration window, legacy plaintext JSON. The boolean reports
// whether the stored value was plaintext and therefore must be re-encrypted.
func DecryptProviderConfig(stored string, key []byte) (map[string]string, bool, error) {
	if stored == "" {
		return nil, false, nil
	}
	if len(key) != AES256KeySize {
		return nil, false, fmt.Errorf("provider config encryption key must be %d bytes, got %d", AES256KeySize, len(key))
	}

	var config map[string]string
	if err := json.Unmarshal([]byte(stored), &config); err == nil && config != nil {
		return config, true, nil
	}

	plaintext, err := Decrypt(stored, key)
	if err != nil {
		return nil, false, fmt.Errorf("decrypt provider config: %w", err)
	}
	if err := json.Unmarshal([]byte(plaintext), &config); err != nil {
		return nil, false, fmt.Errorf("decode provider config JSON: %w", err)
	}
	if config == nil {
		return nil, false, fmt.Errorf("decode provider config JSON: object is required")
	}
	return config, false, nil
}

// Encrypt encrypts plaintext using AES-256-GCM with the given 32-byte key.
// The output format is "iv:authTag:ciphertext" where each component is base64-encoded,
// matching the Node.js crypto.ts format for cross-compatibility.
func Encrypt(plaintext string, key []byte) (string, error) {
	if len(key) != AES256KeySize {
		return "", fmt.Errorf("encryption key must be %d bytes, got %d", AES256KeySize, len(key))
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return "", fmt.Errorf("create AES cipher: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("create GCM: %w", err)
	}

	nonce := make([]byte, gcm.NonceSize()) // 12 bytes for GCM
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("generate nonce: %w", err)
	}

	// Seal appends the ciphertext + auth tag
	sealed := gcm.Seal(nil, nonce, []byte(plaintext), nil)

	// Split sealed into ciphertext and auth tag (last 16 bytes)
	tagSize := gcm.Overhead()
	ciphertext := sealed[:len(sealed)-tagSize]
	authTag := sealed[len(sealed)-tagSize:]

	// Format: iv:authTag:ciphertext (all base64)
	return fmt.Sprintf("%s:%s:%s",
		base64.StdEncoding.EncodeToString(nonce),
		base64.StdEncoding.EncodeToString(authTag),
		base64.StdEncoding.EncodeToString(ciphertext),
	), nil
}

// Decrypt decrypts a ciphertext string produced by Encrypt.
// The input format is "iv:authTag:ciphertext" where each component is base64-encoded.
func Decrypt(ciphertext string, key []byte) (string, error) {
	if len(key) != AES256KeySize {
		return "", fmt.Errorf("encryption key must be %d bytes, got %d", AES256KeySize, len(key))
	}

	parts := strings.SplitN(ciphertext, ":", 3)
	if len(parts) != 3 {
		return "", fmt.Errorf("invalid ciphertext format: expected iv:authTag:ciphertext")
	}

	nonce, err := base64.StdEncoding.DecodeString(parts[0])
	if err != nil {
		return "", fmt.Errorf("decode IV: %w", err)
	}

	authTag, err := base64.StdEncoding.DecodeString(parts[1])
	if err != nil {
		return "", fmt.Errorf("decode auth tag: %w", err)
	}

	encrypted, err := base64.StdEncoding.DecodeString(parts[2])
	if err != nil {
		return "", fmt.Errorf("decode ciphertext: %w", err)
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return "", fmt.Errorf("create AES cipher: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("create GCM: %w", err)
	}
	if len(nonce) != gcm.NonceSize() {
		return "", fmt.Errorf("invalid IV length: expected %d bytes, got %d", gcm.NonceSize(), len(nonce))
	}
	if len(authTag) != gcm.Overhead() {
		return "", fmt.Errorf("invalid auth tag length: expected %d bytes, got %d", gcm.Overhead(), len(authTag))
	}

	// Reconstruct the sealed data: ciphertext + authTag
	sealed := append(encrypted, authTag...)

	plaintext, err := gcm.Open(nil, nonce, sealed, nil)
	if err != nil {
		return "", fmt.Errorf("decrypt: %w", err)
	}

	return string(plaintext), nil
}
