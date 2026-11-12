package principal

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync/atomic"
	"time"

	"peruri/internal/apps/docproc"
)

// NewSimulator is a local demo Principal. Its queue and worker lifetime are bounded.
func NewSimulator(ctx context.Context, callbackURL, callbackToken, submitToken string, client HTTPClient) (http.Handler, func()) {
	queue := make(chan docproc.CallbackRequest, 100)
	done := make(chan struct{})
	var inflight, maximum, total atomic.Int64
	go func() {
		defer close(done)
		for {
			select {
			case <-ctx.Done():
				return
			case cb := <-queue:
				body, e := json.Marshal(cb)
				if e != nil {
					slog.Error("encode demo callback", "error", e)
					continue
				}
				delivered := false
				for attempt := 0; attempt < 3 && ctx.Err() == nil; attempt++ {
					req, e := http.NewRequestWithContext(ctx, "POST", callbackURL, bytes.NewReader(body))
					if e != nil {
						slog.Error("create demo callback", "error", e)
						break
					}
					req.Header.Set("Authorization", "Bearer "+callbackToken)
					req.Header.Set("Content-Type", "application/json")
					resp, e := client.Do(req)
					if e == nil {
						_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
						_ = resp.Body.Close()
						if resp.StatusCode == 200 {
							delivered = true
							break
						}
					}
					timer := time.NewTimer(200 * time.Millisecond)
					select {
					case <-ctx.Done():
						timer.Stop()
						return
					case <-timer.C:
					}
				}
				if !delivered {
					slog.Error("demo callback delivery exhausted", "callback_id", cb.CallbackID)
				}
			}
		}
	}()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	mux.HandleFunc("GET /stats", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]int64{"inflight": inflight.Load(), "max_inflight": maximum.Load(), "total_requests": total.Load()})
	})
	mux.HandleFunc("POST /submit", func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		var d docproc.Document
		if e := json.NewDecoder(r.Body).Decode(&d); e != nil {
			http.Error(w, "invalid document", 400)
			return
		}
		if d.ID == "" || d.RequestID == "" || r.Header.Get("Idempotency-Key") != d.RequestID || d.AttemptCount < 1 || d.AttemptCount > 3 {
			http.Error(w, "invalid request identity", 400)
			return
		}
		var payload struct {
			Mode string `json:"mode"`
		}
		if e := json.Unmarshal(d.Payload, &payload); e != nil {
			http.Error(w, "invalid demo payload", 400)
			return
		}
		total.Add(1)
		n := inflight.Add(1)
		defer inflight.Add(-1)
		for old := maximum.Load(); n > old; old = maximum.Load() {
			if maximum.CompareAndSwap(old, n) {
				break
			}
		}
		delay := 50 * time.Millisecond
		if payload.Mode == "timeout" {
			delay = 10 * time.Second
		}
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-r.Context().Done():
			return
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		switch payload.Mode {
		case "reject", "timeout":
			http.Error(w, "demo rejection", 503)
			return
		case "fail_once":
			if d.AttemptCount == 1 {
				http.Error(w, "demo first attempt rejection", 503)
				return
			}
		case "", "success", "failed", "manual":
		default:
			http.Error(w, "unknown demo mode", 400)
			return
		}
		if payload.Mode != "manual" {
			status := docproc.StatusSuccess
			if payload.Mode == "failed" {
				status = docproc.StatusFailed
			}
			cb := docproc.CallbackRequest{CallbackID: fmt.Sprintf("demo-%s-%d-%s", d.RequestID, d.AttemptCount, status), DocumentID: d.ID, AttemptCount: d.AttemptCount, Status: status}
			if status == docproc.StatusFailed {
				cb.Message = "demo principal processing failed"
			}
			select {
			case queue <- cb:
			case <-ctx.Done():
				http.Error(w, "shutting down", 503)
				return
			default:
				http.Error(w, "callback queue full", 503)
				return
			}
		}
		w.WriteHeader(http.StatusAccepted)
	})
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" && (submitToken == "" || subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+submitToken)) != 1) {
			http.Error(w, "unauthorized", 401)
			return
		}
		mux.ServeHTTP(w, r)
	})
	return h, func() { <-done }
}
