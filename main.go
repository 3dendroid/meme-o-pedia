// Command meme-o-pedia is the HTTP API and web page of the meme encyclopedia.
//
// All settings come from environment variables (see loadConfig).
// In Kubernetes, passwords come from a Secret and everything else from the Deployment.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"syscall"
	"time"
)

type config struct {
	Port        string
	DatabaseURL string
	RedisAddr   string
	RedisPass   string
	AdminToken  string
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func loadConfig() (config, error) {
	// Build the connection string with url.URL so that special characters
	// in the password (such as @ or /) don't break it.
	dsn := url.URL{
		Scheme:   "postgres",
		User:     url.UserPassword(env("DB_USER", "memes"), os.Getenv("DB_PASSWORD")),
		Host:     env("DB_HOST", "localhost") + ":" + env("DB_PORT", "5432"),
		Path:     env("DB_NAME", "memes"),
		RawQuery: "sslmode=" + env("DB_SSLMODE", "disable"),
	}
	cfg := config{
		Port:        env("PORT", "8080"),
		DatabaseURL: dsn.String(),
		RedisAddr:   os.Getenv("REDIS_ADDR"), // empty means run without a cache
		RedisPass:   os.Getenv("REDIS_PASSWORD"),
		AdminToken:  os.Getenv("ADMIN_TOKEN"),
	}
	if os.Getenv("DB_PASSWORD") == "" {
		return cfg, errors.New("DB_PASSWORD is not set")
	}
	return cfg, nil
}

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(log)

	cfg, err := loadConfig()
	if err != nil {
		log.Error("config", "err", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	store, err := connectStore(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Error("database", "err", err)
		os.Exit(1)
	}
	defer store.Close()

	if err := store.Migrate(ctx); err != nil {
		log.Error("migrate", "err", err)
		os.Exit(1)
	}

	var cache Cache = noCache{}
	if cfg.RedisAddr != "" {
		cache = newRedisCache(cfg.RedisAddr, cfg.RedisPass)
		log.Info("redis cache enabled", "addr", cfg.RedisAddr)
	}

	srv := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           newServer(store, cache, cfg.AdminToken).routes(),
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		log.Info("listening", "addr", srv.Addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("server", "err", err)
			stop()
		}
	}()

	// Kubernetes sends SIGTERM before deleting a pod:
	// finish the requests in flight and exit.
	<-ctx.Done()
	log.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
}
