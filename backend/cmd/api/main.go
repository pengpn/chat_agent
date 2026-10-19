package main

import (
	"context"
	"errors"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/example/agent-platform/backend/internal/auth"
	"github.com/example/agent-platform/backend/internal/config"
	"github.com/example/agent-platform/backend/internal/database"
	"github.com/example/agent-platform/backend/internal/embeddinggateway"
	"github.com/example/agent-platform/backend/internal/httpapi"
	"github.com/example/agent-platform/backend/internal/llmgateway"
	"github.com/example/agent-platform/backend/internal/logger"
	"github.com/example/agent-platform/backend/internal/mailer"
	"github.com/example/agent-platform/backend/internal/objectstore"
	"github.com/example/agent-platform/backend/internal/provider"
	"github.com/example/agent-platform/backend/internal/ratelimit"
	"github.com/example/agent-platform/backend/internal/repository"
	"github.com/example/agent-platform/backend/internal/rerankgateway"
	"github.com/redis/go-redis/v9"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		_, _ = os.Stderr.WriteString("invalid configuration: " + err.Error() + "\n")
		os.Exit(1)
	}
	if err := logger.Init(cfg.LogLevel, os.Stdout, logger.FileConfig{Directory: cfg.LogDir, RetentionDays: cfg.LogRetentionDays}); err != nil {
		_, _ = os.Stderr.WriteString("invalid log configuration: " + err.Error() + "\n")
		os.Exit(1)
	}
	defer logger.Sync()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	db, sqlDB, err := database.Open(ctx, database.Config{
		URL:             cfg.DatabaseURL,
		MaxOpenConns:    cfg.DatabaseMaxOpenConns,
		MaxIdleConns:    cfg.DatabaseMaxIdleConns,
		ConnMaxLifetime: cfg.DatabaseConnMaxLifetime,
		ConnMaxIdleTime: cfg.DatabaseConnMaxIdleTime,
	})
	if err != nil {
		logger.Error(context.Background(), "database connection failed", logger.Err(err))
		os.Exit(1)
	}
	defer sqlDB.Close()

	if err := repository.Migrate(ctx, db); err != nil {
		logger.Error(context.Background(), "database migration failed", logger.Err(err))
		os.Exit(1)
	}
	repositories := repository.New(db)
	if err := repositories.Auth.CleanupExpiredCredentials(ctx, time.Now(), time.Now().AddDate(0, 0, -cfg.AuthCredentialRetentionDays)); err != nil {
		logger.Error(context.Background(), "expired auth credential cleanup failed", logger.Err(err))
	}
	if err := repositories.Auth.CleanupAuditLogs(ctx, time.Now().AddDate(0, 0, -cfg.AccountAuditRetentionDays)); err != nil {
		logger.Error(context.Background(), "account audit cleanup failed", logger.Err(err))
	}

	jwtManager := auth.NewJWTManager(cfg.JWTSecret, cfg.AccessTokenTTL)
	providers := provider.NewRegistry(cfg.Providers)
	store, err := objectstore.NewMinIO(objectstore.Config{
		Endpoint: cfg.ObjectStorageEndpoint, AccessKey: cfg.ObjectStorageAccessKey, SecretKey: cfg.ObjectStorageSecretKey,
		Bucket: cfg.ObjectStorageBucket, Region: cfg.ObjectStorageRegion, UseSSL: cfg.ObjectStorageUseSSL,
	})
	if err != nil {
		logger.Error(context.Background(), "object storage configuration failed", logger.Err(err))
		os.Exit(1)
	}
	storageCtx, cancelStorage := context.WithTimeout(context.Background(), 10*time.Second)
	err = store.EnsureBucket(storageCtx)
	cancelStorage()
	if err != nil {
		logger.Error(context.Background(), "object storage initialization failed", logger.Err(err))
		os.Exit(1)
	}
	redisOptions, err := redis.ParseURL(cfg.RedisURL)
	if err != nil {
		logger.Error(context.Background(), "invalid Redis URL", logger.Err(err))
		os.Exit(1)
	}
	redisClient := redis.NewClient(redisOptions)
	redisCtx, cancelRedis := context.WithTimeout(context.Background(), 5*time.Second)
	err = redisClient.Ping(redisCtx).Err()
	cancelRedis()
	if err != nil {
		_ = redisClient.Close()
		logger.Error(context.Background(), "redis connection failed", logger.Err(err))
		os.Exit(1)
	}
	defer redisClient.Close()
	embeddings, err := embeddinggateway.NewConfigured(cfg.Embedding, redisClient)
	if err != nil {
		logger.Error(context.Background(), "embedding gateway configuration failed", logger.Err(err))
		os.Exit(1)
	}
	reranker, err := rerankgateway.NewConfigured(cfg.Rerank)
	if err != nil {
		logger.Error(context.Background(), "rerank gateway configuration failed", logger.Err(err))
		os.Exit(1)
	}
	var limiter llmgateway.RateLimiter
	if cfg.LLMRateTokensPerMinute > 0 {
		limiter = llmgateway.NewTokenBucketLimiter(redisClient, cfg.LLMRateTokensPerMinute, cfg.LLMRateBurst, "")
	}

	var emailSender mailer.Sender
	switch cfg.EmailDeliveryMode {
	case "smtp":
		emailSender, err = mailer.NewSMTP(mailer.SMTPConfig{
			Host: cfg.SMTPHost, Port: cfg.SMTPPort, Username: cfg.SMTPUsername,
			Password: cfg.SMTPPassword, From: cfg.SMTPFrom,
		})
		if err != nil {
			logger.Error(context.Background(), "email configuration failed", logger.Err(err))
			os.Exit(1)
		}
	case "disabled":
		emailSender = mailer.NoopSender{}
	default:
		emailSender = mailer.LogSender{}
	}
	server := httpapi.NewServerWithRAGGateways(repositories, jwtManager, providers, limiter, ratelimit.NewRedis(redisClient, ""), store, emailSender, embeddings, reranker, cfg)
	cleanupCtx, cancelCleanup := context.WithTimeout(context.Background(), 10*time.Second)
	server.CleanupKnowledgeObjects(cleanupCtx)
	cancelCleanup()

	httpServer := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           server.Router(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		logger.Info(context.Background(), "api server starting", logger.String("address", cfg.HTTPAddr), logger.Strings("providers", providers.IDs()))
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error(context.Background(), "api server failed", logger.Err(err))
			os.Exit(1)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop

	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelShutdown()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		logger.Error(context.Background(), "graceful shutdown failed", logger.Err(err))
	}
}
