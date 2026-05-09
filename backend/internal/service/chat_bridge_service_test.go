package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type chatBridgeUserReaderStub struct {
	users map[int64]*User
}

func (s *chatBridgeUserReaderStub) GetByID(_ context.Context, id int64) (*User, error) {
	user, ok := s.users[id]
	if !ok {
		return nil, ErrUserNotFound
	}
	return user, nil
}

type chatBridgeTokenIssuerStub struct {
	token     string
	expiresIn int
	err       error
	userIDs   []int64
}

func (s *chatBridgeTokenIssuerStub) GenerateToken(user *User) (string, error) {
	if s.err != nil {
		return "", s.err
	}
	s.userIDs = append(s.userIDs, user.ID)
	return s.token, nil
}

func (s *chatBridgeTokenIssuerStub) GetAccessTokenExpiresIn() int {
	return s.expiresIn
}

func TestChatBridgeServiceCreateAndExchangeLoginCodeOnce(t *testing.T) {
	userReader := &chatBridgeUserReaderStub{
		users: map[int64]*User{
			42: {ID: 42, Email: "user@example.com", Role: RoleUser, Status: StatusActive},
		},
	}
	tokenIssuer := &chatBridgeTokenIssuerStub{token: "user-jwt", expiresIn: 3600}
	svc := newChatBridgeServiceForTest(userReader, tokenIssuer, time.Minute)

	code, err := svc.CreateLoginCode(context.Background(), 42)
	require.NoError(t, err)
	require.NotEmpty(t, code.Code)
	require.Equal(t, 60, code.ExpiresIn)

	exchanged, err := svc.ExchangeLoginCode(context.Background(), code.Code)
	require.NoError(t, err)
	require.Equal(t, "user-jwt", exchanged.AccessToken)
	require.Equal(t, "Bearer", exchanged.TokenType)
	require.Equal(t, 3600, exchanged.ExpiresIn)
	require.Equal(t, int64(42), exchanged.User.ID)
	require.Equal(t, []int64{42}, tokenIssuer.userIDs)

	_, err = svc.ExchangeLoginCode(context.Background(), code.Code)
	require.ErrorIs(t, err, ErrChatBridgeCodeInvalid)
}

func TestChatBridgeServiceRejectsInactiveUser(t *testing.T) {
	userReader := &chatBridgeUserReaderStub{
		users: map[int64]*User{
			7: {ID: 7, Email: "disabled@example.com", Role: RoleUser, Status: StatusDisabled},
		},
	}
	svc := newChatBridgeServiceForTest(userReader, &chatBridgeTokenIssuerStub{}, time.Minute)

	_, err := svc.CreateLoginCode(context.Background(), 7)
	require.ErrorIs(t, err, ErrUserNotActive)
}

func TestChatBridgeServiceRejectsExpiredCode(t *testing.T) {
	userReader := &chatBridgeUserReaderStub{
		users: map[int64]*User{
			42: {ID: 42, Email: "user@example.com", Role: RoleUser, Status: StatusActive},
		},
	}
	svc := newChatBridgeServiceForTest(userReader, &chatBridgeTokenIssuerStub{token: "user-jwt", expiresIn: 3600}, -time.Second)

	code, err := svc.CreateLoginCode(context.Background(), 42)
	require.NoError(t, err)

	_, err = svc.ExchangeLoginCode(context.Background(), code.Code)
	require.ErrorIs(t, err, ErrChatBridgeCodeInvalid)
}

func TestChatBridgeServiceReturnsTokenIssuerError(t *testing.T) {
	userReader := &chatBridgeUserReaderStub{
		users: map[int64]*User{
			42: {ID: 42, Email: "user@example.com", Role: RoleUser, Status: StatusActive},
		},
	}
	tokenErr := errors.New("sign failed")
	svc := newChatBridgeServiceForTest(userReader, &chatBridgeTokenIssuerStub{err: tokenErr}, time.Minute)

	code, err := svc.CreateLoginCode(context.Background(), 42)
	require.NoError(t, err)

	_, err = svc.ExchangeLoginCode(context.Background(), code.Code)
	require.ErrorIs(t, err, tokenErr)
}
