package docs

import (
	_ "embed"
	"io"
	"net/http"

	"github.com/swaggest/swgui"
	"github.com/swaggest/swgui/v5emb"
)

//go:embed openapi.json
var specification string

func OpenAPI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = io.WriteString(w, specification)
}

func SwaggerUI() http.Handler {
	ui := v5emb.NewHandlerWithConfig(swgui.Config{
		Title: "Peruri Bulk Document API", SwaggerJSON: "/openapi.json", BasePath: "/docs/",
		SettingsUI: map[string]string{
			"validatorUrl": "null", "persistAuthorization": "false",
			"displayRequestDuration": "true", "defaultModelsExpandDepth": "1",
		},
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		ui.ServeHTTP(w, r)
	})
}
