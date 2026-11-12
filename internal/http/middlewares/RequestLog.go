package middlewares

import (
	"context"
	"crypto/rand"
	"log/slog"
	"net/http"
	"time"
)

type requestLoggerKey struct{}

func RequestLogger(ctx context.Context) *slog.Logger {
	if logger, ok := ctx.Value(requestLoggerKey{}).(*slog.Logger); ok {
		return logger
	}
	return slog.Default()
}

type loggedResponse struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (w *loggedResponse) WriteHeader(status int) {
	if w.status != 0 {
		return
	}
	w.ResponseWriter.WriteHeader(status)
	if status >= 200 {
		w.status = status
	}
}

func (w *loggedResponse) Write(body []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	n, err := w.ResponseWriter.Write(body)
	w.bytes += n
	return n, err
}

func (w *loggedResponse) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (m *Middleware) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		requestID := rand.Text()
		w.Header().Set("X-Request-ID", requestID)
		logger := m.logger.With("request_id", requestID, "method", r.Method, "route", r.Pattern)
		ctx := context.WithValue(r.Context(), requestLoggerKey{}, logger)
		out := &loggedResponse{ResponseWriter: w}
		completed := false
		defer func() {
			level, message := slog.LevelInfo, "http request completed"
			status := out.status
			if !completed {
				level, message = slog.LevelError, "http request aborted"
			} else if status == 0 {
				status = http.StatusOK
			}
			if status >= 500 {
				level = slog.LevelError
			} else if completed && status >= 400 {
				level = slog.LevelWarn
			}
			logger.LogAttrs(ctx, level, message,
				slog.Int("status", status), slog.Int("response_bytes", out.bytes),
				slog.Float64("duration_ms", float64(time.Since(start).Microseconds())/1000))
		}()
		next.ServeHTTP(out, r.WithContext(ctx))
		completed = true
	})
}
