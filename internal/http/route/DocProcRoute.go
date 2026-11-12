package route

import (
	"context"
	"net/http"
	"peruri/docs"
	"peruri/internal/http/handler"
	"peruri/internal/http/middlewares"
	"peruri/internal/http/response"
	"time"
)

func RegisterDocumentationRoute(app *http.ServeMux) {
	app.HandleFunc("GET /openapi.json", docs.OpenAPI)
	app.Handle("GET /docs/", docs.SwaggerUI())
}

func RegisterDocProcRoute(app *http.ServeMux, docProcHandler handler.IDocProcHandler, middleware *middlewares.Middleware) {
	app.Handle("POST /batches", middleware.Wrap(http.HandlerFunc(docProcHandler.CreateBatch)))
	app.Handle("POST /batches/{no}/process", middleware.Wrap(http.HandlerFunc(docProcHandler.ProcessBatch)))
	app.Handle("POST /documents/{id}/process", middleware.Wrap(http.HandlerFunc(docProcHandler.ProcessDocument)))
	app.Handle("POST /callbacks", middleware.Wrap(http.HandlerFunc(docProcHandler.HandleCallback)))
	app.Handle("POST /batches/{no}/retry", middleware.Wrap(http.HandlerFunc(docProcHandler.RetryFailed)))
	app.Handle("GET /batches/{no}", middleware.Wrap(http.HandlerFunc(docProcHandler.GetBatchStatus)))
	app.Handle("GET /batches/{no}/documents", middleware.Wrap(http.HandlerFunc(docProcHandler.ListDocuments)))
	app.Handle("GET /documents/{id}/callbacks", middleware.Wrap(http.HandlerFunc(docProcHandler.ListCallbacks)))
}

type HealthChecker interface{ Ping(context.Context) error }

func RegisterHealthRoute(app *http.ServeMux, health HealthChecker, middleware *middlewares.Middleware) {
	app.Handle("GET /healthz", middleware.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := health.Ping(ctx); err != nil {
			response.JSON(w, 503, map[string]string{"error": "database unavailable"})
			return
		}
		response.JSON(w, 200, map[string]string{"status": "ok"})
	})))
}
