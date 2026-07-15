//go:build unit

package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/pquerna/otp/totp"
	"github.com/stretchr/testify/require"
)

type totpSecurityUserRepo struct {
	*userRepoStub
	updateSecretCalls int
	enableCalls       int
}

type totpVerifyUserRepo struct {
	UserRepository
	user *User
}

func (r *totpVerifyUserRepo) GetByID(context.Context, int64) (*User, error) {
	copy := *r.user
	return &copy, nil
}

func (r *totpSecurityUserRepo) UpdateTotpSecret(context.Context, int64, *string) error {
	r.updateSecretCalls++
	return nil
}

func (r *totpSecurityUserRepo) EnableTotp(context.Context, int64) error {
	r.enableCalls++
	return nil
}

type totpSecurityCache struct {
	session        *TotpSetupSession
	deleteCalls    int
	attempts       int
	scopedAttempts int
	incrementErr   error
	consumeResult  bool
	consumeErr     error
}

func (c *totpSecurityCache) GetSetupSession(context.Context, int64) (*TotpSetupSession, error) {
	return c.session, nil
}
func (*totpSecurityCache) SetSetupSession(context.Context, int64, *TotpSetupSession, time.Duration) error {
	return nil
}
func (c *totpSecurityCache) DeleteSetupSession(context.Context, int64) error {
	c.deleteCalls++
	return nil
}
func (*totpSecurityCache) GetLoginSession(context.Context, string) (*TotpLoginSession, error) {
	return nil, nil
}
func (*totpSecurityCache) SetLoginSession(context.Context, string, *TotpLoginSession, time.Duration) error {
	return nil
}
func (*totpSecurityCache) DeleteLoginSession(context.Context, string) error { return nil }
func (c *totpSecurityCache) IncrementVerifyAttempts(context.Context, int64) (int, error) {
	if c.incrementErr != nil {
		return 0, c.incrementErr
	}
	c.attempts++
	return c.attempts, nil
}
func (c *totpSecurityCache) GetVerifyAttempts(context.Context, int64) (int, error) {
	return c.attempts, nil
}
func (*totpSecurityCache) ClearVerifyAttempts(context.Context, int64) error { return nil }
func (c *totpSecurityCache) IncrementScopedVerifyAttempts(context.Context, string, int64) (int, error) {
	c.scopedAttempts++
	return c.scopedAttempts, c.incrementErr
}
func (c *totpSecurityCache) ClearScopedVerifyAttempts(context.Context, string, int64) error {
	c.scopedAttempts = 0
	return nil
}
func (c *totpSecurityCache) ConsumeScopedVerification(context.Context, string, int64, string, time.Duration) (bool, error) {
	return c.consumeResult, c.consumeErr
}

type totpSecuritySettingRepo struct{}

func (totpSecuritySettingRepo) Get(context.Context, string) (*Setting, error) {
	return nil, ErrSettingNotFound
}

func TestTotpVerifyFailsClosedWhenRateLimitStoreIsUnavailable(t *testing.T) {
	svc := &TotpService{cache: &totpSecurityCache{incrementErr: errors.New("redis unavailable")}}
	err := svc.VerifyCode(context.Background(), 7, "123456")
	require.ErrorIs(t, err, ErrTotpVerifyUnavailable)
}

func TestTotpVerifyClaimsAttemptBeforeCredentialLookup(t *testing.T) {
	cache := &totpSecurityCache{attempts: maxTotpAttempts}
	svc := &TotpService{cache: cache}
	err := svc.VerifyCode(context.Background(), 7, "123456")
	require.ErrorIs(t, err, ErrTotpTooManyAttempts)
	require.Equal(t, maxTotpAttempts+1, cache.attempts)
}

func TestTotpRevealLimiterIsIsolatedFromLoginAttempts(t *testing.T) {
	cache := &totpSecurityCache{attempts: maxTotpAttempts, scopedAttempts: maxTotpAttempts}
	svc := &TotpService{cache: cache}
	err := svc.VerifyCodeForPurpose(context.Background(), 7, "123456", TotpVerifyPurposeAPIKeyReveal)
	require.ErrorIs(t, err, ErrTotpTooManyAttempts)
	require.Equal(t, maxTotpAttempts, cache.attempts)
	require.Equal(t, maxTotpAttempts+1, cache.scopedAttempts)
}

func TestTotpScopedVerificationConsumesCodeOnce(t *testing.T) {
	const secret = "JBSWY3DPEHPK3PXP"
	encrypted := "ciphertext"
	code, err := totp.GenerateCode(secret, time.Now())
	require.NoError(t, err)
	userRepo := &totpVerifyUserRepo{user: &User{ID: 7, TotpEnabled: true, TotpSecretEncrypted: &encrypted}}

	cache := &totpSecurityCache{consumeResult: true}
	svc := &TotpService{userRepo: userRepo, cache: cache, encryptor: totpRoundTripEncryptor{plaintext: secret}}
	require.NoError(t, svc.VerifyCodeForPurpose(context.Background(), 7, code, TotpVerifyPurposeAPIKeyReveal))
	require.Zero(t, cache.attempts)
	require.Zero(t, cache.scopedAttempts)

	cache.consumeResult = false
	require.ErrorIs(t, svc.VerifyCodeForPurpose(context.Background(), 7, code, TotpVerifyPurposeAPIKeyReveal), ErrTotpInvalidCode)
}

func TestTotpUpdatePurposeUsesScopedVerification(t *testing.T) {
	const secret = "JBSWY3DPEHPK3PXP"
	encrypted := "ciphertext"
	code, err := totp.GenerateCode(secret, time.Now())
	require.NoError(t, err)
	userRepo := &totpVerifyUserRepo{user: &User{ID: 7, TotpEnabled: true, TotpSecretEncrypted: &encrypted}}
	cache := &totpSecurityCache{consumeResult: true}
	svc := &TotpService{userRepo: userRepo, cache: cache, encryptor: totpRoundTripEncryptor{plaintext: secret}}

	require.NoError(t, svc.VerifyCodeForPurpose(context.Background(), 7, code, TotpVerifyPurposeAPIKeyUpdate))
}

func TestTotpManagementPasswordUsesDedicatedAttemptLimiter(t *testing.T) {
	user := &User{ID: 7, Email: "oauth@example.com"}
	require.NoError(t, user.SetPassword("known-password"))
	settings := NewSettingService(totpSecuritySettingRepo{}, &config.Config{})

	tests := []struct {
		name string
		run  func(*TotpService) error
	}{
		{
			name: "setup",
			run: func(svc *TotpService) error {
				_, err := svc.InitiateSetup(context.Background(), user.ID, "", "wrong-password")
				return err
			},
		},
		{
			name: "disable",
			run: func(svc *TotpService) error {
				return svc.Disable(context.Background(), user.ID, "", "wrong-password")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			userCopy := *user
			userCopy.TotpEnabled = tt.name == "disable"
			cache := &totpSecurityCache{scopedAttempts: maxTotpAttempts}
			svc := NewTotpService(&totpVerifyUserRepo{user: &userCopy}, totpRoundTripEncryptor{}, cache, settings, nil, nil)

			err := tt.run(svc)
			require.ErrorIs(t, err, ErrTotpTooManyAttempts)
			require.Equal(t, maxTotpAttempts+1, cache.scopedAttempts)
		})
	}
}

func (totpSecuritySettingRepo) GetValue(_ context.Context, key string) (string, error) {
	if key == SettingKeyTotpEnabled {
		return "true", nil
	}
	return "", ErrSettingNotFound
}
func (totpSecuritySettingRepo) Set(context.Context, string, string) error { return nil }
func (totpSecuritySettingRepo) GetMultiple(context.Context, []string) (map[string]string, error) {
	return map[string]string{}, nil
}
func (totpSecuritySettingRepo) SetMultiple(context.Context, map[string]string) error { return nil }
func (totpSecuritySettingRepo) GetAll(context.Context) (map[string]string, error) {
	return map[string]string{}, nil
}
func (totpSecuritySettingRepo) Delete(context.Context, string) error { return nil }

type totpRoundTripEncryptor struct {
	ciphertext string
	plaintext  string
	decryptErr error
}

func (e totpRoundTripEncryptor) Encrypt(string) (string, error) { return e.ciphertext, nil }
func (e totpRoundTripEncryptor) Decrypt(string) (string, error) {
	return e.plaintext, e.decryptErr
}
func (e totpRoundTripEncryptor) EncryptForDomain(string, string) (string, error) {
	return e.ciphertext, nil
}
func (e totpRoundTripEncryptor) DecryptForDomain(string, string) (string, error) {
	return e.plaintext, e.decryptErr
}

func TestTotpCompleteSetupFailsClosedWhenCiphertextCannotRoundTrip(t *testing.T) {
	const secret = "JBSWY3DPEHPK3PXP"
	code, err := totp.GenerateCode(secret, time.Now())
	require.NoError(t, err)

	tests := []struct {
		name      string
		encryptor SecretEncryptor
	}{
		{
			name:      "decrypt error",
			encryptor: totpRoundTripEncryptor{ciphertext: "ciphertext", decryptErr: errors.New("cannot decrypt")},
		},
		{
			name:      "plaintext mismatch",
			encryptor: totpRoundTripEncryptor{ciphertext: "ciphertext", plaintext: "DIFFERENTSECRET"},
		},
		{
			name:      "empty ciphertext",
			encryptor: totpRoundTripEncryptor{plaintext: secret},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			userRepo := &totpSecurityUserRepo{userRepoStub: &userRepoStub{}}
			cache := &totpSecurityCache{session: &TotpSetupSession{
				Secret:     secret,
				SetupToken: "setup-token",
				CreatedAt:  time.Now(),
			}}
			settings := NewSettingService(totpSecuritySettingRepo{}, &config.Config{})
			svc := NewTotpService(userRepo, tt.encryptor, cache, settings, nil, nil)

			err := svc.CompleteSetup(context.Background(), 173, code, "setup-token")
			require.Error(t, err)
			require.Zero(t, userRepo.updateSecretCalls, "unreadable ciphertext must never be persisted")
			require.Zero(t, userRepo.enableCalls, "2FA must remain disabled when its secret cannot round-trip")
			require.Zero(t, cache.deleteCalls, "the user must be able to retry the still-valid setup session")
		})
	}
}
