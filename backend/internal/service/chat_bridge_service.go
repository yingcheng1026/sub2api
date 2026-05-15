package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

const defaultChatBridgeCodeTTL = 2 * time.Minute

var ErrChatBridgeCodeInvalid = infraerrors.Unauthorized("CHAT_BRIDGE_CODE_INVALID", "invalid or expired chat login code")

type chatBridgeUserReader interface {
	GetByID(ctx context.Context, id int64) (*User, error)
}

type chatBridgeTokenIssuer interface {
	GenerateToken(user *User) (string, error)
	GetAccessTokenExpiresIn() int
}

type authServiceChatBridgeUserReader struct {
	authService *AuthService
}

func (r authServiceChatBridgeUserReader) GetByID(ctx context.Context, id int64) (*User, error) {
	if r.authService == nil || r.authService.userRepo == nil {
		return nil, ErrServiceUnavailable
	}
	return r.authService.userRepo.GetByID(ctx, id)
}

type ChatBridgeLoginCode struct {
	Code      string
	ExpiresAt time.Time
	ExpiresIn int
}

type ChatBridgeExchangeResult struct {
	AccessToken string
	ExpiresIn   int
	TokenType   string
	User        *User
}

type chatBridgeCodeRecord struct {
	expiresAt time.Time
	userID    int64
}

type ChatBridgeService struct {
	mu          sync.Mutex
	codes       map[string]chatBridgeCodeRecord
	ttl         time.Duration
	tokenIssuer chatBridgeTokenIssuer
	userReader  chatBridgeUserReader
}

func NewChatBridgeService(authService *AuthService) *ChatBridgeService {
	if authService == nil {
		return nil
	}
	return newChatBridgeService(authServiceChatBridgeUserReader{authService: authService}, authService, defaultChatBridgeCodeTTL)
}

func newChatBridgeServiceForTest(userReader chatBridgeUserReader, tokenIssuer chatBridgeTokenIssuer, ttl time.Duration) *ChatBridgeService {
	return newChatBridgeService(userReader, tokenIssuer, ttl)
}

func newChatBridgeService(userReader chatBridgeUserReader, tokenIssuer chatBridgeTokenIssuer, ttl time.Duration) *ChatBridgeService {
	return &ChatBridgeService{
		codes:       make(map[string]chatBridgeCodeRecord),
		ttl:         ttl,
		tokenIssuer: tokenIssuer,
		userReader:  userReader,
	}
}

func (s *ChatBridgeService) CreateLoginCode(ctx context.Context, userID int64) (*ChatBridgeLoginCode, error) {
	if s == nil || s.userReader == nil {
		return nil, ErrServiceUnavailable
	}
	user, err := s.userReader.GetByID(ctx, userID)
	if err != nil {
		return nil, err
	}
	if !user.IsActive() {
		return nil, ErrUserNotActive
	}

	code, err := randomURLSafeToken(32)
	if err != nil {
		return nil, fmt.Errorf("generate chat bridge code: %w", err)
	}

	now := time.Now()
	expiresAt := now.Add(s.ttl)
	s.mu.Lock()
	s.cleanupExpiredLocked(now)
	s.codes[hashChatBridgeCode(code)] = chatBridgeCodeRecord{userID: userID, expiresAt: expiresAt}
	s.mu.Unlock()

	return &ChatBridgeLoginCode{
		Code:      code,
		ExpiresAt: expiresAt,
		ExpiresIn: int(s.ttl.Seconds()),
	}, nil
}

func (s *ChatBridgeService) ExchangeLoginCode(ctx context.Context, code string) (*ChatBridgeExchangeResult, error) {
	code = strings.TrimSpace(code)
	if s == nil || s.userReader == nil || s.tokenIssuer == nil || code == "" {
		return nil, ErrChatBridgeCodeInvalid
	}

	now := time.Now()
	key := hashChatBridgeCode(code)

	s.mu.Lock()
	record, ok := s.codes[key]
	delete(s.codes, key)
	s.cleanupExpiredLocked(now)
	s.mu.Unlock()

	if !ok || !record.expiresAt.After(now) {
		return nil, ErrChatBridgeCodeInvalid
	}

	user, err := s.userReader.GetByID(ctx, record.userID)
	if err != nil {
		return nil, err
	}
	if !user.IsActive() {
		return nil, ErrUserNotActive
	}

	token, err := s.tokenIssuer.GenerateToken(user)
	if err != nil {
		return nil, err
	}

	return &ChatBridgeExchangeResult{
		AccessToken: token,
		ExpiresIn:   s.tokenIssuer.GetAccessTokenExpiresIn(),
		TokenType:   "Bearer",
		User:        user,
	}, nil
}

func (s *ChatBridgeService) cleanupExpiredLocked(now time.Time) {
	for key, record := range s.codes {
		if !record.expiresAt.After(now) {
			delete(s.codes, key)
		}
	}
}

func hashChatBridgeCode(code string) string {
	sum := sha256.Sum256([]byte(code))
	return hex.EncodeToString(sum[:])
}

func randomURLSafeToken(byteLength int) (string, error) {
	if byteLength <= 0 {
		byteLength = 32
	}
	buf := make([]byte, byteLength)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}
