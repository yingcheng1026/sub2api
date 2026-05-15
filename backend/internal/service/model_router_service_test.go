package service

import (
	"context"
	"errors"
	"testing"
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
		{Pattern: "claude-opus-*", GroupName: "claude-opus"},
		{Pattern: "claude-sonnet-*", GroupName: "claude-sonnet"},
		{Pattern: "claude-haiku-*", GroupName: "claude-sonnet"},
		{Pattern: "gpt-*", GroupName: "gpt-5"},
		{Pattern: "o1-*", GroupName: "gpt-5"},
		{Pattern: "o3-*", GroupName: "gpt-5"},
		{Pattern: "gemini-*", GroupName: "gemini-2-pro"},
	}
	repo := &modelRouterGroupRepoStub{groups: []Group{
		{ID: 2, Name: "gpt-5", Status: StatusActive},
		{ID: 3, Name: "claude-sonnet", Status: StatusActive},
		{ID: 4, Name: "claude-opus", Status: StatusActive},
		{ID: 5, Name: "gemini-2-pro", Status: StatusActive},
	}}
	router := NewModelRouterServiceWithRoutes(repo, routes)

	tests := []struct {
		name  string
		model string
		want  int64
	}{
		{name: "claude opus routes to opus group", model: "claude-opus-4-20250514", want: 4},
		{name: "claude sonnet routes to sonnet group", model: "claude-sonnet-4-6", want: 3},
		{name: "claude haiku routes to sonnet group", model: "claude-haiku-4-5", want: 3},
		{name: "gpt routes to gpt group", model: "gpt-5", want: 2},
		{name: "o1 routes to gpt group", model: "o1-preview", want: 2},
		{name: "o3 routes to gpt group", model: "o3-mini", want: 2},
		{name: "gemini routes to gemini group", model: "gemini-2.5-pro", want: 5},
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
		{ID: 2, Name: "gpt-5", Status: StatusActive},
	}}, []ModelRoute{{Pattern: "gpt-*", GroupName: "gpt-5"}})

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
		{ID: 2, Name: "gpt-5", Status: StatusDisabled},
	}}, []ModelRoute{{Pattern: "gpt-*", GroupName: "gpt-5"}})

	_, err := router.ResolveGroupID(context.Background(), 99, "gpt-5")
	if !errors.Is(err, ErrModelUnsupported) {
		t.Fatalf("ResolveGroupID error = %v, want ErrModelUnsupported", err)
	}
}
