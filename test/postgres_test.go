package peruri_test

import (
	"context"
	"crypto/rand"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"peruri/db"
	"peruri/internal/apps/docproc"
	"peruri/internal/repositories/batch"
)

type batchCreator interface {
	CreateBatch(context.Context, string, []docproc.Document) (*docproc.Batch, error)
}

// Every database test gets its own schema on real PostgreSQL; demo data is untouched.
func postgresPool(t testing.TB) *pgxpool.Pool {
	t.Helper()
	pool := postgresSchema(t)
	check(t, db.Migrate(context.Background(), pool))
	return pool
}

func postgresSchema(t testing.TB) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Fatal("TEST_DATABASE_URL is required; start PostgreSQL and run scripts/test.ps1")
	}
	config, err := pgxpool.ParseConfig(dsn)
	check(t, err)
	config.ConnConfig.ConnectTimeout = 5 * time.Second
	config.MaxConns = 6
	admin, err := pgxpool.NewWithConfig(context.Background(), config)
	check(t, err)
	quoted := pgx.Identifier{"test_" + rand.Text()}.Sanitize()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err = admin.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	config = config.Copy()
	config.ConnConfig.RuntimeParams["search_path"] = quoted
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		_, _ = admin.Exec(ctx, "DROP SCHEMA "+quoted+" CASCADE")
		admin.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Close()
		cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		_, err := admin.Exec(cleanup, "DROP SCHEMA "+quoted+" CASCADE")
		if err != nil {
			t.Error(err)
		}
		admin.Close()
	})
	return pool
}

func setup(t testing.TB) (*batch.BatchRepository, *pgxpool.Pool) {
	t.Helper()
	pool := postgresPool(t)
	return batch.NewBatchRepository(pool, newRetryPolicy()), pool
}
