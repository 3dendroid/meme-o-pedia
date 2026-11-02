package main

import (
	"context"
	"crypto/subtle"
	"embed"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

//go:embed web
var webFiles embed.FS

type server struct {
	store      Store
	cache      Cache
	adminToken string
}

func newServer(store Store, cache Cache, adminToken string) *server {
	return &server{store: store, cache: cache, adminToken: adminToken}
}

func (s *server) routes() http.Handler {
	mux := http.NewServeMux()
	web, _ := fs.Sub(webFiles, "web")
	mux.Handle("GET /", http.FileServerFS(web))

	mux.HandleFunc("GET /api/memes", s.listMemes)
	mux.HandleFunc("GET /api/memes/{id}", s.getMeme)
	mux.HandleFunc("GET /api/countries", s.listCountries)
	mux.HandleFunc("POST /api/memes", s.createMeme)

	// liveness: процесс жив. readiness: можно слать трафик (база отвечает).
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	})
	mux.HandleFunc("GET /readyz", s.ready)
	return logRequests(mux)
}

func (s *server) ready(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := s.store.Ping(ctx); err != nil {
		http.Error(w, "database unavailable", http.StatusServiceUnavailable)
		return
	}
	w.Write([]byte("ok"))
}

var countryCode = regexp.MustCompile(`^[A-Z]{2}$`)

func (s *server) listMemes(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := Filter{
		Country: strings.ToUpper(q.Get("country")),
		Query:   strings.TrimSpace(q.Get("q")),
		Oldest:  q.Get("sort") == "old",
	}
	if f.Country != "" && !countryCode.MatchString(f.Country) {
		writeError(w, http.StatusBadRequest, "country must be a two-letter code, for example US")
		return
	}
	if len(f.Query) > 100 {
		writeError(w, http.StatusBadRequest, "search query is too long")
		return
	}
	key := "list:" + f.Country + ":" + strconv.FormatBool(f.Oldest) + ":" + strings.ToLower(f.Query)
	s.cached(w, r, key, func(ctx context.Context) (any, error) {
		memes, err := s.store.List(ctx, f)
		if memes == nil {
			memes = []Meme{}
		}
		return memes, err
	})
}

func (s *server) listCountries(w http.ResponseWriter, r *http.Request) {
	s.cached(w, r, "countries", func(ctx context.Context) (any, error) {
		return s.store.Countries(ctx)
	})
}

// cached отдаёт ответ из Redis, а при промахе — из базы, сохраняя
// результат в кэш. Заголовок X-Cache показывает, откуда пришёл ответ.
func (s *server) cached(w http.ResponseWriter, r *http.Request, key string, load func(context.Context) (any, error)) {
	if body, ok := s.cache.Get(r.Context(), key); ok {
		w.Header().Set("X-Cache", "HIT")
		writeRaw(w, http.StatusOK, body)
		return
	}
	v, err := load(r.Context())
	if err != nil {
		slog.Error("store", "err", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	body, _ := json.Marshal(v)
	s.cache.Set(r.Context(), key, body)
	w.Header().Set("X-Cache", "MISS")
	writeRaw(w, http.StatusOK, body)
}

func (s *server) getMeme(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "id must be a positive number")
		return
	}
	m, err := s.store.Get(r.Context(), id)
	if errors.Is(err, ErrNotFound) {
		writeError(w, http.StatusNotFound, "meme not found")
		return
	}
	if err != nil {
		slog.Error("store", "err", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, m)
}

func (s *server) createMeme(w http.ResponseWriter, r *http.Request) {
	if !s.isAdmin(r) {
		writeError(w, http.StatusUnauthorized, "admin token required")
		return
	}
	var m Meme
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&m); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	m.CountryCode = strings.ToUpper(m.CountryCode)
	if msg := validate(m); msg != "" {
		writeError(w, http.StatusBadRequest, msg)
		return
	}
	m, err := s.store.Create(r.Context(), m)
	if err != nil {
		slog.Error("store", "err", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	s.cache.Invalidate(r.Context())
	writeJSON(w, http.StatusCreated, m)
}

func (s *server) isAdmin(r *http.Request) bool {
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	// Пустой ADMIN_TOKEN отключает запись совсем.
	// ConstantTimeCompare не даёт подобрать токен по времени ответа.
	return ok && s.adminToken != "" &&
		subtle.ConstantTimeCompare([]byte(token), []byte(s.adminToken)) == 1
}

func validate(m Meme) string {
	switch {
	case strings.TrimSpace(m.Title) == "":
		return "title is required"
	case !countryCode.MatchString(m.CountryCode):
		return "country_code must be a two-letter code, for example US"
	case strings.TrimSpace(m.CountryName) == "":
		return "country_name is required"
	case m.Year < 1990 || m.Year > 2100:
		return "year must be between 1990 and 2100"
	case m.Status != "trend" && m.Status != "classic" && m.Status != "outdated":
		return "status must be trend, classic or outdated"
	}
	return ""
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	body, _ := json.Marshal(v)
	writeRaw(w, code, body)
}

func writeRaw(w http.ResponseWriter, code int, body []byte) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	w.Write(body)
}

func writeError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

type statusRecorder struct {
	http.ResponseWriter
	code int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.code = code
	r.ResponseWriter.WriteHeader(code)
}

func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, code: http.StatusOK}
		next.ServeHTTP(rec, r)
		if r.URL.Path == "/healthz" || r.URL.Path == "/readyz" {
			return // пробы Kubernetes приходят каждые несколько секунд — не засоряем лог
		}
		slog.Info("request", "method", r.Method, "path", r.URL.Path,
			"status", rec.code, "duration_ms", time.Since(start).Milliseconds())
	})
}
