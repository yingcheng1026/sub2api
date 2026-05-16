package repository

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

const (
	encryptedCredentialMarkerKey = "__sub2api_encrypted"
	encryptedCredentialAlgKey    = "alg"
	encryptedCredentialValueKey  = "ciphertext"
	encryptedCredentialAlg       = "aes-256-gcm-json-v1"
)

var sensitiveCredentialKeys = map[string]struct{}{
	"access_token":         {},
	"api_key":              {},
	"apikey":               {},
	"auth_token":           {},
	"bearer_token":         {},
	"client_secret":        {},
	"cookie":               {},
	"cookies":              {},
	"credentials":          {},
	"id_token":             {},
	"password":             {},
	"private_key":          {},
	"refresh_token":        {},
	"secret":               {},
	"secret_access_key":    {},
	"service_account_json": {},
	"session_key":          {},
	"token":                {},
}

func encryptAccountCredentials(in map[string]any, encryptor service.SecretEncryptor) (map[string]any, error) {
	if in == nil {
		return nil, nil
	}
	out, err := encryptCredentialMap(in, encryptor)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func decryptAccountCredentials(in map[string]any, encryptor service.SecretEncryptor) (map[string]any, error) {
	if in == nil {
		return nil, nil
	}
	out, err := decryptCredentialMap(in, encryptor)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func encryptCredentialMap(in map[string]any, encryptor service.SecretEncryptor) (map[string]any, error) {
	out := make(map[string]any, len(in))
	for key, value := range in {
		encrypted, err := encryptCredentialValue(key, value, encryptor)
		if err != nil {
			return nil, fmt.Errorf("encrypt credential %q: %w", key, err)
		}
		out[key] = encrypted
	}
	return out, nil
}

func decryptCredentialMap(in map[string]any, encryptor service.SecretEncryptor) (map[string]any, error) {
	out := make(map[string]any, len(in))
	for key, value := range in {
		decrypted, err := decryptCredentialValue(value, encryptor)
		if err != nil {
			return nil, fmt.Errorf("decrypt credential %q: %w", key, err)
		}
		out[key] = decrypted
	}
	return out, nil
}

func encryptCredentialValue(key string, value any, encryptor service.SecretEncryptor) (any, error) {
	if value == nil {
		return nil, nil
	}
	if encryptor == nil {
		return copyJSONValue(value), nil
	}
	if isEncryptedCredentialEnvelope(value) {
		return copyJSONValue(value), nil
	}
	if isSensitiveCredentialKey(key) {
		payload, err := json.Marshal(value)
		if err != nil {
			return nil, err
		}
		ciphertext, err := encryptor.Encrypt(string(payload))
		if err != nil {
			return nil, err
		}
		return encryptedCredentialEnvelope(ciphertext), nil
	}

	switch typed := value.(type) {
	case map[string]any:
		return encryptCredentialMap(typed, encryptor)
	case []any:
		out := make([]any, len(typed))
		for i, item := range typed {
			encrypted, err := encryptCredentialValue("", item, encryptor)
			if err != nil {
				return nil, err
			}
			out[i] = encrypted
		}
		return out, nil
	default:
		return copyJSONValue(value), nil
	}
}

func decryptCredentialValue(value any, encryptor service.SecretEncryptor) (any, error) {
	if isEncryptedCredentialEnvelope(value) {
		if encryptor == nil {
			return copyJSONValue(value), nil
		}
		envelope, _ := value.(map[string]any)
		ciphertext, _ := envelope[encryptedCredentialValueKey].(string)
		plaintext, err := encryptor.Decrypt(ciphertext)
		if err != nil {
			return nil, err
		}
		var out any
		if err := json.Unmarshal([]byte(plaintext), &out); err != nil {
			return nil, err
		}
		return out, nil
	}

	switch typed := value.(type) {
	case map[string]any:
		return decryptCredentialMap(typed, encryptor)
	case []any:
		out := make([]any, len(typed))
		for i, item := range typed {
			decrypted, err := decryptCredentialValue(item, encryptor)
			if err != nil {
				return nil, err
			}
			out[i] = decrypted
		}
		return out, nil
	default:
		return copyJSONValue(value), nil
	}
}

func isSensitiveCredentialKey(key string) bool {
	normalized := strings.ToLower(strings.TrimSpace(key))
	_, ok := sensitiveCredentialKeys[normalized]
	return ok
}

func encryptedCredentialEnvelope(ciphertext string) map[string]any {
	return map[string]any{
		encryptedCredentialMarkerKey: true,
		encryptedCredentialAlgKey:    encryptedCredentialAlg,
		encryptedCredentialValueKey:  ciphertext,
	}
}

func isEncryptedCredentialEnvelope(value any) bool {
	envelope, ok := value.(map[string]any)
	if !ok {
		return false
	}
	marker, _ := envelope[encryptedCredentialMarkerKey].(bool)
	alg, _ := envelope[encryptedCredentialAlgKey].(string)
	ciphertext, _ := envelope[encryptedCredentialValueKey].(string)
	return marker && alg == encryptedCredentialAlg && ciphertext != ""
}

func copyJSONValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for k, v := range typed {
			out[k] = copyJSONValue(v)
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for i, v := range typed {
			out[i] = copyJSONValue(v)
		}
		return out
	default:
		return value
	}
}
