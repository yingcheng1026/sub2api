package service

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/fsnotify/fsnotify"
	"github.com/spf13/viper"
)

var ErrModelUnsupported = infraerrors.BadRequest("MODEL_UNSUPPORTED", "model unsupported")

type ModelRouter interface {
	ResolveGroupID(ctx context.Context, userID int64, modelName string) (int64, error)
}

type ModelRouterGroupRepository interface {
	ListActive(ctx context.Context) ([]Group, error)
}

type ModelRoute struct {
	Pattern      string `mapstructure:"pattern" yaml:"pattern" json:"pattern"`
	GroupName    string `mapstructure:"group_name" yaml:"group_name" json:"group_name"`
	ExampleModel string `mapstructure:"example_model" yaml:"example_model" json:"example_model,omitempty"`
}

type modelRoutesConfig struct {
	Routes []ModelRoute `mapstructure:"routes"`
}

type ModelRouterService struct {
	groupRepo ModelRouterGroupRepository
	routes    atomic.Value
	viper     *viper.Viper
}

func NewModelRouterService(groupRepo ModelRouterGroupRepository) *ModelRouterService {
	svc := NewModelRouterServiceWithRoutes(groupRepo, DefaultModelRoutes())
	svc.loadConfig()
	return svc
}

func NewModelRouterServiceWithRoutes(groupRepo ModelRouterGroupRepository, routes []ModelRoute) *ModelRouterService {
	svc := &ModelRouterService{groupRepo: groupRepo}
	svc.setRoutes(routes)
	return svc
}

func DefaultModelRoutes() []ModelRoute {
	return []ModelRoute{
		{Pattern: "claude-opus-*", GroupName: "claude-opus", ExampleModel: "claude-opus-4-1"},
		{Pattern: "claude-sonnet-*", GroupName: "claude-sonnet", ExampleModel: "claude-sonnet-4-6"},
		{Pattern: "claude-haiku-*", GroupName: "claude-sonnet", ExampleModel: "claude-haiku-4-5"},
		{Pattern: "gpt-*", GroupName: "gpt-5", ExampleModel: "gpt-5"},
		{Pattern: "o1-*", GroupName: "gpt-5", ExampleModel: "o1-preview"},
		{Pattern: "o3-*", GroupName: "gpt-5", ExampleModel: "o3-mini"},
		{Pattern: "gemini-*", GroupName: "gemini-2-pro", ExampleModel: "gemini-2.5-pro"},
	}
}

func (s *ModelRouterService) ResolveGroupID(ctx context.Context, userID int64, modelName string) (int64, error) {
	_ = userID
	modelName = strings.TrimSpace(modelName)
	if modelName == "" {
		return 0, ErrModelUnsupported
	}

	groupName, ok := s.resolveGroupName(modelName)
	if !ok {
		return 0, ErrModelUnsupported
	}

	groups, err := s.groupRepo.ListActive(ctx)
	if err != nil {
		return 0, fmt.Errorf("list active groups: %w", err)
	}
	for i := range groups {
		group := &groups[i]
		if group.Name == groupName && group.Status == StatusActive {
			return group.ID, nil
		}
	}
	return 0, ErrModelUnsupported
}

func (s *ModelRouterService) Routes() []ModelRoute {
	return cloneModelRoutes(s.currentRoutes())
}

func (s *ModelRouterService) resolveGroupName(modelName string) (string, bool) {
	for _, route := range s.currentRoutes() {
		if route.GroupName == "" || route.Pattern == "" {
			continue
		}
		if matchModelRoute(route.Pattern, modelName) {
			return route.GroupName, true
		}
	}
	return "", false
}

func (s *ModelRouterService) currentRoutes() []ModelRoute {
	if loaded := s.routes.Load(); loaded != nil {
		if routes, ok := loaded.([]ModelRoute); ok {
			return routes
		}
	}
	return DefaultModelRoutes()
}

func (s *ModelRouterService) setRoutes(routes []ModelRoute) {
	cleaned := make([]ModelRoute, 0, len(routes))
	for _, route := range routes {
		route.Pattern = strings.TrimSpace(route.Pattern)
		route.GroupName = strings.TrimSpace(route.GroupName)
		route.ExampleModel = strings.TrimSpace(route.ExampleModel)
		if route.Pattern == "" || route.GroupName == "" {
			continue
		}
		cleaned = append(cleaned, route)
	}
	if len(cleaned) == 0 {
		cleaned = DefaultModelRoutes()
	}
	s.routes.Store(cleaned)
}

func (s *ModelRouterService) loadConfig() {
	configFile := findModelRoutesConfig()
	if configFile == "" {
		return
	}

	v := viper.New()
	v.SetConfigFile(configFile)
	if routes, err := readModelRoutesConfig(v); err == nil {
		s.setRoutes(routes)
	}
	v.OnConfigChange(func(fsnotify.Event) {
		if routes, err := readModelRoutesConfig(v); err == nil {
			s.setRoutes(routes)
		}
	})
	v.WatchConfig()
	s.viper = v
}

func readModelRoutesConfig(v *viper.Viper) ([]ModelRoute, error) {
	if err := v.ReadInConfig(); err != nil {
		return nil, err
	}
	var cfg modelRoutesConfig
	if err := v.Unmarshal(&cfg); err != nil {
		return nil, err
	}
	return cfg.Routes, nil
}

func findModelRoutesConfig() string {
	if explicit := strings.TrimSpace(os.Getenv("MODEL_ROUTES_CONFIG")); explicit != "" {
		if fileExists(explicit) {
			return explicit
		}
	}
	for _, path := range []string{
		filepath.Join("config", "model_routes.yaml"),
		filepath.Join(".", "model_routes.yaml"),
		filepath.Join("..", "config", "model_routes.yaml"),
		filepath.Join("backend", "config", "model_routes.yaml"),
		filepath.Join("/app", "data", "model_routes.yaml"),
		filepath.Join("/app", "config", "model_routes.yaml"),
		filepath.Join("/etc", "sub2api", "model_routes.yaml"),
	} {
		if fileExists(path) {
			return path
		}
	}
	return ""
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func cloneModelRoutes(routes []ModelRoute) []ModelRoute {
	return append([]ModelRoute(nil), routes...)
}

func matchModelRoute(pattern, modelName string) bool {
	if pattern == modelName {
		return true
	}
	if strings.HasSuffix(pattern, "*") {
		prefix := strings.TrimSuffix(pattern, "*")
		return strings.HasPrefix(modelName, prefix)
	}
	return false
}
