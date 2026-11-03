package main

import (
	"context"
	"crypto/subtle"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	images     ImageStore
	adminToken string
}

func newServer(store Store, cache Cache, images ImageStore, adminToken string) *server {
	return &server{store: store, cache: cache, images: images, adminToken: adminToken}
}

func (s *server) routes() http.Handler {
	mux := http.NewServeMux()
	web, _ := fs.Sub(webFiles, "web")
	mux.Handle("GET /", http.FileServerFS(web))

	mux.HandleFunc("GET /api/memes", s.listMemes)
	mux.HandleFunc("GET /api/memes/{id}", s.getMeme)
	mux.HandleFunc("GET /api/countries", s.listCountries)
	mux.HandleFunc("POST /api/memes", s.createMeme)
	mux.HandleFunc("PUT /api/memes/{id}/image", s.uploadImage)
	mux.HandleFunc("GET /images/{key}", s.getImage)

	// liveness: the process is alive. readiness: it can take traffic (the database answers).
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

// cached serves the response from Redis, or on a miss from the database,
// saving the result to the cache. The X-Cache header shows where it came from.
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

const maxImageSize = 5 << 20 // 5 MB

// Allowed picture types and the file extension used in the storage key.
var imageTypes = map[string]string{
	"image/png":  ".png",
	"image/jpeg": ".jpg",
	"image/gif":  ".gif",
	"image/webp": ".webp",
}

var imageKey = regexp.MustCompile(`^[0-9]+-[0-9]+\.(png|jpg|gif|webp)$`)

func (s *server) uploadImage(w http.ResponseWriter, r *http.Request) {
	if !s.isAdmin(r) {
		writeError(w, http.StatusUnauthorized, "admin token required")
		return
	}
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "id must be a positive number")
		return
	}
	if _, err := s.store.Get(r.Context(), id); errors.Is(err, ErrNotFound) {
		writeError(w, http.StatusNotFound, "meme not found")
		return
	} else if err != nil {
		slog.Error("store", "err", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxImageSize))
	if err != nil {
		writeError(w, http.StatusRequestEntityTooLarge, "image must be 5 MB or smaller")
		return
	}
	// The type comes from the file contents, not from the Content-Type header,
	// so a renamed file of another kind is rejected.
	contentType := http.DetectContentType(data)
	ext, ok := imageTypes[contentType]
	if !ok {
		writeError(w, http.StatusUnsupportedMediaType, "image must be PNG, JPEG, GIF or WebP")
		return
	}

	// A new key on every upload: browsers may cache an image forever,
	// and a new picture still shows up right away.
	key := fmt.Sprintf("%d-%d%s", id, time.Now().UnixNano(), ext)
	if err := s.images.Put(r.Context(), key, contentType, data); err != nil {
		if errors.Is(err, ErrNoImages) {
			writeError(w, http.StatusServiceUnavailable, "image storage is not configured")
			return
		}
		slog.Error("image put", "err", err)
		writeError(w, http.StatusBadGateway, "could not save the image")
		return
	}
	if err := s.store.SetImage(r.Context(), id, key); err != nil {
		slog.Error("store", "err", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	s.cache.Invalidate(r.Context())

	m, err := s.store.Get(r.Context(), id)
	if err != nil {
		slog.Error("store", "err", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, m)
}

func (s *server) getImage(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	if !imageKey.MatchString(key) {
		http.NotFound(w, r)
		return
	}
	body, contentType, err := s.images.Get(r.Context(), key)
	if errors.Is(err, ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		slog.Error("image get", "err", err)
		http.Error(w, "could not load the image", http.StatusBadGateway)
		return
	}
	defer body.Close()
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	io.Copy(w, body)
}

func (s *server) isAdmin(r *http.Request) bool {
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	// An empty ADMIN_TOKEN disables writes entirely.
	// ConstantTimeCompare prevents guessing the token from response timing.
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
			return // Kubernetes probes arrive every few seconds; keep them out of the log
		}
		slog.Info("request", "method", r.Method, "path", r.URL.Path,
			"status", rec.code, "duration_ms", time.Since(start).Milliseconds())
	})
}
