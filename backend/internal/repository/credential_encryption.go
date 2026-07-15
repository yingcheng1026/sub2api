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
	encryptedCredentialAlgV1     = "aes-256-gcm-json-v1"
	encryptedCredentialAlgV2     = "aes-256-gcm-json-domain-v2"
	encryptedCredentialAlgV3     = "aes-256-gcm-json-domain-v3"
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
	alg, ciphertext, envelope, err := parseEncryptedCredentialEnvelope(value)
	if err != nil {
		return nil, err
	}
	if envelope {
		return preserveDomainCredentialEnvelope(value, alg, ciphertext, encryptor)
	}
	if isSensitiveCredentialKey(key) {
		return encryptCredentialJSON(value, encryptor)
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

func preserveDomainCredentialEnvelope(value any, alg, ciphertext string, encryptor service.SecretEncryptor) (any, error) {
	if alg != encryptedCredentialAlgV3 {
		return nil, fmt.Errorf("legacy credential envelope requires security migration")
	}
	plaintext, err := service.DecryptForSecretDomain(encryptor, service.SecretDomainAccountCredential, ciphertext)
	if err != nil {
		return nil, fmt.Errorf("validate domain-bound credential: %w", err)
	}
	var decoded any
	if err := json.Unmarshal([]byte(plaintext), &decoded); err != nil {
		return nil, fmt.Errorf("validate domain-bound credential payload: %w", err)
	}
	return copyJSONValue(value), nil
}

func encryptCredentialJSON(value any, encryptor service.SecretEncryptor) (any, error) {
	payload, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	ciphertext, err := service.EncryptForSecretDomain(encryptor, service.SecretDomainAccountCredential, string(payload))
	if err != nil {
		return nil, err
	}
	return encryptedCredentialEnvelope(encryptedCredentialAlgV3, ciphertext), nil
}

func decryptCredentialValue(value any, encryptor service.SecretEncryptor) (any, error) {
	alg, ciphertext, envelope, err := parseEncryptedCredentialEnvelope(value)
	if err != nil {
		return nil, err
	}
	if envelope {
		if alg != encryptedCredentialAlgV3 {
			return nil, fmt.Errorf("legacy credential envelope requires security migration")
		}
		plaintext, err := service.DecryptForSecretDomain(encryptor, service.SecretDomainAccountCredential, ciphertext)
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

func encryptedCredentialEnvelope(alg, ciphertext string) map[string]any {
	return map[string]any{
		encryptedCredentialMarkerKey: true,
		encryptedCredentialAlgKey:    alg,
		encryptedCredentialValueKey:  ciphertext,
	}
}

func parseEncryptedCredentialEnvelope(value any) (alg, ciphertext string, isEnvelope bool, err error) {
	envelopeMap, ok := value.(map[string]any)
	if !ok {
		return "", "", false, nil
	}
	marker, _ := envelopeMap[encryptedCredentialMarkerKey].(bool)
	if !marker {
		return "", "", false, nil
	}
	alg, _ = envelopeMap[encryptedCredentialAlgKey].(string)
	ciphertext, _ = envelopeMap[encryptedCredentialValueKey].(string)
	if (alg != encryptedCredentialAlgV1 && alg != encryptedCredentialAlgV2 && alg != encryptedCredentialAlgV3) || ciphertext == "" {
		return "", "", true, fmt.Errorf("invalid encrypted credential envelope")
	}
	return alg, ciphertext, true, nil
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
