// Command meme-o-pedia — HTTP API и веб-страница энциклопедии мемов.
//
// Все настройки читаются из переменных окружения (см. loadConfig).
// В Kubernetes пароли приходят из Secret, остальное — из Deployment.
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
	// Строку подключения собираем через url.URL, чтобы спецсимволы
	// в пароле (например @ или /) не ломали её.
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
		RedisAddr:   os.Getenv("REDIS_ADDR"), // пусто — работаем без кэша
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

	// Kubernetes присылает SIGTERM перед удалением пода:
	// дорабатываем текущие запросы и выходим.
	<-ctx.Done()
	log.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
}
