package repository

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/securitysecret"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

const (
	securitySecretKeyJWT        = "jwt_secret"
	securitySecretReadRetryMax  = 5
	securitySecretReadRetryWait = 10 * time.Millisecond
)

func ensureBootstrapSecrets(ctx context.Context, client *ent.Client, cfg *config.Config, encryptor service.SecretEncryptor) error {
	if client == nil {
		return fmt.Errorf("nil ent client")
	}
	if cfg == nil {
		return fmt.Errorf("nil config")
	}
	if encryptor == nil {
		return fmt.Errorf("jwt secret encryptor is required")
	}

	cfg.JWT.Secret = strings.TrimSpace(cfg.JWT.Secret)
	configuredSecret := cfg.JWT.Secret
	if configuredSecret != "" {
		if err := validateBootstrapSecret(securitySecretKeyJWT, configuredSecret); err != nil {
			return err
		}
	}

	stored, err := client.SecuritySecret.Query().Where(securitysecret.KeyEQ(securitySecretKeyJWT)).Only(ctx)
	if ent.IsNotFound(err) {
		if configuredSecret == "" {
			return fmt.Errorf("JWT_SECRET is required when no persisted jwt secret exists")
		}
		ciphertext, encryptErr := service.EncryptForSecretDomain(encryptor, service.SecretDomainJWTHMAC, configuredSecret)
		if encryptErr != nil {
			return fmt.Errorf("encrypt jwt secret: %w", encryptErr)
		}
		if createErr := client.SecuritySecret.Create().
			SetKey(securitySecretKeyJWT).
			SetValue(ciphertext).
			OnConflictColumns(securitysecret.FieldKey).
			DoNothing().
			Exec(ctx); createErr != nil && !isSQLNoRowsError(createErr) {
			return fmt.Errorf("persist encrypted jwt secret: %w", createErr)
		}
		stored, err = querySecuritySecretWithRetry(ctx, client, securitySecretKeyJWT)
		if err != nil {
			return fmt.Errorf("read persisted jwt secret: %w", err)
		}
	} else if err != nil {
		return fmt.Errorf("read jwt secret: %w", err)
	}

	plaintext, err := openOrMigrateBootstrapSecret(ctx, client, encryptor, stored)
	if err != nil {
		return fmt.Errorf("load encrypted jwt secret: %w", err)
	}
	if subtle.ConstantTimeCompare(
		[]byte(plaintext),
		[]byte(strings.TrimSpace(cfg.SecretEncryption.JWTHMACKey)),
	) == 1 {
		return fmt.Errorf("persisted JWT secret must be distinct from SECRET_ENCRYPTION_JWT_HMAC_KEY")
	}
	if configuredSecret != "" && configuredSecret != plaintext {
		return fmt.Errorf("configured JWT secret mismatches persisted secret")
	}
	cfg.JWT.Secret = plaintext
	return nil
}

func openOrMigrateBootstrapSecret(ctx context.Context, client *ent.Client, encryptor service.SecretEncryptor, stored *ent.SecuritySecret) (string, error) {
	if stored == nil {
		return "", fmt.Errorf("nil persisted secret")
	}
	rawValue := strings.TrimSpace(stored.Value)
	if strings.HasPrefix(rawValue, secretDomainCiphertextPrefix) ||
		strings.HasPrefix(rawValue, legacySecretDomainCiphertextPrefixV2) {
		plaintext, err := service.DecryptForSecretDomain(encryptor, service.SecretDomainJWTHMAC, rawValue)
		if err != nil {
			return "", fmt.Errorf("decrypt %q: %w", stored.Key, err)
		}
		if err := validateBootstrapSecret(stored.Key, plaintext); err != nil {
			return "", err
		}
		return plaintext, nil
	}

	if err := validateBootstrapSecret(stored.Key, rawValue); err != nil {
		return "", err
	}
	ciphertext, err := service.EncryptForSecretDomain(encryptor, service.SecretDomainJWTHMAC, rawValue)
	if err != nil {
		return "", fmt.Errorf("encrypt legacy plaintext %q: %w", stored.Key, err)
	}
	affected, err := client.SecuritySecret.Update().
		Where(securitysecret.IDEQ(stored.ID), securitysecret.ValueEQ(stored.Value)).
		SetValue(ciphertext).
		Save(ctx)
	if err != nil {
		return "", fmt.Errorf("migrate plaintext %q: %w", stored.Key, err)
	}
	if affected == 0 {
		latest, err := querySecuritySecretWithRetry(ctx, client, stored.Key)
		if err != nil {
			return "", err
		}
		return openOrMigrateBootstrapSecret(ctx, client, encryptor, latest)
	}
	return rawValue, nil
}

func validateBootstrapSecret(key, value string) error {
	value = strings.TrimSpace(value)
	if len([]byte(value)) < 32 {
		return fmt.Errorf("secret %q must be at least 32 bytes", key)
	}
	return nil
}

func querySecuritySecretWithRetry(ctx context.Context, client *ent.Client, key string) (*ent.SecuritySecret, error) {
	var lastErr error
	for attempt := 0; attempt <= securitySecretReadRetryMax; attempt++ {
		stored, err := client.SecuritySecret.Query().Where(securitysecret.KeyEQ(key)).Only(ctx)
		if err == nil {
			return stored, nil
		}
		if !isSecretNotFoundError(err) {
			return nil, err
		}
		lastErr = err
		if attempt == securitySecretReadRetryMax {
			break
		}

		timer := time.NewTimer(securitySecretReadRetryWait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
	return nil, lastErr
}

func isSecretNotFoundError(err error) bool {
	if err == nil {
		return false
	}
	return ent.IsNotFound(err) || isSQLNoRowsError(err)
}

func isSQLNoRowsError(err error) bool {
	if err == nil {
		return false
	}
	return errors.Is(err, sql.ErrNoRows) || strings.Contains(err.Error(), "no rows in result set")
}
