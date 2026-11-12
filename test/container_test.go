package peruri_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"peruri/internal/apps/docproc"
	"peruri/internal/container"
	"strings"
	"testing"
)

func TestSetupContainerRejectsMissingDatabase(t *testing.T) {
	_, err := container.SetupContainer(http.NewServeMux(), nil, container.Config{})
	if err == nil {
		t.Fatal("missing database accepted")
	}
}

func TestAPIValidationAuthAndWorkflow(t *testing.T) {
	pool := postgresPool(t)
	principalServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/submit" || r.Header.Get("Authorization") != "Bearer principal-token" {
			t.Error("Principal wiring lost configuration")
		}
		var d docproc.Document
		if err := json.NewDecoder(r.Body).Decode(&d); err != nil {
			t.Error(err)
		}
		if d.AttemptCount != 1 || d.RequestID == "" || r.Header.Get("Idempotency-Key") != d.RequestID {
			t.Error("invalid submitted identity")
		}
		w.WriteHeader(202)
	}))
	defer principalServer.Close()
	h := http.NewServeMux()
	app, err := container.SetupContainer(h, pool, container.Config{APIToken: "api-token", CallbackToken: "callback-token", PrincipalToken: "principal-token", PrincipalURL: principalServer.URL})
	check(t, err)
	defer app.Close()
	call := func(method, path, body, token string, want int) string {
		t.Helper()
		q := httptest.NewRequest(method, path, strings.NewReader(body))
		q.Header.Set("Content-Type", "application/json")
		q.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, q)
		if w.Code != want {
			t.Fatalf("%s %s: %d want %d body=%s", method, path, w.Code, want, w.Body.String())
		}
		return w.Body.String()
	}
	call("GET", "/healthz", "", "", http.StatusOK)
	call("GET", "/docs/", "", "", http.StatusOK)
	call("GET", "/openapi.json", "", "", http.StatusOK)
	call("POST", "/batches", `{"batch_no":"demo","documents":[{"document_no":"one","payload":{"x":1}}]}`, "", 401)
	call("POST", "/batches", `{"batch_no":"demo","documents":[{"document_no":"one","payload":{"x":1}}]}`, "callback-token", 401)
	call("POST", "/batches", `{"batch_no":"demo","documents":[{"document_no":"one","payload":{"x":1}}]}`, "api-token", 201)
	call("POST", "/batches", `{"batch_no":"demo","documents":[{"document_no":"one","payload":{}}]}`, "api-token", 409)
	for _, body := range []string{`null`, `{}`, `{"unknown":1}`, `{} {}`, `{"batch_no":"bad","documents":[{"document_no":"one","status":"SUCCESS","payload":{}}]}`} {
		call("POST", "/batches", body, "api-token", 400)
	}
	call("GET", "/batches/missing", "", "api-token", 404)
	call("POST", "/batches/demo/process", "", "api-token", 200)
	ds, e := app.DocProcService.ListDocuments(context.Background(), "demo")
	if e != nil {
		t.Fatal(e)
	}
	id := ds[0].ID
	body := `{"callback_id":"demo-cb","document_id":"` + id + `","attempt_count":1,"status":"SUCCESS"}`
	call("POST", "/callbacks", body, "api-token", 401)
	call("POST", "/callbacks", body, "callback-token", 200)
	call("POST", "/callbacks", body, "callback-token", 200)
	got := call("GET", "/batches/demo", "", "api-token", 200)
	if !strings.Contains(got, `"COMPLETED"`) {
		t.Fatal(got)
	}
	got = call("GET", "/documents/"+id+"/callbacks", "", "api-token", 200)
	if strings.Count(got, "demo-cb") != 1 {
		t.Fatal(got)
	}
	call("GET", "/batches/demo/documents", "", "api-token", 200)
	call("POST", "/batches/demo/retry", "", "api-token", 200)
	call("POST", "/documents/"+id+"/process", "", "api-token", 200)
	call("POST", "/callbacks", strings.Replace(body, "SUCCESS", "PENDING", 1), "callback-token", 400)
	call("POST", "/batches", `{"batch_no":"`+strings.Repeat("x", 1048577)+`"}`, "api-token", 413)
}
