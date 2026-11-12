package container

import (
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"net/url"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"peruri/internal/apps/docproc"
	"peruri/internal/apps/docproc/service"
	"peruri/internal/apps/principal"
	"peruri/internal/http/handler"
	"peruri/internal/http/middlewares"
	"peruri/internal/http/route"
	"peruri/internal/repositories/batch"
	"peruri/internal/repositories/callback"
)

type Config struct {
	PrincipalURL   string
	PrincipalToken string
	APIToken       string
	CallbackToken  string
}

func (c Config) Validate() error {
	if c.APIToken == "" || c.CallbackToken == "" || c.PrincipalToken == "" || c.APIToken == c.CallbackToken {
		return errors.New("three tokens are required; API_TOKEN and CALLBACK_TOKEN must differ")
	}
	endpoint, err := url.Parse(c.PrincipalURL)
	if err != nil || endpoint.Host == "" || (endpoint.Scheme != "http" && endpoint.Scheme != "https") {
		return errors.New("PRINCIPAL_URL must be an absolute HTTP(S) URL")
	}
	return nil
}

type Container struct {
	DocProcService service.IDocProcService
	principalHTTP  *http.Client
}

func (c *Container) Close() { c.principalHTTP.CloseIdleConnections() }

func SetupContainer(app *http.ServeMux, pool *pgxpool.Pool, config Config) (*Container, error) {
	if app == nil || pool == nil {
		return nil, errors.New("HTTP mux and database pool are required")
	}
	if err := config.Validate(); err != nil {
		return nil, err
	}

	retry := docproc.NewRetryPolicy(time.Now, rand.Float64)
	batchRepo := batch.NewBatchRepository(pool, retry)
	callbackRepo := callback.NewCallbackRepository(pool, retry)
	middleware := middlewares.NewMiddleware(config.APIToken, config.CallbackToken, slog.Default())
	validate, err := handler.NewRequestValidator()
	if err != nil {
		return nil, err
	}
	principalHTTP := &http.Client{Timeout: 6 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	principalClient := principal.New(config.PrincipalURL, config.PrincipalToken, principalHTTP)

	docProcService, err := service.NewDocProcService(batchRepo, callbackRepo, principalClient, 5*time.Second)
	if err != nil {
		principalHTTP.CloseIdleConnections()
		return nil, fmt.Errorf("construct document service: %w", err)
	}

	docProcHandler := handler.NewDocProcHandler(docProcService, validate)

	route.RegisterDocProcRoute(app, docProcHandler, middleware)
	route.RegisterHealthRoute(app, pool, middleware)
	route.RegisterDocumentationRoute(app)

	return &Container{DocProcService: docProcService, principalHTTP: principalHTTP}, nil
}
