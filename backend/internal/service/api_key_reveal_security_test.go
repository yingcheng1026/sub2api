package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

type revealAPIKeyRepoStub struct {
	APIKeyRepository
	key *APIKey
}

func (s revealAPIKeyRepoStub) GetByID(context.Context, int64) (*APIKey, error) {
	if s.key == nil {
		return nil, ErrAPIKeyNotFound
	}
	copy := *s.key
	return &copy, nil
}

type revealUserRepoStub struct {
	UserRepository
	user *User
}

type revealAPIKeyCacheStub struct {
	APIKeyCache
	count    int
	incrErr  error
	clearErr error
}

func (s *revealAPIKeyCacheStub) GetCreateAttemptCount(context.Context, int64) (int, error) {
	return s.count, nil
}

func (s *revealAPIKeyCacheStub) IncrementCreateAttemptCount(context.Context, int64) error {
	s.count++
	return nil
}

func (s *revealAPIKeyCacheStub) DeleteCreateAttemptCount(context.Context, int64) error {
	s.count = 0
	return nil
}

func (s *revealAPIKeyCacheStub) IncrementAPIKeyStepUpAttempt(context.Context, string, int64) (int, error) {
	if s.incrErr != nil {
		return 0, s.incrErr
	}
	s.count++
	return s.count, nil
}

func (s *revealAPIKeyCacheStub) DeleteAPIKeyStepUpAttempts(context.Context, string, int64) error {
	if s.clearErr != nil {
		return s.clearErr
	}
	s.count = 0
	return nil
}

func (s revealUserRepoStub) GetByID(context.Context, int64) (*User, error) {
	if s.user == nil {
		return nil, ErrUserNotFound
	}
	copy := *s.user
	return &copy, nil
}

func TestAPIKeyRevealRequiresFreshPasswordAndOwnership(t *testing.T) {
	user := &User{ID: 7}
	require.NoError(t, user.SetPassword("fresh-password"))
	svc := &APIKeyService{
		apiKeyRepo: revealAPIKeyRepoStub{key: &APIKey{ID: 11, UserID: 7, Key: "sk-reveal-secret"}},
		userRepo:   revealUserRepoStub{user: user},
		cache:      &revealAPIKeyCacheStub{},
	}

	require.ErrorIs(t, svc.VerifyRevealPassword(context.Background(), 7, "wrong"), ErrAPIKeyRevealVerification)
	require.NoError(t, svc.VerifyRevealPassword(context.Background(), 7, "fresh-password"))
	key, err := svc.Reveal(context.Background(), 11, 7)
	require.NoError(t, err)
	require.Equal(t, "sk-reveal-secret", key)

	_, err = svc.Reveal(context.Background(), 11, 8)
	require.ErrorIs(t, err, ErrInsufficientPerms)
}

func TestAPIKeyUpdateAcceptsDedicatedFreshPasswordPurpose(t *testing.T) {
	user := &User{ID: 7}
	require.NoError(t, user.SetPassword("fresh-password"))
	svc := &APIKeyService{
		userRepo: revealUserRepoStub{user: user},
		cache:    &revealAPIKeyCacheStub{},
	}

	require.NoError(t, svc.VerifyStepUpPassword(
		context.Background(), 7, "fresh-password", APIKeyStepUpPurposeUpdate,
	))
}

func TestAPIKeyRevealVerificationModePrefersTOTP(t *testing.T) {
	svc := &APIKeyService{userRepo: revealUserRepoStub{user: &User{ID: 7, TotpEnabled: true}}}
	mode, err := svc.RevealVerificationMode(context.Background(), 7)
	require.NoError(t, err)
	require.Equal(t, "totp", mode)
}

func TestAPIKeyRevealPasswordLimiterFailsClosed(t *testing.T) {
	user := &User{ID: 7}
	require.NoError(t, user.SetPassword("fresh-password"))

	for name, cache := range map[string]*revealAPIKeyCacheStub{
		"increment": {incrErr: context.Canceled},
		"clear":     {clearErr: context.Canceled},
	} {
		t.Run(name, func(t *testing.T) {
			svc := &APIKeyService{userRepo: revealUserRepoStub{user: user}, cache: cache}
			password := "wrong"
			if name == "clear" {
				password = "fresh-password"
			}
			require.ErrorIs(t, svc.VerifyRevealPassword(context.Background(), 7, password), ErrAPIKeyRevealUnavailable)
		})
	}
}

func TestAPIKeyCreateRejectsUserChosenSecret(t *testing.T) {
	custom := "user-chosen-secret-must-not-be-stored"
	svc := &APIKeyService{}
	_, err := svc.Create(context.Background(), 7, CreateAPIKeyRequest{CustomKey: &custom})
	require.ErrorIs(t, err, ErrAPIKeyCustomKeyDisabled)
}
