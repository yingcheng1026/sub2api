package service

import (
	"context"
	"errors"
	"runtime"
	"testing"
	"time"
)

type updateSecurityCache struct{}

func (updateSecurityCache) GetUpdateInfo(context.Context) (string, error) {
	return "", errors.New("cache miss")
}

func (updateSecurityCache) SetUpdateInfo(context.Context, string, time.Duration) error {
	return nil
}

type updateSecurityReleaseClient struct {
	release        *GitHubRelease
	downloadCalled bool
}

func (c *updateSecurityReleaseClient) FetchLatestRelease(context.Context, string) (*GitHubRelease, error) {
	return c.release, nil
}

func (c *updateSecurityReleaseClient) DownloadFile(context.Context, string, string, int64) error {
	c.downloadCalled = true
	return nil
}

func (c *updateSecurityReleaseClient) FetchChecksumFile(context.Context, string) ([]byte, error) {
	return nil, errors.New("unexpected checksum fetch")
}

func TestOfficialUpdateMutationDisabledByDefault(t *testing.T) {
	t.Setenv(officialUpdateApplyEnabledEnv, "")
	svc := NewUpdateService(nil, nil, "0.1.151-hfc", "release")

	if err := svc.PerformUpdate(context.Background()); !errors.Is(err, ErrOfficialUpdateApplyDisabled) {
		t.Fatalf("PerformUpdate() error=%v, want disabled guard", err)
	}
	if err := svc.Rollback(); !errors.Is(err, ErrOfficialUpdateApplyDisabled) {
		t.Fatalf("Rollback() error=%v, want disabled guard", err)
	}
}

func TestOfficialUpdateMutationRequiresExplicitTrue(t *testing.T) {
	for _, value := range []string{"1", "yes", "TRUE-ish", "false"} {
		t.Run(value, func(t *testing.T) {
			t.Setenv(officialUpdateApplyEnabledEnv, value)
			svc := NewUpdateService(nil, nil, "0.1.151-hfc", "release")
			if svc.updateApplyAllowed {
				t.Fatalf("update apply unexpectedly enabled for %q", value)
			}
		})
	}
}

func TestOfficialUpdateMutationCannotBeEnabledByEnvironmentForHFCBuild(t *testing.T) {
	t.Setenv(officialUpdateApplyEnabledEnv, "true")
	svc := NewUpdateService(nil, nil, "0.1.151-hfc", "release")

	if svc.updateApplyAllowed {
		t.Fatal("HFC build must not permit an environment-only upstream binary replacement")
	}
	if err := svc.PerformUpdate(context.Background()); !errors.Is(err, ErrOfficialUpdateApplyDisabled) {
		t.Fatalf("PerformUpdate() error=%v, want disabled compatibility guard", err)
	}
}

func TestOfficialUpdateRequiresChecksumAssetBeforeDownload(t *testing.T) {
	client := &updateSecurityReleaseClient{}
	svc := &UpdateService{
		cache:              updateSecurityCache{},
		githubClient:       client,
		currentVersion:     "1.0.0",
		buildType:          "release",
		updateApplyAllowed: true, // bypass the HFC constructor gate to test defense in depth
	}
	client.release = &GitHubRelease{
		TagName: "v1.0.1",
		Assets: []GitHubAsset{{
			Name:               "sub2api_" + runtime.GOOS + "_" + runtime.GOARCH + ".tar.gz",
			BrowserDownloadURL: "https://github.com/Wei-Shaw/sub2api/releases/download/v1.0.1/sub2api.tar.gz",
		}},
	}

	err := svc.PerformUpdate(context.Background())
	if !errors.Is(err, ErrOfficialUpdateChecksumRequired) {
		t.Fatalf("PerformUpdate() error=%v, want mandatory checksum error", err)
	}
	if client.downloadCalled {
		t.Fatal("archive download must not start when checksums.txt is absent")
	}
}
