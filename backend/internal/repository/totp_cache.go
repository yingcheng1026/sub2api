package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

const (
	totpSetupKeyPrefix    = "totp:setup:"
	totpLoginKeyPrefix    = "totp:login:"
	totpAttemptsKeyPrefix = "totp:attempts:"
	totpAttemptsTTL       = 15 * time.Minute

	// V1 is retained only so stale rolling-upgrade sessions can be rejected
	// explicitly. New sessions are domain-bound V2 and old sessions must retry.
	totpSetupEncryptedV1Prefix = "totp-setup-encrypted:v1:"
	totpLoginEncryptedV1Prefix = "totp-login-encrypted:v1:"
	totpSetupEncryptedV2Prefix = "totp-setup-encrypted:v2:"
	totpLoginEncryptedV2Prefix = "totp-login-encrypted:v2:"
)

// TotpCache implements service.TotpCache using Redis
type TotpCache struct {
	rdb       *redis.Client
	encryptor service.SecretEncryptor
}

// NewTotpCache creates a new TOTP cache
func NewTotpCache(rdb *redis.Client, encryptor service.SecretEncryptor) service.TotpCache {
	return &TotpCache{rdb: rdb, encryptor: encryptor}
}

// GetSetupSession retrieves a TOTP setup session
func (c *TotpCache) GetSetupSession(ctx context.Context, userID int64) (*service.TotpSetupSession, error) {
	key := fmt.Sprintf("%s%d", totpSetupKeyPrefix, userID)
	data, err := c.rdb.Get(ctx, key).Bytes()
	if err != nil {
		if err == redis.Nil {
			return nil, nil
		}
		return nil, fmt.Errorf("get setup session: %w", err)
	}

	session, err := decodeTotpSetupSession(data, c.encryptor)
	if err != nil {
		return nil, err
	}

	return session, nil
}

// SetSetupSession stores a TOTP setup session
func (c *TotpCache) SetSetupSession(ctx context.Context, userID int64, session *service.TotpSetupSession, ttl time.Duration) error {
	key := fmt.Sprintf("%s%d", totpSetupKeyPrefix, userID)
	data, err := encodeTotpSetupSession(session, c.encryptor)
	if err != nil {
		return err
	}

	if err := c.rdb.Set(ctx, key, data, ttl).Err(); err != nil {
		return fmt.Errorf("set setup session: %w", err)
	}

	return nil
}

func encodeTotpSetupSession(session *service.TotpSetupSession, encryptor service.SecretEncryptor) ([]byte, error) {
	if session == nil {
		return nil, fmt.Errorf("encode setup session: session is nil")
	}
	if encryptor == nil {
		return nil, fmt.Errorf("encode setup session: encryptor is nil")
	}

	plaintext, err := json.Marshal(session)
	if err != nil {
		return nil, fmt.Errorf("marshal setup session: %w", err)
	}

	ciphertext, err := service.EncryptForSecretDomain(encryptor, service.SecretDomainTOTPCache, string(plaintext))
	if err != nil {
		return nil, fmt.Errorf("encrypt setup session: %w", err)
	}
	if ciphertext == "" {
		return nil, fmt.Errorf("encrypt setup session: empty ciphertext")
	}

	return []byte(totpSetupEncryptedV2Prefix + ciphertext), nil
}

func decodeTotpSetupSession(data []byte, encryptor service.SecretEncryptor) (*service.TotpSetupSession, error) {
	if !strings.HasPrefix(string(data), totpSetupEncryptedV2Prefix) {
		return nil, fmt.Errorf("decrypt setup session: legacy or unmarked session is not accepted")
	}
	ciphertext := string(data[len(totpSetupEncryptedV2Prefix):])
	if ciphertext == "" {
		return nil, fmt.Errorf("decrypt setup session: empty ciphertext")
	}
	decrypted, err := service.DecryptForSecretDomain(encryptor, service.SecretDomainTOTPCache, ciphertext)
	if err != nil {
		return nil, fmt.Errorf("decrypt setup session: %w", err)
	}

	var session service.TotpSetupSession
	if err := json.Unmarshal([]byte(decrypted), &session); err != nil {
		return nil, fmt.Errorf("unmarshal setup session: %w", err)
	}

	return &session, nil
}

// DeleteSetupSession deletes a TOTP setup session
func (c *TotpCache) DeleteSetupSession(ctx context.Context, userID int64) error {
	key := fmt.Sprintf("%s%d", totpSetupKeyPrefix, userID)
	return c.rdb.Del(ctx, key).Err()
}

// GetLoginSession retrieves a TOTP login session
func (c *TotpCache) GetLoginSession(ctx context.Context, tempToken string) (*service.TotpLoginSession, error) {
	key := totpLoginKeyPrefix + tempToken
	data, err := c.rdb.Get(ctx, key).Bytes()
	if err != nil {
		if err == redis.Nil {
			return nil, nil
		}
		return nil, fmt.Errorf("get login session: %w", err)
	}

	return decodeTotpLoginSession(data, c.encryptor)
}

// SetLoginSession stores a TOTP login session
func (c *TotpCache) SetLoginSession(ctx context.Context, tempToken string, session *service.TotpLoginSession, ttl time.Duration) error {
	key := totpLoginKeyPrefix + tempToken
	data, err := encodeTotpLoginSession(session, c.encryptor)
	if err != nil {
		return err
	}

	if err := c.rdb.Set(ctx, key, data, ttl).Err(); err != nil {
		return fmt.Errorf("set login session: %w", err)
	}

	return nil
}

func encodeTotpLoginSession(session *service.TotpLoginSession, encryptor service.SecretEncryptor) ([]byte, error) {
	if session == nil {
		return nil, fmt.Errorf("encode login session: session is nil")
	}
	if encryptor == nil {
		return nil, fmt.Errorf("encode login session: encryptor is nil")
	}
	plaintext, err := json.Marshal(session)
	if err != nil {
		return nil, fmt.Errorf("marshal login session: %w", err)
	}
	ciphertext, err := service.EncryptForSecretDomain(encryptor, service.SecretDomainTOTPCache, string(plaintext))
	if err != nil {
		return nil, fmt.Errorf("encrypt login session: %w", err)
	}
	if ciphertext == "" {
		return nil, fmt.Errorf("encrypt login session: empty ciphertext")
	}
	return []byte(totpLoginEncryptedV2Prefix + ciphertext), nil
}

func decodeTotpLoginSession(data []byte, encryptor service.SecretEncryptor) (*service.TotpLoginSession, error) {
	if !strings.HasPrefix(string(data), totpLoginEncryptedV2Prefix) {
		return nil, fmt.Errorf("decrypt login session: legacy or unmarked session is not accepted")
	}
	ciphertext := string(data[len(totpLoginEncryptedV2Prefix):])
	if ciphertext == "" {
		return nil, fmt.Errorf("decrypt login session: empty ciphertext")
	}
	decrypted, err := service.DecryptForSecretDomain(encryptor, service.SecretDomainTOTPCache, ciphertext)
	if err != nil {
		return nil, fmt.Errorf("decrypt login session: %w", err)
	}
	var session service.TotpLoginSession
	if err := json.Unmarshal([]byte(decrypted), &session); err != nil {
		return nil, fmt.Errorf("unmarshal login session: %w", err)
	}
	return &session, nil
}

// ConsumeLoginSession atomically gets and deletes a verified 2FA session so
// only one concurrent request can continue to token issuance/binding.
func (c *TotpCache) ConsumeLoginSession(ctx context.Context, tempToken string) (*service.TotpLoginSession, error) {
	key := totpLoginKeyPrefix + tempToken
	const consumeScript = `
		local value = redis.call('GET', KEYS[1])
		if not value then
			return nil
		end
		redis.call('DEL', KEYS[1])
		return value`
	result, err := c.rdb.Eval(ctx, consumeScript, []string{key}).Result()
	if err != nil {
		if err == redis.Nil {
			return nil, nil
		}
		return nil, fmt.Errorf("consume login session: %w", err)
	}
	encoded, ok := result.(string)
	if !ok || encoded == "" {
		return nil, fmt.Errorf("consume login session: invalid stored session")
	}
	data := []byte(encoded)
	return decodeTotpLoginSession(data, c.encryptor)
}

// DeleteLoginSession deletes a TOTP login session
func (c *TotpCache) DeleteLoginSession(ctx context.Context, tempToken string) error {
	key := totpLoginKeyPrefix + tempToken
	return c.rdb.Del(ctx, key).Err()
}

// IncrementVerifyAttempts increments the verify attempt counter
func (c *TotpCache) IncrementVerifyAttempts(ctx context.Context, userID int64) (int, error) {
	key := fmt.Sprintf("%s%d", totpAttemptsKeyPrefix, userID)
	count, err := c.incrementWithFixedTTL(ctx, key, totpAttemptsTTL)
	if err != nil {
		return 0, fmt.Errorf("increment verify attempts: %w", err)
	}
	return count, nil
}

// GetVerifyAttempts gets the current verify attempt count
func (c *TotpCache) GetVerifyAttempts(ctx context.Context, userID int64) (int, error) {
	key := fmt.Sprintf("%s%d", totpAttemptsKeyPrefix, userID)
	count, err := c.rdb.Get(ctx, key).Int()
	if err != nil {
		if err == redis.Nil {
			return 0, nil
		}
		return 0, fmt.Errorf("get verify attempts: %w", err)
	}
	return count, nil
}

// ClearVerifyAttempts clears the verify attempt counter
func (c *TotpCache) ClearVerifyAttempts(ctx context.Context, userID int64) error {
	key := fmt.Sprintf("%s%d", totpAttemptsKeyPrefix, userID)
	return c.rdb.Del(ctx, key).Err()
}

func (c *TotpCache) IncrementScopedVerifyAttempts(ctx context.Context, purpose string, userID int64) (int, error) {
	key := fmt.Sprintf("%s%s:%d", totpAttemptsKeyPrefix, purpose, userID)
	count, err := c.incrementWithFixedTTL(ctx, key, totpAttemptsTTL)
	if err != nil {
		return 0, fmt.Errorf("increment scoped verify attempts: %w", err)
	}
	return count, nil
}

func (c *TotpCache) ClearScopedVerifyAttempts(ctx context.Context, purpose string, userID int64) error {
	key := fmt.Sprintf("%s%s:%d", totpAttemptsKeyPrefix, purpose, userID)
	return c.rdb.Del(ctx, key).Err()
}

func (c *TotpCache) ConsumeScopedVerification(ctx context.Context, _ string, userID int64, fingerprint string, ttl time.Duration) (bool, error) {
	// A TOTP code is a single proof, not one proof per endpoint. Deliberately
	// omit purpose so the same live code cannot be replayed across reveal/create.
	key := fmt.Sprintf("totp:used:api_key_step_up:%d:%s", userID, fingerprint)
	consumed, err := c.rdb.SetNX(ctx, key, "1", ttl).Result()
	if err != nil {
		return false, fmt.Errorf("consume scoped totp verification: %w", err)
	}
	return consumed, nil
}

func (c *TotpCache) incrementWithFixedTTL(ctx context.Context, key string, ttl time.Duration) (int, error) {
	const script = `
		local count = redis.call('INCR', KEYS[1])
		if count == 1 then
			redis.call('EXPIRE', KEYS[1], ARGV[1])
		end
		return count`
	count, err := c.rdb.Eval(ctx, script, []string{key}, int64(ttl/time.Second)).Int()
	if err != nil {
		return 0, err
	}
	return count, nil
}
