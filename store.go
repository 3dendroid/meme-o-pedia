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
	Status      string `json:"status"` // trend | classic | outdated
}

type Country struct {
	Code  string `json:"code"`
	Name  string `json:"name"`
	Count int    `json:"count"`
}

type Filter struct {
	Country string // код страны, например "US"; пусто — все
	Query   string // поиск по названию и описанию
	Oldest  bool   // true — сначала старые
}

var ErrNotFound = errors.New("not found")

// Store — всё, что API нужно от базы. Интерфейс позволяет
// подменить базу в тестах.
type Store interface {
	List(ctx context.Context, f Filter) ([]Meme, error)
	Get(ctx context.Context, id int) (Meme, error)
	Countries(ctx context.Context) ([]Country, error)
	Create(ctx context.Context, m Meme) (Meme, error)
	Ping(ctx context.Context) error
}

type pgStore struct{ pool *pgxpool.Pool }

// connectStore ждёт базу до минуты: в Kubernetes под API может
// стартовать раньше, чем Postgres готов принимать подключения.
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

// Migrate создаёт таблицу и заполняет её стартовыми мемами.
// Несколько копий API могут стартовать одновременно, поэтому
// работа идёт под advisory-блокировкой: её получит только одна копия,
// остальные подождут и увидят, что всё уже сделано.
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
	return tx.Commit(ctx)
}

const memeColumns = "id, title, description, country_code, country_name, year, status"

func (s *pgStore) List(ctx context.Context, f Filter) ([]Meme, error) {
	order := "year DESC, id DESC"
	if f.Oldest {
		order = "year ASC, id ASC"
	}
	// Параметры передаются отдельно ($1, $2) — так нет SQL-инъекций.
	rows, err := s.pool.Query(ctx, `
		SELECT `+memeColumns+` FROM memes
		WHERE ($1 = '' OR country_code = $1)
		  AND ($2 = '' OR title ILIKE '%' || $2 || '%' OR description ILIKE '%' || $2 || '%')
		ORDER BY `+order+` LIMIT 100`, f.Country, f.Query)
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

func (s *pgStore) Create(ctx context.Context, m Meme) (Meme, error) {
	err := s.pool.QueryRow(ctx, `
		INSERT INTO memes (title, description, country_code, country_name, year, status)
		VALUES ($1, $2, $3, $4, $5, $6) RETURNING id`,
		m.Title, m.Description, m.CountryCode, m.CountryName, m.Year, m.Status).Scan(&m.ID)
	return m, err
}
