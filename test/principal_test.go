package peruri_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"peruri/internal/apps/docproc"
	"peruri/internal/apps/principal"
	"strings"
	"testing"
	"time"
)

func TestSubmitHTTPContract(t *testing.T) {
	for _, status := range []int{200, 202, 400, 500} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "POST" || r.URL.Path != "/submit" || r.Header.Get("Idempotency-Key") != "stable" || r.Header.Get("Authorization") != "Bearer secret" {
					t.Error("bad request contract")
				}
				w.WriteHeader(status)
			}))
			defer server.Close()
			c := principal.New(server.URL, "secret", server.Client())
			e := c.Submit(context.Background(), docproc.Document{ID: "1", RequestID: "stable", Payload: []byte(`{}`)})
			if (e == nil) != (status < 300) {
				t.Fatal(e)
			}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			if !errors.Is(c.Submit(ctx, docproc.Document{Payload: []byte(`{}`)}), context.Canceled) {
				t.Fatal("cancellation lost")
			}
		})
	}
}

type httpClientFunc func(*http.Request) (*http.Response, error)

func (f httpClientFunc) Do(r *http.Request) (*http.Response, error) { return f(r) }

func TestSubmitUsesInjectedHTTPClient(t *testing.T) {
	type key struct{}
	ctx := context.WithValue(context.Background(), key{}, "trace")
	c := principal.New("https://principal.example", "token", httpClientFunc(func(r *http.Request) (*http.Response, error) {
		if r.Context().Value(key{}) != "trace" || r.Method != "POST" || r.URL.String() != "https://principal.example/submit" || r.Header.Get("Idempotency-Key") != "stable" || r.Header.Get("Authorization") != "Bearer token" {
			t.Fatalf("invalid outbound request: %v", r)
		}
		var d docproc.Document
		if err := json.NewDecoder(r.Body).Decode(&d); err != nil {
			t.Fatal(err)
		}
		if d.ID != "7" || d.AttemptCount != 2 || d.RequestID != "stable" {
			t.Fatal(d)
		}
		return &http.Response{StatusCode: 202, Body: io.NopCloser(strings.NewReader("accepted"))}, nil
	}))
	if err := c.Submit(ctx, docproc.Document{ID: "7", AttemptCount: 2, RequestID: "stable", Payload: []byte(`{}`)}); err != nil {
		t.Fatal(err)
	}
	expected := errors.New("transport unavailable")
	c = principal.New("https://principal.example", "token", httpClientFunc(func(*http.Request) (*http.Response, error) { return nil, expected }))
	if err := c.Submit(ctx, docproc.Document{Payload: []byte(`{}`)}); !errors.Is(err, expected) {
		t.Fatal(err)
	}
}

func TestSimulatorDeliversCallbackAndRejectsFirstAttempt(t *testing.T) {
	callbacks := make(chan docproc.CallbackRequest, 2)
	callbackServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer cb" {
			t.Error("callback not authenticated")
		}
		var cb docproc.CallbackRequest
		if e := json.NewDecoder(r.Body).Decode(&cb); e != nil {
			t.Error(e)
		}
		callbacks <- cb
		w.WriteHeader(200)
	}))
	defer callbackServer.Close()
	ctx, cancel := context.WithCancel(context.Background())
	h, join := principal.NewSimulator(ctx, callbackServer.URL, "cb", "submit", callbackServer.Client())
	defer func() { cancel(); join() }()
	server := httptest.NewServer(h)
	defer server.Close()
	c := principal.New(server.URL, "submit", server.Client())
	d := docproc.Document{ID: "1", RequestID: "key", AttemptCount: 1, Payload: []byte(`{"mode":"fail_once"}`)}
	if c.Submit(context.Background(), d) == nil {
		t.Fatal("first attempt accepted")
	}
	d.AttemptCount = 2
	if e := c.Submit(context.Background(), d); e != nil {
		t.Fatal(e)
	}
	select {
	case cb := <-callbacks:
		if cb.Status != docproc.StatusSuccess || cb.AttemptCount != 2 || cb.DocumentID != "1" {
			t.Fatal(cb)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("missing callback")
	}
	bad := principal.New(server.URL, "wrong", server.Client())
	if bad.Submit(context.Background(), d) == nil {
		t.Fatal("unauthorized submit accepted")
	}
}
