package db

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin migration: %w", err)
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = tx.Rollback(cleanup)
	}()
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(7219441001)"); err != nil {
		return fmt.Errorf("lock migration: %w", err)
	}
	if _, err = tx.Exec(ctx, "CREATE TABLE IF NOT EXISTS schema_migrations(version INT PRIMARY KEY)"); err != nil {
		return fmt.Errorf("migration version table: %w", err)
	}
	var installed bool
	if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version=1)").Scan(&installed); err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}
	if !installed {
		if _, err = tx.Exec(ctx, Schema); err != nil {
			return fmt.Errorf("apply schema: %w", err)
		}
		if _, err = tx.Exec(ctx, "INSERT INTO schema_migrations(version) VALUES(1)"); err != nil {
			return fmt.Errorf("save schema version: %w", err)
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit migration: %w", err)
	}
	return nil
}
