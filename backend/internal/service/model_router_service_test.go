package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/require"
)

type modelRouterGroupRepoStub struct {
	groups []Group
	err    error
}

func (s *modelRouterGroupRepoStub) ListActive(ctx context.Context) ([]Group, error) {
	if s.err != nil {
		return nil, s.err
	}
	return append([]Group(nil), s.groups...), nil
}

func TestModelRouterServiceResolveGroupID(t *testing.T) {
	routes := []ModelRoute{
		{Pattern: "claude-opus-*", GroupName: "vip"},
		{Pattern: "claude-sonnet-*", GroupName: "vip"},
		{Pattern: "claude-haiku-*", GroupName: "vip"},
		{Pattern: "gpt-*", GroupName: "openai-default"},
		{Pattern: "o1-*", GroupName: "openai-default"},
		{Pattern: "o3-*", GroupName: "openai-default"},
	}
	repo := &modelRouterGroupRepoStub{groups: []Group{
		{ID: 2, Name: "openai-default", Status: StatusActive},
		{ID: 3, Name: "vip", Status: StatusActive},
	}}
	router := NewModelRouterServiceWithRoutes(repo, routes)

	tests := []struct {
		name  string
		model string
		want  int64
	}{
		{name: "claude opus routes to vip", model: "claude-opus-4-20250514", want: 3},
		{name: "claude sonnet routes to sonnet group", model: "claude-sonnet-4-6", want: 3},
		{name: "claude haiku routes to sonnet group", model: "claude-haiku-4-5", want: 3},
		{name: "gpt routes to gpt group", model: "gpt-5", want: 2},
		{name: "o1 routes to gpt group", model: "o1-preview", want: 2},
		{name: "o3 routes to gpt group", model: "o3-mini", want: 2},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := router.ResolveGroupID(context.Background(), 99, tt.model)
			if err != nil {
				t.Fatalf("ResolveGroupID returned error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("ResolveGroupID = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestModelRouterServiceResolveGroupIDUnsupported(t *testing.T) {
	router := NewModelRouterServiceWithRoutes(&modelRouterGroupRepoStub{groups: []Group{
		{ID: 2, Name: "openai-default", Status: StatusActive},
	}}, []ModelRoute{{Pattern: "gpt-*", GroupName: "openai-default"}})

	for _, model := range []string{"", "   ", "unknown-model-xyz", "anthropic/claude-sonnet-4-6"} {
		t.Run(model, func(t *testing.T) {
			_, err := router.ResolveGroupID(context.Background(), 99, model)
			if !errors.Is(err, ErrModelUnsupported) {
				t.Fatalf("ResolveGroupID error = %v, want ErrModelUnsupported", err)
			}
		})
	}
}

func TestModelRouterServiceRequiresActiveGroup(t *testing.T) {
	router := NewModelRouterServiceWithRoutes(&modelRouterGroupRepoStub{groups: []Group{
		{ID: 2, Name: "openai-default", Status: StatusDisabled},
	}}, []ModelRoute{{Pattern: "gpt-*", GroupName: "openai-default"}})

	_, err := router.ResolveGroupID(context.Background(), 99, "gpt-5")
	if !errors.Is(err, ErrModelUnsupported) {
		t.Fatalf("ResolveGroupID error = %v, want ErrModelUnsupported", err)
	}
}

func TestDefaultModelRoutesMatchHFCWalletBusinessGroups(t *testing.T) {
	routes := DefaultModelRoutes()
	repo := &modelRouterGroupRepoStub{groups: []Group{
		{ID: 3, Name: "openai-default", Status: StatusActive},
		{ID: 22, Name: "vip", Status: StatusActive},
	}}
	router := NewModelRouterServiceWithRoutes(repo, routes)

	tests := []struct {
		model string
		want  int64
	}{
		{model: "gpt-5.6-high", want: 3},
		{model: "o1-preview", want: 3},
		{model: "o3-mini", want: 3},
		{model: "claude-opus-4-7", want: 22},
		{model: "claude-sonnet-4-6", want: 22},
		{model: "claude-haiku-4-5", want: 22},
		{model: "claude-fable-5", want: 22},
		{model: "fable5", want: 22},
	}

	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			got, err := router.ResolveGroupID(context.Background(), 99, tt.model)
			if err != nil {
				t.Fatalf("ResolveGroupID returned error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("ResolveGroupID = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestModelRouterStartupWithoutConfigKeepsDefaultRoutes(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("MODEL_ROUTES_CONFIG", filepath.Join(t.TempDir(), "missing-model-routes.yaml"))

	router := NewModelRouterService(&modelRouterGroupRepoStub{})

	require.Equal(t, DefaultModelRoutes(), router.Routes())
	require.Nil(t, router.viper)
}

func TestModelRouterDropsRoutesThatViolateHFCModelGroupPolicy(t *testing.T) {
	repo := &modelRouterGroupRepoStub{groups: []Group{
		{ID: 3, Name: WalletDefaultOpenAIGroupName, Status: StatusActive},
		{ID: 22, Name: WalletDefaultVIPGroupName, Status: StatusActive},
	}}
	router := NewModelRouterServiceWithRoutes(repo, []ModelRoute{
		{Pattern: "gpt-*", GroupName: WalletDefaultVIPGroupName},
		{Pattern: "claude-*", GroupName: WalletDefaultOpenAIGroupName},
		{Pattern: "*", GroupName: WalletDefaultOpenAIGroupName},
		{Pattern: "gpt-*", GroupName: WalletDefaultOpenAIGroupName},
	})

	groupID, err := router.ResolveGroupID(context.Background(), 7, "gpt-5.6-high")
	require.NoError(t, err)
	require.Equal(t, int64(3), groupID)
	_, err = router.ResolveGroupID(context.Background(), 7, "claude-sonnet-4-6")
	require.ErrorIs(t, err, ErrModelUnsupported)
	require.Equal(t, []ModelRoute{{Pattern: "gpt-*", GroupName: WalletDefaultOpenAIGroupName}}, router.Routes())
}

func TestModelRouterReloadRejectsEmptyConfigAndKeepsLastKnownGoodRoutes(t *testing.T) {
	initialRoutes := []ModelRoute{{Pattern: "gpt-*", GroupName: WalletDefaultOpenAIGroupName}}
	router := NewModelRouterServiceWithRoutes(&modelRouterGroupRepoStub{}, initialRoutes)
	router.viper = modelRouterTestViper(t, "routes: []\n")

	err := router.ReloadConfig()

	require.ErrorIs(t, err, ErrModelRoutesConfigRejected)
	require.ErrorContains(t, err, "contains no routes")
	require.Equal(t, initialRoutes, router.Routes())
}

func TestModelRouterReloadRejectsAllInvalidConfigAndKeepsLastKnownGoodRoutes(t *testing.T) {
	initialRoutes := []ModelRoute{{Pattern: "claude-*", GroupName: WalletDefaultVIPGroupName}}
	router := NewModelRouterServiceWithRoutes(&modelRouterGroupRepoStub{}, initialRoutes)
	router.viper = modelRouterTestViper(t, `routes:
  - pattern: "gpt-*"
    group_name: "vip"
  - pattern: "*"
    group_name: "openai-default"
`)

	err := router.ReloadConfig()

	require.ErrorIs(t, err, ErrModelRoutesConfigRejected)
	require.ErrorContains(t, err, "all 2 configured routes were rejected")
	require.Equal(t, initialRoutes, router.Routes())
}

func modelRouterTestViper(t *testing.T, contents string) *viper.Viper {
	t.Helper()
	configPath := filepath.Join(t.TempDir(), "model_routes.yaml")
	require.NoError(t, os.WriteFile(configPath, []byte(contents), 0o600))
	v := viper.New()
	v.SetConfigFile(configPath)
	return v
}
