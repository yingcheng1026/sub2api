package payment

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

func makeKey(t *testing.T) []byte {
	t.Helper()
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatalf("generate random key: %v", err)
	}
	return key
}

func TestEncryptDecryptRoundTrip(t *testing.T) {
	t.Parallel()
	key := makeKey(t)

	plaintexts := []string{
		"hello world",
		"short",
		"a longer string with special chars: !@#$%^&*()",
		`{"key":"value","num":42}`,
		"你好世界 unicode test 🎉",
		strings.Repeat("x", 10000),
	}

	for _, pt := range plaintexts {
		encrypted, err := Encrypt(pt, key)
		if err != nil {
			t.Fatalf("Encrypt(%q) error: %v", pt[:min(len(pt), 30)], err)
		}
		decrypted, err := Decrypt(encrypted, key)
		if err != nil {
			t.Fatalf("Decrypt error for plaintext %q: %v", pt[:min(len(pt), 30)], err)
		}
		if decrypted != pt {
			t.Fatalf("round-trip failed: got %q, want %q", decrypted[:min(len(decrypted), 30)], pt[:min(len(pt), 30)])
		}
	}
}

func TestEncryptProducesDifferentCiphertexts(t *testing.T) {
	t.Parallel()
	key := makeKey(t)

	ct1, err := Encrypt("same plaintext", key)
	if err != nil {
		t.Fatalf("first Encrypt error: %v", err)
	}
	ct2, err := Encrypt("same plaintext", key)
	if err != nil {
		t.Fatalf("second Encrypt error: %v", err)
	}
	if ct1 == ct2 {
		t.Fatal("two encryptions of the same plaintext should produce different ciphertexts (random nonce)")
	}
}

func TestDecryptWithWrongKeyFails(t *testing.T) {
	t.Parallel()
	key1 := makeKey(t)
	key2 := makeKey(t)

	encrypted, err := Encrypt("secret data", key1)
	if err != nil {
		t.Fatalf("Encrypt error: %v", err)
	}

	_, err = Decrypt(encrypted, key2)
	if err == nil {
		t.Fatal("Decrypt with wrong key should fail, but got nil error")
	}
}

func TestEncryptRejectsInvalidKeyLength(t *testing.T) {
	t.Parallel()
	badKeys := [][]byte{
		nil,
		make([]byte, 0),
		make([]byte, 16),
		make([]byte, 31),
		make([]byte, 33),
		make([]byte, 64),
	}
	for _, key := range badKeys {
		_, err := Encrypt("test", key)
		if err == nil {
			t.Fatalf("Encrypt should reject key of length %d", len(key))
		}
	}
}

func TestDecryptRejectsInvalidKeyLength(t *testing.T) {
	t.Parallel()
	badKeys := [][]byte{
		nil,
		make([]byte, 16),
		make([]byte, 33),
	}
	for _, key := range badKeys {
		_, err := Decrypt("dummydata:dummydata:dummydata", key)
		if err == nil {
			t.Fatalf("Decrypt should reject key of length %d", len(key))
		}
	}
}

func TestEncryptEmptyPlaintext(t *testing.T) {
	t.Parallel()
	key := makeKey(t)

	encrypted, err := Encrypt("", key)
	if err != nil {
		t.Fatalf("Encrypt empty plaintext error: %v", err)
	}
	decrypted, err := Decrypt(encrypted, key)
	if err != nil {
		t.Fatalf("Decrypt empty plaintext error: %v", err)
	}
	if decrypted != "" {
		t.Fatalf("expected empty string, got %q", decrypted)
	}
}

func TestEncryptDecryptUnicodeJSON(t *testing.T) {
	t.Parallel()
	key := makeKey(t)

	jsonContent := `{"name":"测试用户","email":"test@example.com","balance":100.50}`
	encrypted, err := Encrypt(jsonContent, key)
	if err != nil {
		t.Fatalf("Encrypt JSON error: %v", err)
	}
	decrypted, err := Decrypt(encrypted, key)
	if err != nil {
		t.Fatalf("Decrypt JSON error: %v", err)
	}
	if decrypted != jsonContent {
		t.Fatalf("JSON round-trip failed: got %q, want %q", decrypted, jsonContent)
	}
}

func TestDecryptInvalidFormat(t *testing.T) {
	t.Parallel()
	key := makeKey(t)

	invalidInputs := []string{
		"",
		"nodelimiter",
		"only:two",
		"invalid:base64:!!!",
	}
	for _, input := range invalidInputs {
		_, err := Decrypt(input, key)
		if err == nil {
			t.Fatalf("Decrypt(%q) should fail but got nil error", input)
		}
	}
}

func TestDecryptRejectsInvalidNonceAndTagLengthsWithoutPanic(t *testing.T) {
	t.Parallel()
	key := makeKey(t)
	encoded := func(raw []byte) string { return base64.StdEncoding.EncodeToString(raw) }

	inputs := []string{
		encoded(make([]byte, 1)) + ":" + encoded(make([]byte, 16)) + ":" + encoded([]byte("ciphertext")),
		encoded(make([]byte, 12)) + ":" + encoded(make([]byte, 1)) + ":" + encoded([]byte("ciphertext")),
	}
	for _, input := range inputs {
		func() {
			defer func() {
				if recovered := recover(); recovered != nil {
					t.Fatalf("Decrypt panicked for malformed ciphertext: %v", recovered)
				}
			}()
			if _, err := Decrypt(input, key); err == nil {
				t.Fatal("Decrypt should reject malformed nonce or tag length")
			}
		}()
	}
}

func TestProviderConfigEncryptionUsesCiphertextAndReadsLegacyPlaintext(t *testing.T) {
	t.Parallel()
	key := makeKey(t)
	cfg := map[string]string{"secretKey": "sk-live-secret", "publishableKey": "pk-live"}

	stored, err := EncryptProviderConfig(cfg, key)
	if err != nil {
		t.Fatalf("EncryptProviderConfig returned error: %v", err)
	}
	if strings.Contains(stored, "sk-live-secret") || json.Valid([]byte(stored)) {
		t.Fatalf("provider config was not stored as opaque ciphertext: %q", stored)
	}

	decoded, legacyPlaintext, err := DecryptProviderConfig(stored, key)
	if err != nil {
		t.Fatalf("DecryptProviderConfig returned error: %v", err)
	}
	if legacyPlaintext || decoded["secretKey"] != cfg["secretKey"] {
		t.Fatalf("decoded=%v legacyPlaintext=%v", decoded, legacyPlaintext)
	}

	legacy := `{"secretKey":"sk-legacy","publishableKey":"pk-legacy"}`
	decoded, legacyPlaintext, err = DecryptProviderConfig(legacy, key)
	if err != nil {
		t.Fatalf("DecryptProviderConfig legacy plaintext returned error: %v", err)
	}
	if !legacyPlaintext || decoded["secretKey"] != "sk-legacy" {
		t.Fatalf("decoded=%v legacyPlaintext=%v", decoded, legacyPlaintext)
	}
}

func TestProviderConfigDecryptionFailsClosed(t *testing.T) {
	t.Parallel()
	key := makeKey(t)
	wrongKey := makeKey(t)
	stored, err := EncryptProviderConfig(map[string]string{"secret": "value"}, key)
	if err != nil {
		t.Fatalf("EncryptProviderConfig returned error: %v", err)
	}

	for name, tc := range map[string]struct {
		stored string
		key    []byte
	}{
		"missing key":      {stored: stored, key: nil},
		"wrong key":        {stored: stored, key: wrongKey},
		"malformed config": {stored: "not-json-or-ciphertext", key: key},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, _, err := DecryptProviderConfig(tc.stored, tc.key); err == nil {
				t.Fatal("expected fail-closed provider config error")
			}
		})
	}
}

func TestCiphertextFormat(t *testing.T) {
	t.Parallel()
	key := makeKey(t)

	encrypted, err := Encrypt("test", key)
	if err != nil {
		t.Fatalf("Encrypt error: %v", err)
	}

	parts := strings.SplitN(encrypted, ":", 3)
	if len(parts) != 3 {
		t.Fatalf("ciphertext should have format iv:authTag:ciphertext, got %d parts", len(parts))
	}
	for i, part := range parts {
		if part == "" {
			t.Fatalf("ciphertext part %d is empty", i)
		}
	}
}
