//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type apiKeyUpdateCaptureRepo struct {
	*apiKeyRepoStub
	updated *APIKey
}

func (r *apiKeyUpdateCaptureRepo) Update(_ context.Context, key *APIKey) error {
	copy := *key
	copy.IPWhitelist = append([]string(nil), key.IPWhitelist...)
	copy.IPBlacklist = append([]string(nil), key.IPBlacklist...)
	r.updated = &copy
	return nil
}

func TestAPIKeyUpdatePreservesOmittedIPPolicies(t *testing.T) {
	repo := &apiKeyUpdateCaptureRepo{apiKeyRepoStub: &apiKeyRepoStub{apiKey: &APIKey{
		ID:          17,
		UserID:      23,
		Name:        "before",
		Key:         "sk-sensitive",
		Status:      StatusActive,
		IPWhitelist: []string{"10.0.0.1"},
		IPBlacklist: []string{"192.0.2.0/24"},
	}}}
	svc := NewAPIKeyService(repo, nil, nil, nil, nil, nil, &config.Config{})
	newName := "after"

	_, err := svc.Update(context.Background(), 17, 23, UpdateAPIKeyRequest{Name: &newName})
	require.NoError(t, err)
	require.Equal(t, []string{"10.0.0.1"}, repo.updated.IPWhitelist)
	require.Equal(t, []string{"192.0.2.0/24"}, repo.updated.IPBlacklist)
}

func TestAPIKeyUpdateClearsOnlyExplicitEmptyIPPolicies(t *testing.T) {
	repo := &apiKeyUpdateCaptureRepo{apiKeyRepoStub: &apiKeyRepoStub{apiKey: &APIKey{
		ID:          18,
		UserID:      24,
		Name:        "before",
		Key:         "sk-sensitive",
		Status:      StatusActive,
		IPWhitelist: []string{"10.0.0.1"},
		IPBlacklist: []string{"192.0.2.0/24"},
	}}}
	svc := NewAPIKeyService(repo, nil, nil, nil, nil, nil, &config.Config{})
	empty := []string{}

	_, err := svc.Update(context.Background(), 18, 24, UpdateAPIKeyRequest{IPWhitelist: &empty, StepUpVerified: true})
	require.NoError(t, err)
	require.Empty(t, repo.updated.IPWhitelist)
	require.Equal(t, []string{"192.0.2.0/24"}, repo.updated.IPBlacklist)
}

func TestAPIKeyUpdateRejectsSensitiveMutationWithoutFreshStepUp(t *testing.T) {
	repo := &apiKeyUpdateCaptureRepo{apiKeyRepoStub: &apiKeyRepoStub{apiKey: &APIKey{
		ID:          20,
		UserID:      26,
		Name:        "restricted",
		Key:         "sk-sensitive",
		Status:      StatusActive,
		IPWhitelist: []string{"10.0.0.1"},
	}}}
	svc := NewAPIKeyService(repo, nil, nil, nil, nil, nil, &config.Config{})
	empty := []string{}

	_, err := svc.Update(context.Background(), 20, 26, UpdateAPIKeyRequest{IPWhitelist: &empty})

	require.ErrorIs(t, err, ErrAPIKeyUpdateVerification)
	require.Nil(t, repo.updated)
}

func TestAPIKeyUpdateStepUpClassification(t *testing.T) {
	name := "renamed"
	inactive := "inactive"
	active := StatusAPIKeyActive
	groupID := int64(9)
	empty := []string{}
	quota := 10.0
	expiresAt := time.Now().Add(time.Hour)
	reset := true
	noReset := false
	rate := 1.0

	tests := []struct {
		name string
		req  UpdateAPIKeyRequest
		want bool
	}{
		{name: "rename", req: UpdateAPIKeyRequest{Name: &name}},
		{name: "deactivate", req: UpdateAPIKeyRequest{Status: &inactive}},
		{name: "false resets", req: UpdateAPIKeyRequest{ResetQuota: &noReset, ResetRateLimitUsage: &noReset}},
		{name: "activate", req: UpdateAPIKeyRequest{Status: &active}, want: true},
		{name: "group", req: UpdateAPIKeyRequest{GroupID: &groupID}, want: true},
		{name: "ip policy", req: UpdateAPIKeyRequest{IPWhitelist: &empty}, want: true},
		{name: "quota", req: UpdateAPIKeyRequest{Quota: &quota}, want: true},
		{name: "expiration", req: UpdateAPIKeyRequest{ExpiresAt: &expiresAt}, want: true},
		{name: "clear expiration", req: UpdateAPIKeyRequest{ClearExpiration: true}, want: true},
		{name: "reset quota", req: UpdateAPIKeyRequest{ResetQuota: &reset}, want: true},
		{name: "rate limit", req: UpdateAPIKeyRequest{RateLimit5h: &rate}, want: true},
		{name: "reset rate usage", req: UpdateAPIKeyRequest{ResetRateLimitUsage: &reset}, want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, APIKeyUpdateRequiresStepUp(tt.req))
		})
	}
}

func TestAPIKeyUpdateRejectsOwnerReactivationOfDisabledKey(t *testing.T) {
	repo := &apiKeyUpdateCaptureRepo{apiKeyRepoStub: &apiKeyRepoStub{apiKey: &APIKey{
		ID: 19, UserID: 25, Name: "disabled by abuse review", Key: "sk-sensitive", Status: StatusAPIKeyDisabled,
	}}}
	svc := NewAPIKeyService(repo, nil, nil, nil, nil, nil, &config.Config{})
	active := StatusAPIKeyActive

	_, err := svc.Update(context.Background(), 19, 25, UpdateAPIKeyRequest{Status: &active})

	require.ErrorIs(t, err, ErrAPIKeyReactivationForbidden)
	require.Nil(t, repo.updated)
}
