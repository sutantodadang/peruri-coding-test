package peruri_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"peruri/internal/apps/docproc"
	"peruri/internal/apps/docproc/service"
	"peruri/internal/http/handler"
	"peruri/internal/http/middlewares"
	"peruri/internal/http/route"
	"strings"
	"testing"
)

func newHandler(t testing.TB, app service.IDocProcService, apiToken, callbackToken string, health route.HealthChecker, loggers ...*slog.Logger) http.Handler {
	t.Helper()
	mux := http.NewServeMux()
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	if len(loggers) > 0 {
		logger = loggers[0]
	}
	middleware := middlewares.NewMiddleware(apiToken, callbackToken, logger)
	validate, err := handler.NewRequestValidator()
	check(t, err)
	docProcHandler := handler.NewDocProcHandler(app, validate)
	route.RegisterDocProcRoute(mux, docProcHandler, middleware)
	route.RegisterHealthRoute(mux, health, middleware)
	route.RegisterDocumentationRoute(mux)
	return mux
}

// This fake implements only the HTTP consumer's contract, with no repository or concrete service.
type fakeApplication struct {
	called, arg string
	ctx         context.Context
	err         error
}

func (f *fakeApplication) record(ctx context.Context, method, arg string) {
	f.called, f.arg, f.ctx = method, arg, ctx
}
func (f *fakeApplication) CreateBatch(ctx context.Context, no string, ds []docproc.Document) (*docproc.Batch, error) {
	f.record(ctx, "create", no)
	return &docproc.Batch{ID: "1", BatchNo: no, Documents: ds}, f.err
}
func (f *fakeApplication) ProcessBatch(ctx context.Context, no string) error {
	f.record(ctx, "process", no)
	return f.err
}
func (f *fakeApplication) ProcessDocument(ctx context.Context, id string) error {
	f.record(ctx, "document", id)
	return f.err
}
func (f *fakeApplication) HandleCallback(ctx context.Context, cb docproc.CallbackRequest) error {
	f.record(ctx, "callback", cb.DocumentID)
	return f.err
}
func (f *fakeApplication) RetryFailed(ctx context.Context, no string) (int, error) {
	f.record(ctx, "retry", no)
	return 2, f.err
}
func (f *fakeApplication) GetBatchStatus(ctx context.Context, no string) (*docproc.BatchStatus, error) {
	f.record(ctx, "status", no)
	return &docproc.BatchStatus{BatchNo: no, Status: "PENDING"}, f.err
}
func (f *fakeApplication) ListDocuments(ctx context.Context, no string) ([]docproc.Document, error) {
	f.record(ctx, "list", no)
	return []docproc.Document{{ID: "7", DocumentNo: "one", Payload: json.RawMessage(`{}`)}}, f.err
}
func (f *fakeApplication) ListCallbacks(ctx context.Context, id string) ([]docproc.CallbackRequest, error) {
	f.record(ctx, "audit", id)
	return []docproc.CallbackRequest{{CallbackID: "event", DocumentID: id, Status: docproc.StatusSuccess, AttemptCount: 1}}, f.err
}

type healthFunc func(context.Context) error

func (f *fakeApplication) RecoverStaleDocuments(ctx context.Context) (int, error) {
	f.record(ctx, "recover", "")
	return 0, f.err
}

func (f healthFunc) Ping(ctx context.Context) error { return f(ctx) }

func TestHandlerAcceptsApplicationInterfaceAndPreservesContext(t *testing.T) {
	type key struct{}
	for _, tc := range []struct {
		method, path, body, called, arg string
		want                            int
	}{
		{"POST", "/batches", `{"batch_no":"test","documents":[{"document_no":"one","payload":{}}]}`, "create", "test", 201},
		{"POST", "/batches/test/process", "", "process", "test", 200},
		{"POST", "/documents/7/process", "", "document", "7", 200},
		{"POST", "/callbacks", `{"callback_id":"event","document_id":"7","attempt_count":1,"status":"SUCCESS"}`, "callback", "7", 200},
		{"POST", "/batches/test/retry", "", "retry", "test", 200},
		{"GET", "/batches/test", "", "status", "test", 200},
		{"GET", "/batches/test/documents", "", "list", "test", 200},
		{"GET", "/documents/7/callbacks", "", "audit", "7", 200},
	} {
		t.Run(tc.called, func(t *testing.T) {
			app := &fakeApplication{}
			h := newHandler(t, app, "api", "callback", healthFunc(func(context.Context) error { return nil }))
			ctx := context.WithValue(context.Background(), key{}, "request-context")
			req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body)).WithContext(ctx)
			token := "api"
			if tc.called == "callback" {
				token = "callback"
			}
			req.Header.Set("Authorization", "Bearer "+token)
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			h.ServeHTTP(w, req)
			if w.Code != tc.want || app.called != tc.called || app.arg != tc.arg {
				t.Fatalf("status=%d called=%s arg=%s body=%s", w.Code, app.called, app.arg, w.Body.String())
			}
			if app.ctx.Value(key{}) != "request-context" {
				t.Fatal("request context lost")
			}
			if _, ok := app.ctx.Deadline(); !ok {
				t.Fatal("request budget lost")
			}
		})
	}
}
func TestInjectedErrorsKeepHTTPContract(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want int
	}{{docproc.ErrBatchNotFound, 404}, {docproc.ErrConflict, 409}, {docproc.ErrInvalidInput, 400}, {context.DeadlineExceeded, 504}, {context.Canceled, 408}, {docproc.ErrSubmitFailed, 502}, {errors.New("private database detail"), 500}} {
		app := &fakeApplication{err: fmt.Errorf("application: %w", tc.err)}
		h := newHandler(t, app, "api", "callback", healthFunc(func(context.Context) error { return nil }))
		q := httptest.NewRequest("GET", "/batches/test", nil)
		q.Header.Set("Authorization", "Bearer api")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, q)
		if w.Code != tc.want || strings.Contains(w.Body.String(), "private database detail") {
			t.Fatalf("%d %s", w.Code, w.Body.String())
		}
	}
	h := newHandler(t, &fakeApplication{}, "api", "callback", healthFunc(func(context.Context) error { return errors.New("offline") }))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/healthz", nil))
	if w.Code != 503 {
		t.Fatal(w.Code)
	}
}

func TestRequestValidationRejectsBeforeService(t *testing.T) {
	for _, tc := range []struct {
		name, path, body, field string
	}{
		{"missing batch", "/batches", `{}`, "batch_no"},
		{"blank batch", "/batches", `{"batch_no":"  ","documents":[{"document_no":"one","payload":{}}]}`, "batch_no"},
		{"empty documents", "/batches", `{"batch_no":"test","documents":[]}`, "documents"},
		{"blank document", "/batches", `{"batch_no":"test","documents":[{"document_no":" ","payload":{}}]}`, "documents[0].document_no"},
		{"missing payload", "/batches", `{"batch_no":"test","documents":[{"document_no":"one"}]}`, "documents[0].payload"},
		{"null payload", "/batches", `{"batch_no":"test","documents":[{"document_no":"one","payload":null}]}`, "documents[0].payload"},
		{"duplicate document", "/batches", `{"batch_no":"test","documents":[{"document_no":"one","payload":{}},{"document_no":"one","payload":{}}]}`, "documents"},
		{"byte limit", "/batches", `{"batch_no":"` + strings.Repeat("é", 33) + `","documents":[{"document_no":"one","payload":{}}]}`, "batch_no"},
		{"blank event", "/callbacks", `{"callback_id":" ","document_id":"1","attempt_count":1,"status":"SUCCESS"}`, "callback_id"},
		{"invalid document ID", "/callbacks", `{"callback_id":"event","document_id":"01","attempt_count":1,"status":"SUCCESS"}`, "document_id"},
		{"zero attempt", "/callbacks", `{"callback_id":"event","document_id":"1","attempt_count":0,"status":"SUCCESS"}`, "attempt_count"},
		{"fourth attempt", "/callbacks", `{"callback_id":"event","document_id":"1","attempt_count":4,"status":"SUCCESS"}`, "attempt_count"},
		{"invalid status", "/callbacks", `{"callback_id":"event","document_id":"1","attempt_count":1,"status":"PENDING"}`, "status"},
		{"message byte limit", "/callbacks", `{"callback_id":"event","document_id":"1","attempt_count":1,"status":"FAILED","message":"` + strings.Repeat("é", 1025) + `"}`, "message"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app := &fakeApplication{}
			h := newHandler(t, app, "api", "callback", healthFunc(func(context.Context) error { return nil }))
			token := "api"
			if tc.path == "/callbacks" {
				token = "callback"
			}
			req := httptest.NewRequest("POST", tc.path, strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", "Bearer "+token)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, req)
			var result struct {
				Error  string                         `json:"error"`
				Fields []struct{ Field, Rule string } `json:"fields"`
			}
			check(t, json.Unmarshal(w.Body.Bytes(), &result))
			if w.Code != 400 || app.called != "" || result.Error != "validation failed" || len(result.Fields) == 0 || result.Fields[0].Field != tc.field || result.Fields[0].Rule == "" {
				t.Fatalf("invalid request reached service or lost field error: status=%d called=%s body=%s", w.Code, app.called, w.Body.String())
			}
		})
	}
}

func TestRequestValidationBoundaries(t *testing.T) {
	for _, payload := range []string{`{}`, `[]`, `false`, `0`, `""`, `"text"`} {
		t.Run("payload "+payload, func(t *testing.T) {
			app := &fakeApplication{}
			h := newHandler(t, app, "api", "callback", healthFunc(func(context.Context) error { return nil }))
			body := `{"batch_no":"` + strings.Repeat("é", 32) + `","documents":[{"document_no":"` + strings.Repeat("é", 32) + `","payload":` + payload + `}]}`
			q := httptest.NewRequest("POST", "/batches", strings.NewReader(body))
			q.Header.Set("Authorization", "Bearer api")
			q.Header.Set("Content-Type", "application/json; charset=utf-8")
			w := httptest.NewRecorder()
			h.ServeHTTP(w, q)
			if w.Code != 201 || app.called != "create" {
				t.Fatalf("valid payload or 64-byte names rejected: %d %s", w.Code, w.Body.String())
			}
		})
	}
	for _, count := range []int{1000, 1001} {
		t.Run(fmt.Sprint("document count ", count), func(t *testing.T) {
			app := &fakeApplication{}
			h := newHandler(t, app, "api", "callback", healthFunc(func(context.Context) error { return nil }))
			documents := make([]map[string]any, count)
			for i := range documents {
				documents[i] = map[string]any{"document_no": fmt.Sprint(i), "payload": map[string]any{}}
			}
			body, err := json.Marshal(map[string]any{"batch_no": "limit", "documents": documents})
			check(t, err)
			q := httptest.NewRequest("POST", "/batches", strings.NewReader(string(body)))
			q.Header.Set("Authorization", "Bearer api")
			q.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			h.ServeHTTP(w, q)
			if count == 1000 && (w.Code != 201 || app.called != "create") || count == 1001 && (w.Code != 400 || app.called != "") {
				t.Fatalf("document limit broken: count=%d status=%d called=%s", count, w.Code, app.called)
			}
		})
	}
	for _, status := range []string{"SUCCESS", "FAILED"} {
		t.Run("callback "+status, func(t *testing.T) {
			app := &fakeApplication{}
			h := newHandler(t, app, "api", "callback", healthFunc(func(context.Context) error { return nil }))
			body := `{"callback_id":"` + strings.Repeat("é", 64) + `","document_id":"9223372036854775807","attempt_count":3,"status":"` + status + `","message":"` + strings.Repeat("é", 1024) + `"}`
			q := httptest.NewRequest("POST", "/callbacks", strings.NewReader(body))
			q.Header.Set("Authorization", "Bearer callback")
			q.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			h.ServeHTTP(w, q)
			if w.Code != 200 || app.called != "callback" {
				t.Fatalf("valid callback boundary rejected: %d %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestJSONRequestBoundaries(t *testing.T) {
	valid := `{"batch_no":"boundary","documents":[{"document_no":"one","payload":{}}]}`
	for _, tc := range []struct {
		name, contentType, body string
		want                    int
	}{
		{"media type case", "Application/JSON; Charset=UTF-8", valid, 201},
		{"unsupported media", "text/plain", valid, 415},
		{"malformed media", "application/json; charset", valid, 415},
		{"second JSON value", "application/json", valid + ` {}`, 400},
		{"trailing garbage", "application/json", valid + ` x`, 400},
		{"oversized trailing whitespace", "application/json", valid + strings.Repeat(" ", 1<<20), 413},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app := &fakeApplication{}
			h := newHandler(t, app, "api", "callback", healthFunc(func(context.Context) error { return nil }))
			req := httptest.NewRequest("POST", "/batches", strings.NewReader(tc.body))
			req.Header.Set("Content-Type", tc.contentType)
			req.Header.Set("Authorization", "Bearer api")
			w := httptest.NewRecorder()
			h.ServeHTTP(w, req)
			if w.Code != tc.want || (app.called != "") != (tc.want == 201) {
				t.Fatalf("status=%d want=%d called=%s body=%s", w.Code, tc.want, app.called, w.Body.String())
			}
		})
	}
}
