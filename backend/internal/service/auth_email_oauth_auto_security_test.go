//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCreateEmailOAuthUserRejectsConcurrentEmailCollision(t *testing.T) {
	repo := &userRepoStub{
		createErr: ErrEmailExists,
		user: &User{
			ID:     42,
			Email:  "victim@example.com",
			Status: StatusActive,
		},
	}
	svc := newAuthService(repo, map[string]string{
		SettingKeyRegistrationEnabled: "true",
	}, nil)

	user, err := svc.createEmailOAuthUser(
		context.Background(),
		"victim@example.com",
		"attacker-controlled-name",
		"google",
		"",
		"",
	)

	require.Nil(t, user)
	require.ErrorIs(t, err, ErrOAuthAccountLinkProofRequired)
}
