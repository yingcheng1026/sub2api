package service

import (
	"crypto/hmac"
	"crypto/sha256"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const (
	adminOpsWSTicketAudience = "sub2api-admin-ops-ws"
	adminOpsWSTicketIssuer   = "sub2api"
	adminOpsWSTicketPurpose  = "admin_ops_ws"
	AdminOpsWSTicketTTL      = 20 * time.Second
)

var adminOpsWSTicketKeyContext = []byte("sub2api/admin-ops-ws-ticket/v1")

type AdminOpsWSTicketClaims struct {
	UserID       int64  `json:"user_id"`
	TokenVersion int64  `json:"token_version"`
	Purpose      string `json:"purpose"`
	jwt.RegisteredClaims
}

func deriveAdminOpsWSTicketSigningKey(secret string) []byte {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(adminOpsWSTicketKeyContext)
	return mac.Sum(nil)
}

// GenerateAdminOpsWSTicket creates a short-lived, purpose-bound credential for
// the browser WebSocket handshake. A domain-separated signing key prevents the
// ticket from being replayed as an ordinary admin access token.
func (s *AuthService) GenerateAdminOpsWSTicket(user *User) (string, time.Time, error) {
	if s == nil || s.cfg == nil || s.cfg.JWT.Secret == "" {
		return "", time.Time{}, fmt.Errorf("admin ops websocket ticket signing is unavailable")
	}
	if user == nil || user.ID <= 0 || !user.IsActive() || !user.IsAdmin() {
		return "", time.Time{}, ErrInvalidToken
	}

	now := time.Now().UTC()
	expiresAt := now.Add(AdminOpsWSTicketTTL)
	ticketID, err := randomHexString(16)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("generate admin ops websocket ticket id: %w", err)
	}

	claims := &AdminOpsWSTicketClaims{
		UserID:       user.ID,
		TokenVersion: resolvedTokenVersion(user),
		Purpose:      adminOpsWSTicketPurpose,
		RegisteredClaims: jwt.RegisteredClaims{
			Audience:  jwt.ClaimStrings{adminOpsWSTicketAudience},
			ExpiresAt: jwt.NewNumericDate(expiresAt),
			ID:        ticketID,
			IssuedAt:  jwt.NewNumericDate(now),
			Issuer:    adminOpsWSTicketIssuer,
			NotBefore: jwt.NewNumericDate(now),
			Subject:   strconv.FormatInt(user.ID, 10),
		},
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString(deriveAdminOpsWSTicketSigningKey(s.cfg.JWT.Secret))
	if err != nil {
		return "", time.Time{}, fmt.Errorf("sign admin ops websocket ticket: %w", err)
	}
	return signed, expiresAt, nil
}

func (s *AuthService) ValidateAdminOpsWSTicket(tokenString string) (*AdminOpsWSTicketClaims, error) {
	if s == nil || s.cfg == nil || s.cfg.JWT.Secret == "" || tokenString == "" {
		return nil, ErrInvalidToken
	}
	if len(tokenString) > maxTokenLength {
		return nil, ErrTokenTooLarge
	}

	claims := &AdminOpsWSTicketClaims{}
	token, err := jwt.ParseWithClaims(
		tokenString,
		claims,
		func(token *jwt.Token) (any, error) {
			if token.Method != jwt.SigningMethodHS256 {
				return nil, fmt.Errorf("unexpected signing method")
			}
			return deriveAdminOpsWSTicketSigningKey(s.cfg.JWT.Secret), nil
		},
		jwt.WithAudience(adminOpsWSTicketAudience),
		jwt.WithExpirationRequired(),
		jwt.WithIssuedAt(),
		jwt.WithIssuer(adminOpsWSTicketIssuer),
		jwt.WithLeeway(time.Second),
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Name}),
	)
	if err != nil || !token.Valid {
		if errors.Is(err, jwt.ErrTokenExpired) {
			return nil, ErrTokenExpired
		}
		return nil, ErrInvalidToken
	}
	if claims.UserID <= 0 ||
		claims.Purpose != adminOpsWSTicketPurpose ||
		claims.ID == "" ||
		claims.Subject != strconv.FormatInt(claims.UserID, 10) ||
		claims.IssuedAt == nil ||
		claims.ExpiresAt == nil ||
		claims.ExpiresAt.Time.Sub(claims.IssuedAt.Time) > AdminOpsWSTicketTTL+time.Second {
		return nil, ErrInvalidToken
	}
	return claims, nil
}
