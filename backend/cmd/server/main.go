package main

//go:generate go run github.com/google/wire/cmd/wire

import (
	"context"
	_ "embed"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	_ "github.com/Wei-Shaw/sub2api/ent/runtime"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/handler"
	"github.com/Wei-Shaw/sub2api/internal/payment"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/repository"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/Wei-Shaw/sub2api/internal/setup"
	"github.com/Wei-Shaw/sub2api/internal/web"

	"github.com/gin-gonic/gin"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
)

//go:embed VERSION
var embeddedVersion string

// Build-time variables (can be set by ldflags)
var (
	Version   = ""
	Commit    = "unknown"
	Date      = "unknown"
	BuildType = "source" // "source" for manual builds, "release" for CI builds (set by ldflags)
)

const defaultServerShutdownTimeout = 30 * time.Minute

const (
	apiKeySecurityMigrationTimeout = 30 * time.Minute
	paymentConfigMigrationTimeout  = 5 * time.Minute
	domainSecretMigrationTimeout   = 30 * time.Minute
	schedulerCachePurgeTimeout     = 2 * time.Minute
	oauthTokenCachePurgeTimeout    = 2 * time.Minute
)

func init() {
	// 如果 Version 已通过 ldflags 注入（例如 -X main.Version=...），则不要覆盖。
	if strings.TrimSpace(Version) != "" {
		return
	}

	// 默认从 embedded VERSION 文件读取版本号（编译期打包进二进制）。
	Version = strings.TrimSpace(embeddedVersion)
	if Version == "" {
		Version = "0.0.0-dev"
	}
}

// initLogger configures the default slog handler based on gin.Mode().
// In non-release mode, Debug level logs are enabled.
func main() {
	logger.InitBootstrap()
	defer logger.Sync()

	// Parse command line flags
	setupMode := flag.Bool("setup", false, "Run setup wizard in CLI mode")
	showVersion := flag.Bool("version", false, "Show version information")
	flag.Parse()

	if *showVersion {
		log.Printf("Sub2API %s (commit: %s, built: %s)\n", Version, Commit, Date)
		return
	}

	// CLI setup mode
	if *setupMode {
		if err := setup.RunCLI(); err != nil {
			log.Fatalf("Setup failed: %v", err)
		}
		return
	}

	// Check if setup is needed
	if setup.NeedsSetup() {
		// Check if auto-setup is enabled (for Docker deployment)
		if setup.AutoSetupEnabled() {
			log.Println("Auto setup mode enabled...")
			if err := setup.AutoSetupFromEnv(); err != nil {
				log.Fatalf("Auto setup failed: %v", err)
			}
			// Continue to main server after auto-setup
		} else {
			log.Println("First run detected, starting setup wizard...")
			runSetupServer()
			return
		}
	}

	// Normal server mode
	runMainServer()
}

func runSetupServer() {
	r := gin.New()
	r.Use(middleware.Recovery())
	r.Use(middleware.CORS(config.CORSConfig{}))
	r.Use(middleware.SecurityHeaders(config.CSPConfig{Enabled: true, Policy: config.DefaultCSPPolicy}, nil))

	// Register setup routes
	setup.RegisterRoutes(r)

	// Serve embedded frontend if available
	if web.HasEmbeddedFrontend() {
		r.Use(web.ServeEmbeddedFrontend())
	}

	// Setup can test arbitrary database/Redis destinations and create the first
	// administrator, so it is intentionally loopback-only. Use an SSH tunnel for
	// remote administration instead of exposing the bootstrap surface.
	addr := setupServerAddress()
	log.Printf("Setup wizard available at http://%s", addr)
	log.Println("Complete the setup wizard to configure Sub2API")

	server := &http.Server{
		Addr:              addr,
		Handler:           h2c.NewHandler(r, &http2.Server{}),
		ReadHeaderTimeout: 30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("Failed to start setup server: %v", err)
	}
}

func setupServerAddress() string {
	configured := config.GetServerAddress()
	_, port, err := net.SplitHostPort(configured)
	if err != nil || strings.TrimSpace(port) == "" {
		port = "8080"
	}
	return net.JoinHostPort("127.0.0.1", port)
}

func runMainServer() {
	cfg, err := config.LoadForBootstrap()
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}
	if err := logger.Init(logger.OptionsFromConfig(cfg.Log)); err != nil {
		log.Fatalf("Failed to initialize logger: %v", err)
	}
	if cfg.RunMode == config.RunModeSimple {
		log.Println("⚠️  WARNING: Running in SIMPLE mode - billing and quota checks are DISABLED")
	}

	buildInfo := handler.BuildInfo{
		Version:   Version,
		BuildType: BuildType,
	}

	purgedSchedulerCacheSecrets, err := prepareEncryptedSchedulerCacheBeforeWorkers(cfg)
	if err != nil {
		log.Fatalf("Failed scheduler cache security preflight: %v", err)
	}
	if purgedSchedulerCacheSecrets > 0 {
		log.Printf("Removed %d legacy plaintext scheduler cache record(s)", purgedSchedulerCacheSecrets)
	}

	purgedOAuthTokenCacheSecrets, err := prepareEncryptedOAuthTokenCacheBeforeWorkers(cfg)
	if err != nil {
		log.Fatalf("Failed OAuth token cache security preflight: %v", err)
	}
	if purgedOAuthTokenCacheSecrets > 0 {
		log.Printf("Removed %d legacy plaintext OAuth access token cache record(s)", purgedOAuthTokenCacheSecrets)
	}

	migratedAPIKeys, migratedProviderConfigs, migratedDomainSecrets, err := migrateSecuritySecretsBeforeWorkers(cfg)
	if err != nil {
		log.Fatalf("Failed security migration preflight: %v", err)
	}
	if migratedAPIKeys > 0 {
		log.Printf("Encrypted %d legacy plaintext API key(s)", migratedAPIKeys)
	}
	if migratedProviderConfigs > 0 {
		log.Printf("Encrypted %d legacy plaintext payment provider config(s)", migratedProviderConfigs)
	}
	if migratedDomainSecrets != (repository.DomainSecretMigrationResult{}) {
		log.Printf("Domain-bound legacy secrets: accounts=%d totp=%d channel_monitor_keys=%d channel_monitor_payloads=%d backup_s3=%d content_moderation=%d settings=%d proxies=%d",
			migratedDomainSecrets.AccountCredentials,
			migratedDomainSecrets.TOTPSecrets,
			migratedDomainSecrets.ChannelMonitorKeys,
			migratedDomainSecrets.ChannelMonitorPayloads,
			migratedDomainSecrets.BackupS3Configs,
			migratedDomainSecrets.ContentModerationConfigs,
			migratedDomainSecrets.SettingSecrets,
			migratedDomainSecrets.ProxyCredentials,
		)
	}

	app, err := initializeApplication(buildInfo)
	if err != nil {
		log.Fatalf("Failed to initialize application: %v", err)
	}
	defer app.Cleanup()

	// 启动服务器
	go func() {
		if err := app.Server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("Failed to start server: %v", err)
		}
	}()

	log.Printf("Server started on %s", app.Server.Addr)

	// 等待中断信号
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Println("Shutting down server...")

	shutdownTimeout := serverShutdownTimeout()
	log.Printf("Waiting up to %s for in-flight requests to finish", shutdownTimeout)

	ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()

	if err := app.Server.Shutdown(ctx); err != nil {
		log.Fatalf("Server forced to shutdown: %v", err)
	}

	log.Println("Server exited")
}

func prepareEncryptedSchedulerCacheBeforeWorkers(cfg *config.Config) (int64, error) {
	rdb := repository.InitRedis(cfg)
	defer func() { _ = rdb.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), schedulerCachePurgeTimeout)
	defer cancel()
	removed, err := repository.PurgeLegacySchedulerCacheSecrets(ctx, rdb)
	if err != nil {
		return removed, fmt.Errorf("purge legacy plaintext scheduler cache: %w", err)
	}
	if _, err := repository.SeedSchedulerCacheV2Watermark(ctx, rdb); err != nil {
		return removed, fmt.Errorf("seed encrypted scheduler cache watermark: %w", err)
	}
	return removed, nil
}

func prepareEncryptedOAuthTokenCacheBeforeWorkers(cfg *config.Config) (int64, error) {
	rdb := repository.InitRedis(cfg)
	defer func() { _ = rdb.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), oauthTokenCachePurgeTimeout)
	defer cancel()
	removed, err := repository.PurgeLegacyOAuthTokenCacheSecrets(ctx, rdb)
	if err != nil {
		return removed, fmt.Errorf("purge legacy plaintext OAuth token cache: %w", err)
	}
	return removed, nil
}

// migrateSecuritySecretsBeforeWorkers constructs only database-backed migration
// dependencies. The full Wire graph starts background workers in provider
// constructors, so it must not be built until these fail-closed rewrites pass.
func migrateSecuritySecretsBeforeWorkers(cfg *config.Config) (int, int, repository.DomainSecretMigrationResult, error) {
	client, _, err := repository.InitEnt(cfg)
	if err != nil {
		return 0, 0, repository.DomainSecretMigrationResult{}, fmt.Errorf("initialize migration database: %w", err)
	}
	defer func() { _ = client.Close() }()

	db, err := repository.ProvideSQLDB(client)
	if err != nil {
		return 0, 0, repository.DomainSecretMigrationResult{}, err
	}
	protector, err := repository.NewAPIKeyProtector(cfg)
	if err != nil {
		return 0, 0, repository.DomainSecretMigrationResult{}, err
	}
	apiKeyRepo := repository.NewAPIKeyRepository(client, db, protector)
	migrator, ok := apiKeyRepo.(service.APIKeyPlaintextMigrator)
	if !ok {
		return 0, 0, repository.DomainSecretMigrationResult{}, errors.New("API key plaintext migrator is unavailable")
	}

	apiKeyCtx, cancelAPIKeyMigration := context.WithTimeout(context.Background(), apiKeySecurityMigrationTimeout)
	migratedAPIKeys, err := migrator.MigratePlaintextAPIKeysToEncrypted(apiKeyCtx)
	cancelAPIKeyMigration()
	if err != nil {
		return migratedAPIKeys, 0, repository.DomainSecretMigrationResult{}, fmt.Errorf("secure API keys: %w", err)
	}

	secretEncryptor, err := repository.NewAESEncryptor(cfg)
	if err != nil {
		return migratedAPIKeys, 0, repository.DomainSecretMigrationResult{}, err
	}
	domainCtx, cancelDomainMigration := context.WithTimeout(context.Background(), domainSecretMigrationTimeout)
	migratedDomainSecrets, err := repository.MigrateDomainBoundSecrets(domainCtx, client, secretEncryptor)
	cancelDomainMigration()
	if err != nil {
		return migratedAPIKeys, 0, migratedDomainSecrets, fmt.Errorf("bind persistent secrets to domains: %w", err)
	}

	paymentKey, err := payment.ProvideEncryptionKey(cfg)
	if err != nil {
		return migratedAPIKeys, 0, migratedDomainSecrets, err
	}
	legacyPaymentKey, err := payment.ProvideLegacyEncryptionKey(cfg)
	if err != nil {
		return migratedAPIKeys, 0, migratedDomainSecrets, err
	}
	paymentConfig := service.NewPaymentConfigService(client, repository.NewSettingRepository(client, secretEncryptor), []byte(paymentKey))
	paymentCtx, cancelPaymentMigration := context.WithTimeout(context.Background(), paymentConfigMigrationTimeout)
	migratedProviderConfigs, err := paymentConfig.MigrateProviderConfigsToEncrypted(paymentCtx, []byte(legacyPaymentKey))
	cancelPaymentMigration()
	if err != nil {
		return migratedAPIKeys, migratedProviderConfigs, migratedDomainSecrets, fmt.Errorf("secure payment provider configs: %w", err)
	}
	return migratedAPIKeys, migratedProviderConfigs, migratedDomainSecrets, nil
}

func serverShutdownTimeout() time.Duration {
	raw := strings.TrimSpace(os.Getenv("SERVER_SHUTDOWN_TIMEOUT_SECONDS"))
	if raw == "" {
		return defaultServerShutdownTimeout
	}

	seconds, err := strconv.Atoi(raw)
	if err != nil || seconds <= 0 {
		log.Printf("Invalid SERVER_SHUTDOWN_TIMEOUT_SECONDS=%q; using default %s", raw, defaultServerShutdownTimeout)
		return defaultServerShutdownTimeout
	}

	return time.Duration(seconds) * time.Second
}
