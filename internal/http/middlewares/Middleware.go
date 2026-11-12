package middlewares

import (
	"context"
	"crypto/subtle"
	"log/slog"
	"net/http"
	"peruri/internal/http/response"
	"time"
)

type Middleware struct {
	apiToken      string
	callbackToken string
	admission     chan struct{}
	logger        *slog.Logger
}

func NewMiddleware(apiToken, callbackToken string, logger *slog.Logger) *Middleware {
	return &Middleware{apiToken: apiToken, callbackToken: callbackToken, admission: make(chan struct{}, 32), logger: logger}
}

// Wrap shares admission capacity across all routes registered with this instance.
func (m *Middleware) Wrap(next http.Handler) http.Handler {
	return m.logRequests(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if r.URL.Path != "/healthz" {
			expected := m.apiToken
			if r.URL.Path == "/callbacks" {
				expected = m.callbackToken
			}
			if expected == "" || subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+expected)) != 1 {
				response.JSON(w, 401, map[string]string{"error": "unauthorized"})
				return
			}
		}
		select {
		case m.admission <- struct{}{}:
			defer func() { <-m.admission }()
		default:
			response.JSON(w, 429, map[string]string{"error": "too many active requests"})
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
		defer cancel()
		next.ServeHTTP(w, r.WithContext(ctx))
	}))
}
