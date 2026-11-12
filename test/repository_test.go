package peruri_test

import (
	"context"
	"errors"
	"peruri/db"
	"peruri/internal/apps/docproc"
	"peruri/internal/repositories/callback"
	"strings"
	"testing"
	"time"
)

func TestPostgresCreateRollbackAndRecovery(t *testing.T) {
	r, p := setup(t)
	ctx := context.Background()
	create(t, r, "persist", 1)
	check(t, db.Migrate(ctx, p))
	var count int
	check(t, p.QueryRow(ctx, "SELECT count(*) FROM batches").Scan(&count))
	if count != 1 {
		t.Fatal("migration lost data")
	}
	_, err := r.CreateBatch(ctx, "invalid", []docproc.Document{{DocumentNo: "same", Payload: []byte(`{}`)}, {DocumentNo: "same", Payload: []byte(`{}`)}})
	if !errors.Is(err, docproc.ErrConflict) {
		t.Fatal(err)
	}
	check(t, p.QueryRow(ctx, "SELECT count(*) FROM batches WHERE batch_no=$1", "invalid").Scan(&count))
	if count != 0 {
		t.Fatal("partial batch committed")
	}
	b := create(t, r, "recover", 1)
	id := b.Documents[0].ID
	_, claimed, err := r.Claim(ctx, id)
	check(t, err)
	if !claimed {
		t.Fatal("not claimed")
	}
	n, err := r.Recover(ctx, time.Now().Add(-time.Hour))
	check(t, err)
	if n != 0 {
		t.Fatal("fresh claim recovered")
	}
	_, err = p.Exec(ctx, "UPDATE documents SET locked_at=now()-interval '2 minutes' WHERE id=$1", id)
	check(t, err)
	s := newService(t, p, principalFunc(func(context.Context, docproc.Document) error { return nil }))
	n, err = s.RecoverStaleDocuments(ctx)
	check(t, err)
	if n != 1 {
		t.Fatal(n)
	}
	ds, err := s.ListDocuments(ctx, "recover")
	check(t, err)
	if ds[0].Status != docproc.StatusFailed || ds[0].AttemptCount != 1 {
		t.Fatal(ds)
	}
	ctx, cancel := context.WithCancel(ctx)
	cancel()
	_, _, err = r.Claim(ctx, id)
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestCallbackConflictsExplainDocumentState(t *testing.T) {
	for _, code := range []string{"callback_future_attempt", "callback_requires_processing", "callback_id_reused"} {
		t.Run(code, func(t *testing.T) {
			repo, pool := setup(t)
			callbacks := callback.NewCallbackRepository(pool, newRetryPolicy())
			ctx := context.Background()
			batch := create(t, repo, "explain", 1)
			id := batch.Documents[0].ID
			cb := docproc.CallbackRequest{CallbackID: "event", DocumentID: id, AttemptCount: 1, Status: docproc.StatusSuccess}
			if code != "callback_future_attempt" {
				_, claimed, err := repo.Claim(ctx, id)
				check(t, err)
				if !claimed {
					t.Fatal("claim missing")
				}
			}
			if code == "callback_requires_processing" {
				check(t, repo.SubmitError(ctx, id, 1, "submission rejected"))
				makeRetriesDue(t, pool, batch.BatchNo)
				_, err := repo.RetryFailed(ctx, batch.BatchNo)
				check(t, err)
			}
			if code == "callback_id_reused" {
				check(t, callbacks.SubmitCallback(ctx, cb))
				check(t, callbacks.SubmitCallback(ctx, cb))
				cb.Message = "private-callback-message"
			}
			err := callbacks.SubmitCallback(ctx, cb)
			if !errors.Is(err, docproc.ErrConflict) || !strings.Contains(err.Error(), code) || strings.Contains(err.Error(), cb.Message) && cb.Message != "" {
				t.Fatalf("missing safe conflict reason: %v", err)
			}
			var conflict *docproc.CallbackConflictError
			if !errors.As(err, &conflict) || conflict.Code != code {
				t.Fatal("typed conflict details lost", err)
			}
			wantStatus, wantAttempt, wantEvents := docproc.StatusPending, 0, 0
			if code == docproc.CallbackRequiresProcessing {
				wantAttempt = 1
			} else if code == docproc.CallbackIDReused {
				wantStatus, wantAttempt, wantEvents = docproc.StatusSuccess, 1, 1
				if len(conflict.ConflictingFields) != 1 || conflict.ConflictingFields[0] != "message" {
					t.Fatal("changed field names missing", conflict)
				}
			}
			if conflict.DocumentStatus != wantStatus || conflict.CurrentAttemptCount != wantAttempt {
				t.Fatalf("wrong current state in conflict: %+v", conflict)
			}
			var events int
			check(t, pool.QueryRow(ctx, "SELECT count(*) FROM document_callbacks").Scan(&events))
			if events != wantEvents {
				t.Fatal("rejected callback changed event count", events)
			}
		})
	}
}

func TestRepositoryRejectsInvalidAndConflictingCallbacks(t *testing.T) {
	repo, pool := setup(t)
	callbacks := callback.NewCallbackRepository(pool, newRetryPolicy())
	ctx := context.Background()
	b := create(t, repo, "validation", 2)
	first, second := b.Documents[0].ID, b.Documents[1].ID
	_, claimed, err := repo.Claim(ctx, first)
	check(t, err)
	if !claimed {
		t.Fatal("claim missing")
	}
	cb := docproc.CallbackRequest{CallbackID: "future", DocumentID: first, AttemptCount: 2, Status: docproc.StatusSuccess}
	if err = callbacks.SubmitCallback(ctx, cb); !errors.Is(err, docproc.ErrConflict) {
		t.Fatal("future attempt accepted", err)
	}
	cb.AttemptCount = 1
	for _, bad := range []string{"+" + first, "0" + first, "0", "abc"} {
		cb.DocumentID = bad
		if err = callbacks.SubmitCallback(ctx, cb); !errors.Is(err, docproc.ErrInvalidInput) {
			t.Fatal("invalid ID accepted", bad, err)
		}
	}
	var count int
	check(t, pool.QueryRow(ctx, "SELECT count(*) FROM document_callbacks").Scan(&count))
	if count != 0 {
		t.Fatal("invalid callback persisted", count)
	}
	cb.CallbackID = "shared-event"
	cb.DocumentID = first
	check(t, callbacks.SubmitCallback(ctx, cb))
	check(t, callbacks.SubmitCallback(ctx, cb))
	_, _, err = repo.Claim(ctx, second)
	check(t, err)
	cb.DocumentID = second
	if err = callbacks.SubmitCallback(ctx, cb); !errors.Is(err, docproc.ErrConflict) {
		t.Fatal("cross-document event collision accepted", err)
	}
	check(t, pool.QueryRow(ctx, "SELECT count(*) FROM document_callbacks").Scan(&count))
	if count != 1 {
		t.Fatal("collision persisted", count)
	}
	ds, err := repo.ListDocuments(ctx, "validation")
	check(t, err)
	if ds[0].Status != docproc.StatusSuccess || ds[1].Status != docproc.StatusProcessing {
		t.Fatal(ds)
	}
}

func TestPostgresListsDocumentsInCreationOrder(t *testing.T) {
	repo, _ := setup(t)
	b := create(t, repo, "ordered", 12)
	ds, err := repo.ListDocuments(context.Background(), b.BatchNo)
	check(t, err)
	for i, d := range ds {
		if d.ID != b.Documents[i].ID {
			t.Fatalf("document %d: got id=%s want=%s", i, d.ID, b.Documents[i].ID)
		}
	}
}

func TestPostgresRejectsUnsupportedTextAsInvalidInput(t *testing.T) {
	pool := postgresPool(t)
	s := newService(t, pool, principalFunc(func(context.Context, docproc.Document) error { return nil }))
	ctx := context.Background()
	for _, tc := range []struct {
		name, batchNo, documentNo, payload string
	}{
		{"batch NUL", "bad\x00", "doc", `{}`},
		{"document NUL", "bad", "doc\x00", `{}`},
		{"payload NUL", "bad", "doc", `{"value":"\u0000"}`},
		{"payload key NUL", "bad", "doc", `{"\u0000":1}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := s.CreateBatch(ctx, tc.batchNo, []docproc.Document{{DocumentNo: tc.documentNo, Payload: []byte(tc.payload)}})
			if !errors.Is(err, docproc.ErrInvalidInput) {
				t.Fatal("invalid PostgreSQL input not classified", err)
			}
			var count int
			check(t, pool.QueryRow(ctx, "SELECT count(*) FROM batches").Scan(&count))
			if count != 0 {
				t.Fatal("invalid create partially committed")
			}
		})
	}
	b := create(t, s, "valid", 1)
	check(t, s.ProcessDocument(ctx, b.Documents[0].ID))
	for _, cb := range []docproc.CallbackRequest{
		{CallbackID: "event\x00", DocumentID: b.Documents[0].ID, AttemptCount: 1, Status: docproc.StatusSuccess},
		{CallbackID: "event", DocumentID: b.Documents[0].ID, AttemptCount: 1, Status: docproc.StatusSuccess, Message: "message\x00"},
	} {
		if err := s.HandleCallback(ctx, cb); !errors.Is(err, docproc.ErrInvalidInput) {
			t.Fatal("invalid callback text not classified", err)
		}
	}
	var count int
	check(t, pool.QueryRow(ctx, "SELECT count(*) FROM document_callbacks").Scan(&count))
	if count != 0 {
		t.Fatal("invalid callback committed")
	}
}
