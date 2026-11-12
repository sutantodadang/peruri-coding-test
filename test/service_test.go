package peruri_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"peruri/internal/apps/docproc"
	"peruri/internal/apps/docproc/service"
	"peruri/internal/apps/principal"
	"peruri/internal/repositories/batch"
	"peruri/internal/repositories/callback"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type principalFunc func(context.Context, docproc.Document) error

func (f principalFunc) Submit(ctx context.Context, d docproc.Document) error { return f(ctx, d) }
func docs(n int) []docproc.Document {
	ds := make([]docproc.Document, n)
	for i := range ds {
		ds[i] = docproc.Document{DocumentNo: fmt.Sprint(i), Payload: json.RawMessage(`{"value":1}`)}
	}
	return ds
}
func create(t testing.TB, s batchCreator, no string, n int) *docproc.Batch {
	t.Helper()
	b, e := s.CreateBatch(context.Background(), no, docs(n))
	if e != nil {
		t.Fatal(e)
	}
	return b
}
func check(t testing.TB, e error) {
	t.Helper()
	if e != nil {
		t.Fatal(e)
	}
}
func wait(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(10 * time.Second):
		t.Fatal("barrier timed out")
	}
}

func TestGlobalConcurrencyAcrossBatches(t *testing.T) {
	var inflight, max, calls atomic.Int32
	entered := make(chan struct{}, 30)
	release := make(chan struct{})
	s := newService(t, postgresPool(t), principalFunc(func(ctx context.Context, d docproc.Document) error {
		calls.Add(1)
		n := inflight.Add(1)
		defer inflight.Add(-1)
		for old := max.Load(); n > old; old = max.Load() {
			if max.CompareAndSwap(old, n) {
				break
			}
		}
		entered <- struct{}{}
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}))
	create(t, s, "a", 12)
	create(t, s, "b", 12)
	results := make(chan error, 2)
	for _, no := range []string{"a", "b"} {
		go func() { results <- s.ProcessBatch(context.Background(), no) }()
	}
	for range 3 {
		wait(t, entered)
	}
	close(release)
	for range 2 {
		check(t, <-results)
	}
	if max.Load() != 3 || calls.Load() != 24 {
		t.Fatalf("max=%d calls=%d", max.Load(), calls.Load())
	}
}

func TestRetryStopsAtThreeAttempts(t *testing.T) {
	var calls atomic.Int32
	pool := postgresPool(t)
	s := newService(t, pool, principalFunc(func(context.Context, docproc.Document) error { calls.Add(1); return errors.New("rejected") }))
	b := create(t, s, "retry", 2)
	for attempt := 1; attempt <= 4; attempt++ {
		err := s.ProcessBatch(context.Background(), b.BatchNo)
		if attempt <= 3 && err == nil {
			t.Fatal("missing Submit error")
		}
		makeRetriesDue(t, pool, b.BatchNo)
		n, e := s.RetryFailed(context.Background(), b.BatchNo)
		check(t, e)
		want := 2
		if attempt >= 3 {
			want = 0
		}
		if n != want {
			t.Fatalf("retry=%d want=%d", n, want)
		}
	}
	if calls.Load() != 6 {
		t.Fatalf("calls=%d", calls.Load())
	}
	st, e := s.GetBatchStatus(context.Background(), b.BatchNo)
	check(t, e)
	if !st.Done || st.Status != "COMPLETED" || st.Counts[docproc.StatusFailed] != 2 {
		t.Fatalf("%+v", st)
	}
}

func TestOneClaimPerAttempt(t *testing.T) {
	var calls atomic.Int32
	s := newService(t, postgresPool(t), principalFunc(func(context.Context, docproc.Document) error { calls.Add(1); return nil }))
	b := create(t, s, "claim", 1)
	var wg sync.WaitGroup
	for range 50 {
		wg.Go(func() { check(t, s.ProcessDocument(context.Background(), b.Documents[0].ID)) })
	}
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("calls=%d", calls.Load())
	}
}

func TestCallbacksAreAtomicAndSuccessTerminal(t *testing.T) {
	var calls atomic.Int32
	s := newService(t, postgresPool(t), principalFunc(func(context.Context, docproc.Document) error {
		calls.Add(1)
		return errors.New("transport lost after accept")
	}))
	b := create(t, s, "callback", 1)
	_ = s.ProcessBatch(context.Background(), b.BatchNo)
	cb := docproc.CallbackRequest{CallbackID: "event", DocumentID: b.Documents[0].ID, AttemptCount: 1, Status: docproc.StatusSuccess}
	var wg sync.WaitGroup
	for range 30 {
		wg.Go(func() { check(t, s.HandleCallback(context.Background(), cb)) })
	}
	wg.Wait()
	check(t, s.HandleCallback(context.Background(), cb))
	cb.CallbackID = "late-failed"
	cb.Status = docproc.StatusFailed
	check(t, s.HandleCallback(context.Background(), cb))
	st, e := s.GetBatchStatus(context.Background(), b.BatchNo)
	check(t, e)
	if !st.Done || st.Counts[docproc.StatusSuccess] != 1 {
		t.Fatalf("%+v", st)
	}
	events, e := s.ListCallbacks(context.Background(), b.Documents[0].ID)
	check(t, e)
	if len(events) != 2 {
		t.Fatalf("events=%d", len(events))
	}
	check(t, s.ProcessBatch(context.Background(), b.BatchNo))
	n, e := s.RetryFailed(context.Background(), b.BatchNo)
	check(t, e)
	if calls.Load() != 1 || n != 0 {
		t.Fatal("SUCCESS reprocessed")
	}
	cb.Status = docproc.StatusSuccess
	if !errors.Is(s.HandleCallback(context.Background(), cb), docproc.ErrConflict) {
		t.Fatal("conflicting event accepted")
	}
}

func TestOldCallbackCannotFailNewAttempt(t *testing.T) {
	pool := postgresPool(t)
	s := newService(t, pool, principalFunc(func(context.Context, docproc.Document) error { return nil }))
	b := create(t, s, "fence", 1)
	id := b.Documents[0].ID
	check(t, s.ProcessDocument(context.Background(), id))
	check(t, s.HandleCallback(context.Background(), docproc.CallbackRequest{CallbackID: "f1", DocumentID: id, AttemptCount: 1, Status: docproc.StatusFailed}))
	makeRetriesDue(t, pool, b.BatchNo)
	_, e := s.RetryFailed(context.Background(), b.BatchNo)
	check(t, e)
	check(t, s.ProcessDocument(context.Background(), id))
	check(t, s.HandleCallback(context.Background(), docproc.CallbackRequest{CallbackID: "old", DocumentID: id, AttemptCount: 1, Status: docproc.StatusFailed}))
	ds, e := s.ListDocuments(context.Background(), b.BatchNo)
	check(t, e)
	if ds[0].Status != docproc.StatusProcessing || ds[0].AttemptCount != 2 {
		t.Fatalf("%+v", ds[0])
	}
}

func TestEarlierSuccessResolvesUncertainRetriedRequest(t *testing.T) {
	pool := postgresPool(t)
	s := newService(t, pool, principalFunc(func(context.Context, docproc.Document) error { return errors.New("response lost") }))
	b := create(t, s, "uncertain", 1)
	id := b.Documents[0].ID
	_ = s.ProcessDocument(context.Background(), id)
	makeRetriesDue(t, pool, b.BatchNo)
	_, e := s.RetryFailed(context.Background(), b.BatchNo)
	check(t, e)
	cb := docproc.CallbackRequest{CallbackID: "original-success", DocumentID: id, AttemptCount: 1, Status: docproc.StatusSuccess}
	if !errors.Is(s.HandleCallback(context.Background(), cb), docproc.ErrConflict) {
		t.Fatal("pending SUCCESS should be deferred without recording")
	}
	_ = s.ProcessDocument(context.Background(), id)
	check(t, s.HandleCallback(context.Background(), cb))
	ds, e := s.ListDocuments(context.Background(), b.BatchNo)
	check(t, e)
	if ds[0].Status != docproc.StatusSuccess || ds[0].AttemptCount != 2 {
		t.Fatalf("original accepted request did not resolve retry: %+v", ds[0])
	}
}

func TestCancelJoinsWorkersAndLeavesNoUnrecoverableClaims(t *testing.T) {
	var inflight atomic.Int32
	entered := make(chan struct{}, 3)
	s := newService(t, postgresPool(t), principalFunc(func(ctx context.Context, d docproc.Document) error {
		inflight.Add(1)
		defer inflight.Add(-1)
		entered <- struct{}{}
		<-ctx.Done()
		return ctx.Err()
	}))
	b := create(t, s, "cancel", 20)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- s.ProcessBatch(ctx, b.BatchNo) }()
	for range 3 {
		wait(t, entered)
	}
	cancel()
	select {
	case e := <-done:
		if !errors.Is(e, context.Canceled) {
			t.Fatal(e)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("workers leaked")
	}
	st, e := s.GetBatchStatus(context.Background(), b.BatchNo)
	check(t, e)
	if inflight.Load() != 0 || st.Counts[docproc.StatusProcessing] != 0 || st.Counts[docproc.StatusPending] != 17 {
		t.Fatalf("active=%d status=%+v", inflight.Load(), st)
	}
}

func TestTimeoutAndCallbackBeforeSubmitReturn(t *testing.T) {
	t.Run("timeout", func(t *testing.T) {
		s := newService(t, postgresPool(t), principalFunc(func(ctx context.Context, d docproc.Document) error { <-ctx.Done(); return ctx.Err() }), 5*time.Millisecond)
		b := create(t, s, "timeout", 1)
		if !errors.Is(s.ProcessBatch(context.Background(), b.BatchNo), context.DeadlineExceeded) {
			t.Fatal("missing deadline")
		}
		ds, e := s.ListDocuments(context.Background(), b.BatchNo)
		check(t, e)
		if ds[0].Status != docproc.StatusFailed {
			t.Fatal(ds)
		}
	})
	t.Run("early success", func(t *testing.T) {
		var s *service.DocProcService
		s = newService(t, postgresPool(t), principalFunc(func(ctx context.Context, d docproc.Document) error {
			check(t, s.HandleCallback(ctx, docproc.CallbackRequest{CallbackID: "early", DocumentID: d.ID, AttemptCount: d.AttemptCount, Status: docproc.StatusSuccess}))
			return errors.New("connection closed")
		}))
		b := create(t, s, "early", 1)
		_ = s.ProcessBatch(context.Background(), b.BatchNo)
		st, e := s.GetBatchStatus(context.Background(), b.BatchNo)
		check(t, e)
		if st.Counts[docproc.StatusSuccess] != 1 {
			t.Fatal(st)
		}
	})
}

func TestMixedConcurrentOperations(t *testing.T) {
	s := newService(t, postgresPool(t), principalFunc(func(context.Context, docproc.Document) error { return nil }))
	b := create(t, s, "mixed", 20)
	var wg sync.WaitGroup
	for range 10 {
		wg.Go(func() { check(t, s.ProcessBatch(context.Background(), b.BatchNo)) })
		wg.Go(func() { _, e := s.RetryFailed(context.Background(), b.BatchNo); check(t, e) })
		wg.Go(func() { _, e := s.GetBatchStatus(context.Background(), b.BatchNo); check(t, e) })
		wg.Go(func() {
			ds, e := s.ListDocuments(context.Background(), b.BatchNo)
			check(t, e)
			for _, d := range ds {
				if d.AttemptCount > 0 {
					check(t, s.HandleCallback(context.Background(), docproc.CallbackRequest{CallbackID: "cb-" + d.ID, DocumentID: d.ID, AttemptCount: 1, Status: docproc.StatusSuccess}))
				}
			}
		})
	}
	wg.Wait()
	ds, e := s.ListDocuments(context.Background(), b.BatchNo)
	check(t, e)
	for _, d := range ds {
		if d.AttemptCount > 1 {
			t.Fatalf("double claim: %+v", d)
		}
	}
}

func TestCreateValidationAndErrors(t *testing.T) {
	s := newService(t, postgresPool(t), principalFunc(func(context.Context, docproc.Document) error { return nil }))
	create(t, s, "unique", 1)
	if _, e := s.CreateBatch(context.Background(), "unique", docs(1)); !errors.Is(e, docproc.ErrConflict) {
		t.Fatal(e)
	}
	for _, ds := range [][]docproc.Document{nil, {{DocumentNo: "x", Payload: json.RawMessage(`bad`)}}, {{DocumentNo: "x", Payload: json.RawMessage(`{}`)}, {DocumentNo: "x", Payload: json.RawMessage(`{}`)}}} {
		if _, e := s.CreateBatch(context.Background(), "invalid", ds); !errors.Is(e, docproc.ErrInvalidInput) {
			t.Fatal(e)
		}
	}
	if e := s.ProcessBatch(context.Background(), "missing"); !errors.Is(e, docproc.ErrBatchNotFound) {
		t.Fatal(e)
	}
	if e := s.ProcessDocument(context.Background(), "999"); !errors.Is(e, docproc.ErrDocumentNotFound) {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e := s.CreateBatch(ctx, "canceled", docs(1)); !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
}

func BenchmarkProcessBatch(b *testing.B) {
	s := newService(b, postgresPool(b), principalFunc(func(context.Context, docproc.Document) error { return nil }))
	i := 0
	for b.Loop() {
		no := fmt.Sprintf("bench-%d", i)
		i++
		create(b, s, no, 20)
		check(b, s.ProcessBatch(context.Background(), no))
	}
}

func newService(t testing.TB, pool *pgxpool.Pool, client principal.IPrincipalClient, timeout ...time.Duration) *service.DocProcService {
	t.Helper()
	budget := 5 * time.Second
	if len(timeout) > 0 {
		budget = timeout[0]
	}
	retry := newRetryPolicy()
	s, err := service.NewDocProcService(batch.NewBatchRepository(pool, retry), callback.NewCallbackRepository(pool, retry), client, budget)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestConstructorRejectsInvalidDependenciesAndTimeout(t *testing.T) {
	pool := postgresPool(t)
	retry := newRetryPolicy()
	batchRepo := batch.NewBatchRepository(pool, retry)
	callbackRepo := callback.NewCallbackRepository(pool, retry)
	client := principalFunc(func(context.Context, docproc.Document) error { return nil })
	for _, tc := range []struct {
		name         string
		batchRepo    batch.IBatchRepository
		callbackRepo callback.ICallbackRepository
		client       principal.IPrincipalClient
		timeout      time.Duration
	}{
		{"missing batch repository", nil, callbackRepo, client, time.Second},
		{"missing callback repository", batchRepo, nil, client, time.Second},
		{"missing Principal", batchRepo, callbackRepo, nil, time.Second},
		{"zero timeout", batchRepo, callbackRepo, client, 0},
		{"negative timeout", batchRepo, callbackRepo, client, -time.Second},
		{"timeout exceeds lease budget", batchRepo, callbackRepo, client, 57 * time.Second},
		{"timeout overflow", batchRepo, callbackRepo, client, time.Duration(1<<63 - 1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := service.NewDocProcService(tc.batchRepo, tc.callbackRepo, tc.client, tc.timeout); !errors.Is(err, docproc.ErrInvalidInput) {
				t.Fatal(err)
			}
		})
	}
}

func TestServiceReadsAndRecoveryPreserveRepositoryErrors(t *testing.T) {
	s := newService(t, postgresPool(t), principalFunc(func(context.Context, docproc.Document) error { return nil }))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.ListDocuments(ctx, "batch"); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := s.ListCallbacks(ctx, "1"); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := s.RecoverStaleDocuments(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
