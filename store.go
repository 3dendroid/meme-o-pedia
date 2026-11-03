package main

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Meme struct {
	ID          int    `json:"id"`
	Title       string `json:"title"`
	Description string `json:"description"`
	CountryCode string `json:"country_code"`
	CountryName string `json:"country_name"`
	Year        int    `json:"year"`
	Status      string `json:"status"`    // trend | classic | outdated
	ImageURL    string `json:"image_url"` // "" when the meme has no picture
}

type Country struct {
	Code  string `json:"code"`
	Name  string `json:"name"`
	Count int    `json:"count"`
}

type Filter struct {
	Country string // country code such as "US"; empty means all
	Query   string // search in title and description
	Oldest  bool   // true means oldest first
}

var ErrNotFound = errors.New("not found")

// Store is everything the API needs from the database. The interface
// lets tests replace the database with a fake.
type Store interface {
	List(ctx context.Context, f Filter) ([]Meme, error)
	Get(ctx context.Context, id int) (Meme, error)
	Countries(ctx context.Context) ([]Country, error)
	Create(ctx context.Context, m Meme) (Meme, error)
	SetImage(ctx context.Context, id int, key string) error
	Ping(ctx context.Context) error
}

type pgStore struct{ pool *pgxpool.Pool }

// connectStore waits up to a minute for the database: in Kubernetes the API
// pod may start before Postgres is ready to accept connections.
func connectStore(ctx context.Context, dsn string) (*pgStore, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, err
	}
	for i := 0; ; i++ {
		err = pool.Ping(ctx)
		if err == nil {
			return &pgStore{pool: pool}, nil
		}
		if i == 30 {
			pool.Close()
			return nil, fmt.Errorf("database is unreachable: %w", err)
		}
		slog.Info("waiting for database", "err", err)
		select {
		case <-ctx.Done():
			pool.Close()
			return nil, ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

func (s *pgStore) Close() { s.pool.Close() }

func (s *pgStore) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }

//go:embed migrations/*.sql
var migrations embed.FS

// Migrate creates the table and fills it with the starter memes.
// Several API replicas may start at the same time, so the work runs under
// an advisory lock: only one replica gets it, the others wait and then
// see that everything is already done.
func (s *pgStore) Migrate(ctx context.Context) error {
	schema, err := migrations.ReadFile("migrations/001_schema.sql")
	if err != nil {
		return err
	}
	seed, err := migrations.ReadFile("migrations/002_seed.sql")
	if err != nil {
		return err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(42)"); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, string(schema)); err != nil {
		return fmt.Errorf("schema: %w", err)
	}
	var n int
	if err := tx.QueryRow(ctx, "SELECT count(*) FROM memes").Scan(&n); err != nil {
		return err
	}
	if n == 0 {
		if _, err := tx.Exec(ctx, string(seed)); err != nil {
			return fmt.Errorf("seed: %w", err)
		}
		slog.Info("database seeded")
	}
	// Files from 003 on run on every start, in name order. Each adds only the memes
	// whose titles are missing, so they also fill databases seeded before the file existed.
	entries, err := migrations.ReadDir("migrations")
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.Name() < "003" {
			continue
		}
		sql, err := migrations.ReadFile("migrations/" + e.Name())
		if err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, string(sql))
		if err != nil {
			return fmt.Errorf("%s: %w", e.Name(), err)
		}
		if tag.RowsAffected() > 0 {
			slog.Info("memes added", "file", e.Name(), "count", tag.RowsAffected())
		}
	}
	return tx.Commit(ctx)
}

// image_url is built from image_key: pictures are served by the API at /images/<key>.
const memeColumns = "id, title, description, country_code, country_name, year, status, " +
	"CASE WHEN image_key = '' THEN '' ELSE '/images/' || image_key END"

func (s *pgStore) List(ctx context.Context, f Filter) ([]Meme, error) {
	order := "year DESC, id DESC"
	if f.Oldest {
		order = "year ASC, id ASC"
	}
	// Parameters are passed separately ($1, $2), so there is no SQL injection.
	rows, err := s.pool.Query(ctx, `
		SELECT `+memeColumns+` FROM memes
		WHERE ($1 = '' OR country_code = $1)
		  AND ($2 = '' OR title ILIKE '%' || $2 || '%' OR description ILIKE '%' || $2 || '%')
		ORDER BY `+order+` LIMIT 2000`, f.Country, f.Query)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[Meme])
}

func (s *pgStore) Get(ctx context.Context, id int) (Meme, error) {
	rows, err := s.pool.Query(ctx, "SELECT "+memeColumns+" FROM memes WHERE id = $1", id)
	if err != nil {
		return Meme{}, err
	}
	m, err := pgx.CollectOneRow(rows, pgx.RowToStructByPos[Meme])
	if errors.Is(err, pgx.ErrNoRows) {
		return Meme{}, ErrNotFound
	}
	return m, err
}

func (s *pgStore) Countries(ctx context.Context) ([]Country, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT country_code, country_name, count(*) FROM memes
		GROUP BY country_code, country_name
		ORDER BY count(*) DESC, country_name`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[Country])
}

func (s *pgStore) SetImage(ctx context.Context, id int, key string) error {
	tag, err := s.pool.Exec(ctx, "UPDATE memes SET image_key = $2 WHERE id = $1", id, key)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

func (s *pgStore) Create(ctx context.Context, m Meme) (Meme, error) {
	err := s.pool.QueryRow(ctx, `
		INSERT INTO memes (title, description, country_code, country_name, year, status)
		VALUES ($1, $2, $3, $4, $5, $6) RETURNING id`,
		m.Title, m.Description, m.CountryCode, m.CountryName, m.Year, m.Status).Scan(&m.ID)
	return m, err
}
