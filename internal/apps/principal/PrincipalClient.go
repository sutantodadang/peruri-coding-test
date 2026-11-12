package principal

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"peruri/internal/apps/docproc"
)

type PrincipalClient struct {
	url, token string
	http       HTTPClient
}

type HTTPClient interface {
	Do(*http.Request) (*http.Response, error)
}

var _ IPrincipalClient = (*PrincipalClient)(nil)

func New(url, token string, client HTTPClient) *PrincipalClient {
	return &PrincipalClient{url: strings.TrimRight(url, "/") + "/submit", token: token, http: client}
}
func (c *PrincipalClient) Submit(ctx context.Context, d docproc.Document) error {
	body, e := json.Marshal(d)
	if e != nil {
		return fmt.Errorf("encode submit: %w", e)
	}
	req, e := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(body))
	if e != nil {
		return fmt.Errorf("submit request: %w", e)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Idempotency-Key", d.RequestID)
	resp, e := c.http.Do(req)
	if e != nil {
		return fmt.Errorf("principal submit: %w", e)
	}
	defer resp.Body.Close()
	_, e = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if e != nil {
		return fmt.Errorf("read principal response: %w", e)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("principal rejected request (HTTP %d)", resp.StatusCode)
	}
	return nil
}
