package peruri_test

import (
	"context"
	"math"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"peruri/internal/apps/docproc"
	"peruri/internal/repositories/batch"
	"peruri/internal/repositories/callback"
)

func newRetryPolicy() *docproc.RetryPolicy {
	return docproc.NewRetryPolicy(time.Now, func() float64 { return 0 })
}

func TestCallbackBackoffPreservesDeadlineAndAttemptLimit(t *testing.T) {
	pool := postgresPool(t)
	clock := &retryClock{}
	clock.nanos.Store(time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC).UnixNano())
	policy := docproc.NewRetryPolicy(clock.Now, func() float64 { return 0.5 })
	repo := batch.NewBatchRepository(pool, policy)
	callbacks := callback.NewCallbackRepository(pool, policy)
	ctx := context.Background()
	b := create(t, repo, "callback-backoff", 1)
	id := b.Documents[0].ID
	for attempt := 1; attempt <= 3; attempt++ {
		_, claimed, err := repo.Claim(ctx, id)
		check(t, err)
		if !claimed {
			t.Fatal("claim missing", attempt)
		}
		cb := docproc.CallbackRequest{CallbackID: id + "-" + strconv.Itoa(attempt), DocumentID: id, AttemptCount: attempt, Status: docproc.StatusFailed}
		check(t, callbacks.SubmitCallback(ctx, cb))
		ds, err := repo.ListDocuments(ctx, b.BatchNo)
		check(t, err)
		deadline := ds[0].NextRetryAt
		if attempt == 3 {
			if deadline != nil {
				t.Fatal("exhausted attempt scheduled")
			}
			clock.Advance(time.Hour)
			n, err := repo.RetryFailed(ctx, b.BatchNo)
			check(t, err)
			if n != 0 {
				t.Fatal("exhausted attempt requeued")
			}
			break
		}
		want := time.Duration(1500*(1<<(attempt-1))) * time.Millisecond
		if deadline == nil || deadline.Sub(clock.Now()) != want {
			t.Fatal("wrong exponential schedule", deadline)
		}
		clock.Advance(time.Millisecond)
		check(t, callbacks.SubmitCallback(ctx, cb))
		cb.CallbackID += "-late"
		check(t, callbacks.SubmitCallback(ctx, cb))
		check(t, repo.SubmitError(ctx, id, attempt, "late transport failure"))
		ds, err = repo.ListDocuments(ctx, b.BatchNo)
		check(t, err)
		if ds[0].NextRetryAt == nil || !ds[0].NextRetryAt.Equal(*deadline) {
			t.Fatal("duplicate/late failure postponed retry")
		}
		clock.Advance(want - time.Millisecond)
		var total atomic.Int32
		var wg sync.WaitGroup
		for range 12 {
			wg.Go(func() {
				n, err := repo.RetryFailed(ctx, b.BatchNo)
				check(t, err)
				total.Add(int32(n))
			})
		}
		wg.Wait()
		if total.Load() != 1 {
			t.Fatal("concurrent retry requeued more than once", total.Load())
		}
	}
}

func TestRecoverySchedulesIndependentJitterAndSuccessClearsIt(t *testing.T) {
	pool := postgresPool(t)
	now := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	var samples atomic.Int32
	policy := docproc.NewRetryPolicy(func() time.Time { return now }, func() float64 { return float64(samples.Add(1)%3) / 3 })
	repo := batch.NewBatchRepository(pool, policy)
	callbacks := callback.NewCallbackRepository(pool, policy)
	ctx := context.Background()
	b := create(t, repo, "recovery-jitter", 2)
	for _, d := range b.Documents {
		_, _, err := repo.Claim(ctx, d.ID)
		check(t, err)
	}
	n, err := repo.Recover(ctx, now)
	check(t, err)
	if n != 2 {
		t.Fatal("missing recovery", n)
	}
	ds, err := repo.ListDocuments(ctx, b.BatchNo)
	check(t, err)
	if ds[0].NextRetryAt == nil || ds[1].NextRetryAt == nil || ds[0].NextRetryAt.Equal(*ds[1].NextRetryAt) {
		t.Fatal("recovery jitter not independent", ds)
	}
	for _, d := range ds {
		delay := d.NextRetryAt.Sub(now)
		if delay < time.Second || delay >= 2*time.Second {
			t.Fatal("recovery outside backoff window", delay)
		}
		check(t, callbacks.SubmitCallback(ctx, docproc.CallbackRequest{CallbackID: d.ID, DocumentID: d.ID, AttemptCount: 1, Status: docproc.StatusSuccess}))
	}
	ds, err = repo.ListDocuments(ctx, b.BatchNo)
	check(t, err)
	for _, d := range ds {
		if d.Status != docproc.StatusSuccess || d.NextRetryAt != nil {
			t.Fatal("SUCCESS retained retry schedule", d)
		}
	}
}

func TestRecoveryBackoffIsBoundedAndSkipsLockedRows(t *testing.T) {
	repo, pool := setup(t)
	ctx := context.Background()
	b := create(t, repo, "bounded-recovery", 102)
	_, err := pool.Exec(ctx, "UPDATE documents SET status='PROCESSING',attempt_count=1,locked_at=now()-interval '2 minutes' WHERE batch_id=$1", b.ID)
	check(t, err)
	// Exhausted leases recover to terminal FAILED without a retry schedule.
	_, err = pool.Exec(ctx, "UPDATE documents SET attempt_count=3 WHERE id=$1", b.Documents[1].ID)
	check(t, err)
	tx, err := pool.Begin(ctx)
	check(t, err)
	defer func() { _ = tx.Rollback(ctx) }()
	var lockedID string
	check(t, tx.QueryRow(ctx, "SELECT id::text FROM documents WHERE id=$1 FOR UPDATE", b.Documents[0].ID).Scan(&lockedID))
	for _, want := range []int{100, 1, 0} {
		bounded, cancel := context.WithTimeout(ctx, 5*time.Second)
		n, err := repo.Recover(bounded, time.Now().Add(-time.Minute))
		cancel()
		check(t, err)
		if n != want {
			t.Fatal("unexpected recovery chunk", n, want)
		}
	}
	check(t, tx.Rollback(ctx))
	n, err := repo.Recover(ctx, time.Now().Add(-time.Minute))
	check(t, err)
	if n != 1 {
		t.Fatal("unlocked row not recovered", n)
	}
	ds, err := repo.ListDocuments(ctx, b.BatchNo)
	check(t, err)
	for _, d := range ds {
		if d.Status != docproc.StatusFailed || (d.ID == b.Documents[1].ID) != (d.NextRetryAt == nil) {
			t.Fatal("invalid recovery schedule", d)
		}
	}
}

func makeRetriesDue(t testing.TB, pool *pgxpool.Pool, no string) {
	t.Helper()
	_, err := pool.Exec(context.Background(), "UPDATE documents SET next_retry_at=$2 WHERE batch_id=(SELECT id FROM batches WHERE batch_no=$1) AND status='FAILED' AND attempt_count<max_attempts", no, time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC))
	check(t, err)
}

type retryClock struct{ nanos atomic.Int64 }

func (clock *retryClock) Now() time.Time              { return time.Unix(0, clock.nanos.Load()).UTC() }
func (clock *retryClock) Advance(delay time.Duration) { clock.nanos.Add(int64(delay)) }

func TestExponentialRetryPolicy(t *testing.T) {
	now := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, fraction := range []float64{0, 0.5, math.Nextafter(1, 0)} {
		policy := docproc.NewRetryPolicy(func() time.Time { return now }, func() float64 { return fraction })
		for _, attempt := range []int{1, 2} {
			next := policy.Next(attempt)
			window := time.Second << attempt
			want := window/2 + time.Duration(fraction*float64(window/2))
			if next == nil || next.Sub(now) != want || next.Sub(now) < window/2 || next.Sub(now) >= window {
				t.Fatalf("attempt=%d fraction=%f next=%v", attempt, fraction, next)
			}
		}
		for _, attempt := range []int{-1, 0, 3, 4, math.MaxInt} {
			if policy.Next(attempt) != nil {
				t.Fatalf("scheduled invalid/exhausted attempt %d", attempt)
			}
		}
	}
}

func TestRetryBackoffBlocksEarlyRequeue(t *testing.T) {
	pool := postgresPool(t)
	clock := &retryClock{}
	clock.nanos.Store(time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC).UnixNano())
	policy := docproc.NewRetryPolicy(clock.Now, func() float64 { return 0.5 })
	repo := batch.NewBatchRepository(pool, policy)
	ctx := context.Background()
	created := create(t, repo, "backoff", 1)
	id := created.Documents[0].ID
	_, claimed, err := repo.Claim(ctx, id)
	check(t, err)
	if !claimed {
		t.Fatal("claim missing")
	}
	check(t, repo.SubmitError(ctx, id, 1, "rejected"))
	n, err := repo.RetryFailed(ctx, created.BatchNo)
	check(t, err)
	if n != 0 {
		t.Fatalf("retry bypassed backoff: got %d, want 0 before deadline", n)
	}
	documents, err := repo.ListDocuments(ctx, created.BatchNo)
	check(t, err)
	if documents[0].NextRetryAt == nil || documents[0].NextRetryAt.Sub(clock.Now()) != 1500*time.Millisecond {
		t.Fatalf("first retry schedule not persisted: %+v", documents[0])
	}
	// A fresh repository sees the persisted schedule after a restart.
	repo = batch.NewBatchRepository(pool, policy)
	clock.Advance(1500*time.Millisecond - time.Microsecond)
	n, err = repo.RetryFailed(ctx, created.BatchNo)
	check(t, err)
	if n != 0 {
		t.Fatal("retried before exact deadline")
	}
	clock.Advance(time.Microsecond)
	n, err = repo.RetryFailed(ctx, created.BatchNo)
	check(t, err)
	if n != 1 {
		t.Fatal("exact deadline did not make retry eligible", n)
	}
	documents, err = repo.ListDocuments(ctx, created.BatchNo)
	check(t, err)
	if documents[0].Status != docproc.StatusPending || documents[0].NextRetryAt != nil || documents[0].AttemptCount != 1 {
		t.Fatalf("requeue changed attempt or retained schedule: %+v", documents[0])
	}
}
