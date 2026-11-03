package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"time"
)

// recalc updates meme statuses: a "trend" meme older than TREND_MAX_AGE_YEARS
// (1 by default) becomes "classic". After a change it resets the cache
// so the site shows the new statuses right away.
func recalc(ctx context.Context, store *pgStore, cache Cache) error {
	maxAge, err := strconv.Atoi(env("TREND_MAX_AGE_YEARS", "1"))
	if err != nil || maxAge < 0 {
		return fmt.Errorf("TREND_MAX_AGE_YEARS must be a non-negative number, got %q", os.Getenv("TREND_MAX_AGE_YEARS"))
	}
	cutoff := time.Now().Year() - maxAge

	changed, err := store.DemoteTrends(ctx, cutoff)
	if err != nil {
		return err
	}
	if changed > 0 {
		cache.Invalidate(ctx)
	}
	slog.Info("recalc done", "trend_to_classic", changed, "cutoff_year", cutoff)
	return nil
}

// DemoteTrends turns "trend" memes from cutoffYear or earlier into "classic"
// and returns how many rows changed.
func (s *pgStore) DemoteTrends(ctx context.Context, cutoffYear int) (int64, error) {
	tag, err := s.pool.Exec(ctx,
		"UPDATE memes SET status = 'classic' WHERE status = 'trend' AND year <= $1", cutoffYear)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}
