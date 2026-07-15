//go:build unit

package service

import (
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/require"
)

func TestAdminOpsWSTicketIsPurposeBoundAndDomainSeparated(t *testing.T) {
	cfg := &config.Config{JWT: config.JWTConfig{Secret: "unit-test-secret-that-is-long-enough", ExpireHour: 1}}
	authService := NewAuthService(nil, nil, nil, nil, cfg, nil, nil, nil, nil, nil, nil, nil)
	admin := &User{
		ID:                   42,
		Email:                "admin@example.com",
		Role:                 RoleAdmin,
		Status:               StatusActive,
		TokenVersion:         7,
		TokenVersionResolved: true,
	}

	ticket, expiresAt, err := authService.GenerateAdminOpsWSTicket(admin)
	require.NoError(t, err)
	require.NotEmpty(t, ticket)
	require.WithinDuration(t, time.Now().Add(AdminOpsWSTicketTTL), expiresAt, 2*time.Second)

	claims, err := authService.ValidateAdminOpsWSTicket(ticket)
	require.NoError(t, err)
	require.Equal(t, admin.ID, claims.UserID)
	require.Equal(t, admin.TokenVersion, claims.TokenVersion)
	require.Equal(t, adminOpsWSTicketPurpose, claims.Purpose)

	_, err = authService.ValidateToken(ticket)
	require.ErrorIs(t, err, ErrInvalidToken)

	accessToken, err := authService.GenerateToken(admin)
	require.NoError(t, err)
	_, err = authService.ValidateAdminOpsWSTicket(accessToken)
	require.ErrorIs(t, err, ErrInvalidToken)
}

func TestAdminOpsWSTicketRejectsExpiredAndWrongPurposeClaims(t *testing.T) {
	cfg := &config.Config{JWT: config.JWTConfig{Secret: "unit-test-secret-that-is-long-enough", ExpireHour: 1}}
	authService := NewAuthService(nil, nil, nil, nil, cfg, nil, nil, nil, nil, nil, nil, nil)

	sign := func(claims *AdminOpsWSTicketClaims) string {
		t.Helper()
		token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
		signed, err := token.SignedString(deriveAdminOpsWSTicketSigningKey(cfg.JWT.Secret))
		require.NoError(t, err)
		return signed
	}

	now := time.Now().UTC()
	baseClaims := func() *AdminOpsWSTicketClaims {
		return &AdminOpsWSTicketClaims{
			UserID:       42,
			TokenVersion: 7,
			Purpose:      adminOpsWSTicketPurpose,
			RegisteredClaims: jwt.RegisteredClaims{
				Audience:  jwt.ClaimStrings{adminOpsWSTicketAudience},
				ExpiresAt: jwt.NewNumericDate(now.Add(AdminOpsWSTicketTTL)),
				ID:        "ticket-id",
				IssuedAt:  jwt.NewNumericDate(now),
				Issuer:    adminOpsWSTicketIssuer,
				NotBefore: jwt.NewNumericDate(now),
				Subject:   "42",
			},
		}
	}

	wrongPurpose := baseClaims()
	wrongPurpose.Purpose = "admin_api"
	_, err := authService.ValidateAdminOpsWSTicket(sign(wrongPurpose))
	require.ErrorIs(t, err, ErrInvalidToken)

	expired := baseClaims()
	expired.IssuedAt = jwt.NewNumericDate(now.Add(-2 * time.Minute))
	expired.NotBefore = jwt.NewNumericDate(now.Add(-2 * time.Minute))
	expired.ExpiresAt = jwt.NewNumericDate(now.Add(-time.Minute))
	_, err = authService.ValidateAdminOpsWSTicket(sign(expired))
	require.ErrorIs(t, err, ErrTokenExpired)
}
