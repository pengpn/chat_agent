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
	"github.com/example/agent-platform/backend/internal/httpapi"
	"github.com/example/agent-platform/backend/internal/logger"
	"github.com/example/agent-platform/backend/internal/provider"
	"github.com/example/agent-platform/backend/internal/repository"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		_, _ = os.Stderr.WriteString("invalid configuration: " + err.Error() + "\n")
		os.Exit(1)
	}
	if err := logger.Init(cfg.LogLevel, os.Stdout); err != nil {
		_, _ = os.Stderr.WriteString("invalid LOG_LEVEL: " + err.Error() + "\n")
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

	jwtManager := auth.NewJWTManager(cfg.JWTSecret, cfg.JWTTTL)
	providers := provider.NewRegistry(cfg.Providers)
	server := httpapi.NewServer(repositories, jwtManager, providers, cfg)

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
