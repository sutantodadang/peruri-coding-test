package peruri_test

import (
	"context"
	"testing"

	"peruri/db"
)

func TestSchemaInitializationRollsBackOnFailure(t *testing.T) {
	pool := postgresSchema(t)
	ctx := context.Background()
	// Force a late DDL failure to verify the schema is installed atomically.
	_, err := pool.Exec(ctx, "CREATE TABLE documents_due_retry_idx(id INT)")
	check(t, err)
	if err = db.Migrate(ctx, pool); err == nil {
		t.Fatal("expected schema initialization failure")
	}
	var tables int
	check(t, pool.QueryRow(ctx, "SELECT count(*) FROM information_schema.tables WHERE table_schema=current_schema() AND table_name IN ('batches','documents','document_callbacks','schema_migrations')").Scan(&tables))
	if tables != 0 {
		t.Fatal("partial schema committed", tables)
	}
	_, err = pool.Exec(ctx, "DROP TABLE documents_due_retry_idx")
	check(t, err)
	check(t, db.Migrate(ctx, pool))
	check(t, db.Migrate(ctx, pool))
	var versions int
	check(t, pool.QueryRow(ctx, "SELECT count(*) FROM schema_migrations").Scan(&versions))
	if versions != 1 {
		t.Fatal("schema initialization not idempotent", versions)
	}
}

func TestSchemaDefinesRetrySchedule(t *testing.T) {
	pool := postgresSchema(t)
	ctx := context.Background()
	// schema.sql alone must define every table, constraint, and index the API needs.
	_, err := pool.Exec(ctx, db.Schema)
	check(t, err)
	_, err = pool.Exec(ctx, "INSERT INTO batches(batch_no,total_documents) VALUES('schema',1); INSERT INTO documents(batch_id,document_no,payload) SELECT id,'doc','{}' FROM batches")
	check(t, err)
	for _, statement := range []string{
		"UPDATE documents SET next_retry_at=now()",
		"UPDATE documents SET status='FAILED',attempt_count=1",
		"UPDATE documents SET status='FAILED',attempt_count=3,next_retry_at=now()",
	} {
		if _, err = pool.Exec(ctx, statement); err == nil {
			t.Fatal("invalid retry schedule accepted", statement)
		}
	}
	_, err = pool.Exec(ctx, "UPDATE documents SET status='FAILED',attempt_count=1,next_retry_at=now()")
	check(t, err)
	var indexes int
	check(t, pool.QueryRow(ctx, "SELECT count(*) FROM pg_indexes WHERE schemaname=current_schema() AND indexname='documents_due_retry_idx'").Scan(&indexes))
	if indexes != 1 {
		t.Fatal("retry index missing")
	}
}
