//go:build unit

package service

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

type usageOwnerScopeRepoStub struct {
	UsageLogRepository
	globalRecord *UsageLog
	scopedRecord *UsageLog
	scopedErr    error
	scopedCalled bool
}

func (s *usageOwnerScopeRepoStub) GetByID(context.Context, int64) (*UsageLog, error) {
	if s.globalRecord == nil {
		return nil, ErrUsageLogNotFound
	}
	return s.globalRecord, nil
}

func (s *usageOwnerScopeRepoStub) GetByIDForUser(context.Context, int64, int64) (*UsageLog, error) {
	s.scopedCalled = true
	return s.scopedRecord, s.scopedErr
}

type usageGlobalOnlyRepoStub struct {
	UsageLogRepository
	record *UsageLog
}

func (s *usageGlobalOnlyRepoStub) GetByID(context.Context, int64) (*UsageLog, error) {
	return s.record, nil
}

func TestUsageServiceGetByIDForUserUsesOwnerScopedRepositoryLookup(t *testing.T) {
	repo := &usageOwnerScopeRepoStub{scopedErr: ErrUsageLogNotFound}
	svc := NewUsageService(repo, nil, nil, nil)

	record, err := svc.GetByIDForUser(context.Background(), 41, 7)

	require.Nil(t, record)
	require.ErrorIs(t, err, ErrUsageLogNotFound)
	require.True(t, repo.scopedCalled)
}

func TestUsageServiceGetByIDForUserFallbackHidesForeignRecordExistence(t *testing.T) {
	repo := &usageGlobalOnlyRepoStub{record: &UsageLog{ID: 41, UserID: 8}}
	svc := NewUsageService(repo, nil, nil, nil)

	record, err := svc.GetByIDForUser(context.Background(), 41, 7)

	require.Nil(t, record)
	require.True(t, errors.Is(err, ErrUsageLogNotFound))
}
