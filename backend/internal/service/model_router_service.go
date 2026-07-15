package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/fsnotify/fsnotify"
	"github.com/spf13/viper"
)

var ErrModelUnsupported = infraerrors.BadRequest("MODEL_UNSUPPORTED", "model unsupported")
var ErrModelRoutesConfigRejected = errors.New("model routes config rejected")

type ModelRouter interface {
	ResolveGroupID(ctx context.Context, userID int64, modelName string) (int64, error)
}

type ModelRouteProvider interface {
	Routes() []ModelRoute
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
		{Pattern: "claude-opus-*", GroupName: WalletDefaultVIPGroupName, ExampleModel: "claude-opus-4-7"},
		{Pattern: "claude-sonnet-*", GroupName: WalletDefaultVIPGroupName, ExampleModel: "claude-sonnet-4-6"},
		{Pattern: "claude-haiku-*", GroupName: WalletDefaultVIPGroupName, ExampleModel: "claude-haiku-4-5"},
		{Pattern: "claude-fable-*", GroupName: WalletDefaultVIPGroupName, ExampleModel: "claude-fable-5"},
		{Pattern: "fable", GroupName: WalletDefaultVIPGroupName, ExampleModel: "fable"},
		{Pattern: "fable5", GroupName: WalletDefaultVIPGroupName, ExampleModel: "fable5"},
		{Pattern: "gpt-*", GroupName: WalletDefaultOpenAIGroupName, ExampleModel: "gpt-5.6-high"},
		{Pattern: "o1-*", GroupName: WalletDefaultOpenAIGroupName, ExampleModel: "o1-preview"},
		{Pattern: "o3-*", GroupName: WalletDefaultOpenAIGroupName, ExampleModel: "o3-mini"},
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
	return WalletModelRouteGroupName(s.currentRoutes(), modelName)
}

// WalletModelRouteGroupName resolves a model through the same HFC wallet route
// policy used by ModelRouterService. Invalid or cross-business-group routes are
// ignored so model discovery cannot advertise a route that request routing
// would reject.
func WalletModelRouteGroupName(routes []ModelRoute, modelName string) (string, bool) {
	modelName = strings.TrimSpace(modelName)
	if modelName == "" {
		return "", false
	}
	for _, route := range routes {
		pattern := strings.TrimSpace(route.Pattern)
		groupName := strings.TrimSpace(route.GroupName)
		if !walletModelRouteMatchesBusinessPolicy(pattern, groupName) {
			continue
		}
		if matchModelRoute(pattern, modelName) {
			return groupName, true
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
	cleaned := cleanModelRoutes(routes)
	if len(cleaned) == 0 {
		cleaned = DefaultModelRoutes()
	}
	s.routes.Store(cleaned)
}

func (s *ModelRouterService) replaceRoutes(routes []ModelRoute) error {
	if len(routes) == 0 {
		return fmt.Errorf("%w: config contains no routes", ErrModelRoutesConfigRejected)
	}
	cleaned := cleanModelRoutes(routes)
	if len(cleaned) == 0 {
		return fmt.Errorf(
			"%w: all %d configured routes were rejected by the HFC wallet routing policy",
			ErrModelRoutesConfigRejected,
			len(routes),
		)
	}
	s.routes.Store(cleaned)
	return nil
}

func cleanModelRoutes(routes []ModelRoute) []ModelRoute {
	cleaned := make([]ModelRoute, 0, len(routes))
	for _, route := range routes {
		route.Pattern = strings.TrimSpace(route.Pattern)
		route.GroupName = strings.TrimSpace(route.GroupName)
		route.ExampleModel = strings.TrimSpace(route.ExampleModel)
		if route.Pattern == "" || route.GroupName == "" {
			continue
		}
		if route.GroupName != WalletDefaultOpenAIGroupName && route.GroupName != WalletDefaultVIPGroupName {
			continue
		}
		if !walletModelRouteMatchesBusinessPolicy(route.Pattern, route.GroupName) {
			continue
		}
		cleaned = append(cleaned, route)
	}
	return cleaned
}

func walletModelRouteMatchesBusinessPolicy(pattern, groupName string) bool {
	if strings.Count(pattern, "*") > 1 || (strings.Contains(pattern, "*") && !strings.HasSuffix(pattern, "*")) {
		return false
	}
	modelPrefix := strings.TrimSuffix(pattern, "*")
	expectedGroup := ""
	switch {
	case strings.HasPrefix(modelPrefix, "gpt-"), strings.HasPrefix(modelPrefix, "o1-"), strings.HasPrefix(modelPrefix, "o3-"):
		expectedGroup = WalletDefaultOpenAIGroupName
	case strings.HasPrefix(modelPrefix, "claude-"), modelPrefix == "fable", modelPrefix == "fable5", strings.HasPrefix(modelPrefix, "fable-"):
		expectedGroup = WalletDefaultVIPGroupName
	default:
		return false
	}
	return groupName == expectedGroup
}

func (s *ModelRouterService) loadConfig() {
	configFile := findModelRoutesConfig()
	if configFile == "" {
		return
	}

	v := viper.New()
	v.SetConfigFile(configFile)
	s.viper = v
	if err := s.ReloadConfig(); err != nil {
		logger.LegacyPrintf(
			"service.model_router",
			"[ModelRouter] initial config rejected; keeping last-known-good routes: file=%q routes_kept=%d err=%v",
			configFile,
			len(s.currentRoutes()),
			err,
		)
	}
	v.OnConfigChange(func(event fsnotify.Event) {
		if err := s.ReloadConfig(); err != nil {
			logger.LegacyPrintf(
				"service.model_router",
				"[ModelRouter] hot reload rejected; keeping last-known-good routes: file=%q op=%q routes_kept=%d err=%v",
				event.Name,
				event.Op.String(),
				len(s.currentRoutes()),
				err,
			)
		}
	})
	v.WatchConfig()
}

// ReloadConfig atomically replaces the active routes only after the configured
// file yields at least one route allowed by the HFC wallet routing policy.
func (s *ModelRouterService) ReloadConfig() error {
	if s.viper == nil {
		return fmt.Errorf("%w: no config file is configured", ErrModelRoutesConfigRejected)
	}
	configFile := s.viper.ConfigFileUsed()
	routes, err := readModelRoutesConfig(s.viper)
	if err != nil {
		return fmt.Errorf("%w: read config %q: %v", ErrModelRoutesConfigRejected, configFile, err)
	}
	if err := s.replaceRoutes(routes); err != nil {
		return fmt.Errorf("%w (file=%q)", err, configFile)
	}
	return nil
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
