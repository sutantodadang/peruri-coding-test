package peruri_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"peruri/internal/apps/docproc"
	"peruri/internal/http/middlewares"
)

func readLogs(t testing.TB, buffer *bytes.Buffer) []map[string]any {
	t.Helper()
	var entries []map[string]any
	decoder := json.NewDecoder(bytes.NewReader(buffer.Bytes()))
	for {
		var entry map[string]any
		err := decoder.Decode(&entry)
		if err == io.EOF {
			return entries
		}
		check(t, err)
		entries = append(entries, entry)
	}
}

func TestCallbackConflictLogsAndResponseShareRequestID(t *testing.T) {
	var buffer bytes.Buffer
	app := &fakeApplication{err: fmt.Errorf("callback: %w", &docproc.CallbackConflictError{
		Code: docproc.CallbackRequiresProcessing, DocumentStatus: docproc.StatusPending, CurrentAttemptCount: 1,
	})}
	h := newHandler(t, app, "private-api-token", "private-callback-token", healthFunc(func(context.Context) error { return nil }), slog.New(slog.NewJSONHandler(&buffer, nil)))
	request := func() *http.Request {
		q := httptest.NewRequest("POST", "/callbacks", strings.NewReader(`{"callback_id":"event-openapi-002","document_id":"340","attempt_count":1,"status":"SUCCESS","message":"private-callback-message"}`))
		q.Header.Set("Content-Type", "application/json")
		q.Header.Set("Authorization", "Bearer private-callback-token")
		return q
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, request())
	var body map[string]any
	check(t, json.Unmarshal(w.Body.Bytes(), &body))
	requestID := w.Header().Get("X-Request-ID")
	if w.Code != 409 || body["error"] != "conflict" || body["code"] != docproc.CallbackRequiresProcessing || body["document_status"] != "PENDING" || body["current_attempt_count"] != float64(1) || body["request_id"] != requestID || !strings.Contains(body["message"].(string), "process the document") {
		t.Fatalf("callback conflict is not actionable or correlated: %s", w.Body.String())
	}
	entries := readLogs(t, &buffer)
	if len(entries) != 2 || entries[0]["msg"] != "callback rejected" || entries[0]["callback_id"] != "event-openapi-002" || entries[0]["document_id"] != "340" || entries[0]["callback_status"] != "SUCCESS" || entries[0]["reason"] != docproc.CallbackRequiresProcessing || entries[0]["document_status"] != "PENDING" || entries[0]["current_attempt_count"] != float64(1) {
		t.Fatalf("missing callback diagnostics: %s", buffer.String())
	}
	for _, entry := range entries {
		if entry["request_id"] != requestID || entry["level"] != "WARN" || entry["status"] != float64(409) {
			t.Fatalf("callback logs lost request correlation: %s", buffer.String())
		}
	}
	for _, secret := range []string{"private-callback-token", "private-callback-message"} {
		if strings.Contains(buffer.String(), secret) || strings.Contains(w.Body.String(), secret) {
			t.Fatalf("callback values exposed: %s", secret)
		}
	}
	buffer.Reset()
	app.err = nil
	w = httptest.NewRecorder()
	h.ServeHTTP(w, request())
	entries = readLogs(t, &buffer)
	if w.Code != 200 || len(entries) != 2 || entries[0]["msg"] != "callback acknowledged" || entries[0]["level"] != "INFO" || entries[0]["document_id"] != "340" || entries[0]["request_id"] == requestID || entries[0]["request_id"] != w.Header().Get("X-Request-ID") {
		t.Fatalf("successful callback lacks its own correlated INFO log: %s", buffer.String())
	}
	if strings.Contains(buffer.String(), "private-callback-message") {
		t.Fatal("success callback message logged")
	}
}

func TestRequestLogPreservesPanics(t *testing.T) {
	var buffer bytes.Buffer
	middleware := middlewares.NewMiddleware("api", "callback", slog.New(slog.NewJSONHandler(&buffer, nil)))
	h := middleware.Wrap(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("private-panic-data") }))
	func() {
		defer func() {
			if recovered := recover(); recovered != "private-panic-data" {
				t.Fatalf("middleware swallowed or changed panic: %v", recovered)
			}
		}()
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/healthz", nil))
	}()
	entries := readLogs(t, &buffer)
	if len(entries) != 1 || entries[0]["level"] != "ERROR" || entries[0]["msg"] != "http request aborted" || entries[0]["status"] != float64(0) || strings.Contains(buffer.String(), "private-panic-data") {
		t.Fatalf("panic incorrectly logged as a successful response: %s", buffer.String())
	}
}

func TestAPIRequestsEmitCorrelatedLogs(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		token  string
		level  string
	}{
		{"implicit success", 0, "private-callback-token", "INFO"},
		{"created", 201, "private-callback-token", "INFO"},
		{"conflict", 409, "private-callback-token", "WARN"},
		{"internal error", 500, "private-callback-token", "ERROR"},
		{"wrong token", 401, "wrong-token", "WARN"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var buffer bytes.Buffer
			middleware := middlewares.NewMiddleware("private-api-token", "private-callback-token", slog.New(slog.NewJSONHandler(&buffer, nil)))
			mux := http.NewServeMux()
			mux.Handle("POST /callbacks", middleware.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tc.status != 0 {
					w.WriteHeader(tc.status)
				}
				_, _ = w.Write([]byte("ok"))
			})))
			q := httptest.NewRequest("POST", "/callbacks?secret=private-query-data", strings.NewReader(`{"message":"private-body-data"}`))
			q.Header.Set("Authorization", "Bearer "+tc.token)
			q.Header.Set("X-Request-ID", "private-client-request-id")
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, q)
			entries := readLogs(t, &buffer)
			if len(entries) != 1 {
				t.Fatalf("want one completion log, got %d: %s", len(entries), buffer.String())
			}
			entry := entries[0]
			requestID := w.Header().Get("X-Request-ID")
			if requestID == "" || entry["request_id"] != requestID || entry["level"] != tc.level || entry["method"] != "POST" || entry["route"] != "POST /callbacks" || entry["status"] != float64(w.Code) || entry["response_bytes"] != float64(w.Body.Len()) {
				t.Fatalf("incomplete completion log or correlation: %s", buffer.String())
			}
			if duration, ok := entry["duration_ms"].(float64); !ok || duration < 0 {
				t.Fatal("request duration missing")
			}
			for _, secret := range []string{"private-callback-token", "private-body-data", "private-query-data", "private-client-request-id", "wrong-token"} {
				if strings.Contains(buffer.String(), secret) {
					t.Fatalf("sensitive request content logged: %s", secret)
				}
			}
		})
	}
}
