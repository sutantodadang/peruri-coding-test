package peruri_test

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"peruri/internal/http/middlewares"
	"sync"
	"testing"
	"time"
)

func TestAdmissionLimitIsSharedAcrossRegisteredRoutes(t *testing.T) {
	middleware := middlewares.NewMiddleware("api", "callback", slog.New(slog.NewJSONHandler(io.Discard, nil)))
	entered := make(chan struct{}, 32)
	release := make(chan struct{})
	blocked := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		entered <- struct{}{}
		select {
		case <-release:
			w.WriteHeader(200)
		case <-r.Context().Done():
			t.Error("request budget expired")
		}
	})
	first := middleware.Wrap(blocked)
	second := middleware.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }))
	var requests sync.WaitGroup
	for range 32 {
		requests.Go(func() {
			req := httptest.NewRequest("GET", "/batches/first", nil)
			req.Header.Set("Authorization", "Bearer api")
			w := httptest.NewRecorder()
			first.ServeHTTP(w, req)
			if w.Code != 200 {
				t.Error(w.Code)
			}
		})
	}
	for range 32 {
		select {
		case <-entered:
		case <-time.After(10 * time.Second):
			t.Fatal("admission barrier timed out")
		}
	}
	req := httptest.NewRequest("GET", "/batches/second", nil)
	req.Header.Set("Authorization", "Bearer api")
	w := httptest.NewRecorder()
	second.ServeHTTP(w, req)
	close(release)
	requests.Wait()
	if w.Code != 429 {
		t.Fatalf("second route bypassed shared admission: %d", w.Code)
	}
	w = httptest.NewRecorder()
	second.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatal("admission not released", w.Code)
	}
}
