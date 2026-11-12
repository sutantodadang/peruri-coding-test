package peruri_test

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

func TestSubmittedQueries(t *testing.T) {
	repo, pool := setup(t)
	ctx := context.Background()
	b := create(t, repo, "queries", 1)
	raw, err := os.ReadFile("../db/queries.sql")
	check(t, err)
	source := strings.ReplaceAll(string(raw), "\r\n", "\n")
	query := func(name string) string {
		t.Helper()
		_, tail, ok := strings.Cut(source, "-- name: "+name+"\n")
		if !ok {
			t.Fatal("query not found", name)
		}
		sql, _, ok := strings.Cut(tail, ";")
		if !ok {
			t.Fatal("query terminator not found", name)
		}
		return sql
	}
	rows, err := pool.Query(ctx, query("ListBatchDocuments"), "queries")
	check(t, err)
	if !rows.Next() {
		t.Fatal("document missing")
	}
	rows.Close()
	rows, err = pool.Query(ctx, query("BatchCounts"))
	check(t, err)
	if !rows.Next() {
		t.Fatal("counts missing")
	}
	values, err := rows.Values()
	check(t, err)
	rows.Close()
	if values[2] != int64(1) || values[3] != int64(1) || values[4] != int64(0) || values[5] != int64(0) || values[6] != int64(0) {
		t.Fatal(values)
	}
	rows, err = pool.Query(ctx, query("UnfinishedBatches"))
	check(t, err)
	if !rows.Next() {
		t.Fatal("pending batch missing")
	}
	rows.Close()
	rows, err = pool.Query(ctx, query("RetryableDocuments"), "queries", time.Now())
	check(t, err)
	if rows.Next() {
		t.Fatal("pending is retryable")
	}
	check(t, rows.Err())
	rows.Close()
	rows, err = pool.Query(ctx, query("ClaimPending"), "queries", 1)
	check(t, err)
	if !rows.Next() {
		t.Fatal("claim missing")
	}
	rows.Close()
	tx, err := pool.Begin(ctx)
	check(t, err)
	defer func() { _ = tx.Rollback(ctx) }()
	var id int64
	var status string
	var attempt int
	check(t, tx.QueryRow(ctx, query("LockCallbackDocument"), b.Documents[0].ID).Scan(&id, &status, &attempt))
	if status != "PROCESSING" || attempt != 1 {
		t.Fatal(status, attempt)
	}
	check(t, tx.QueryRow(ctx, query("ApplyCallback"), id, "success", 1, "SUCCESS", "", nil).Scan(&id, &status, &attempt))
	if status != "SUCCESS" {
		t.Fatal(status)
	}
	check(t, tx.Commit(ctx))
	var inserted int64
	check(t, pool.QueryRow(ctx, query("InsertCallback"), id, "late", 1, "FAILED", "").Scan(&inserted))
	rows, err = pool.Query(ctx, query("InsertCallback"), id, "late", 1, "FAILED", "")
	check(t, err)
	if rows.Next() {
		t.Fatal("duplicate inserted")
	}
	check(t, rows.Err())
	rows.Close()
	rows, err = pool.Query(ctx, query("ReadCallback"), "success")
	check(t, err)
	if !rows.Next() {
		t.Fatal("callback missing")
	}
	rows.Close()
	_, err = pool.Exec(ctx, query("BatchStatusIndex"))
	check(t, err)
	rows, err = pool.Query(ctx, query("UnfinishedBatches"))
	check(t, err)
	if rows.Next() {
		t.Fatal("terminal batch unfinished")
	}
	rows.Close()
}
