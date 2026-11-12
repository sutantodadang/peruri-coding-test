package peruri_test

import (
	"context"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

func TestOpenAPIDocumentation(t *testing.T) {
	h := newHandler(t, &fakeApplication{}, "api", "callback", healthFunc(func(context.Context) error { return nil }))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/openapi.json", nil))
	if w.Code != 200 || !strings.Contains(w.Header().Get("Content-Type"), "application/json") {
		t.Fatalf("specification unavailable: %d %s", w.Code, w.Body.String())
	}
	var spec map[string]any
	check(t, json.Unmarshal(w.Body.Bytes(), &spec))
	if spec["openapi"] != "3.0.3" || spec["servers"].([]any)[0].(map[string]any)["url"] != "/" {
		t.Fatal("OpenAPI version or same-origin server changed")
	}
	paths := spec["paths"].(map[string]any)
	documented := make(map[string]bool)
	ids := make(map[string]bool)
	for path, item := range paths {
		for method, value := range item.(map[string]any) {
			if method == "parameters" {
				continue
			}
			op := value.(map[string]any)
			id := op["operationId"].(string)
			if ids[id] {
				t.Fatalf("duplicate operation ID: %s", id)
			}
			ids[id] = true
			documented[strings.ToUpper(method)+" "+path] = true
			security := spec["security"]
			if override, ok := op["security"]; ok {
				security = override
			}
			schemes := security.([]any)
			want := "ApiToken"
			if path == "/callbacks" {
				want = "CallbackToken"
			}
			if path == "/healthz" {
				if len(schemes) != 0 {
					t.Fatal("health must be public")
				}
			} else if len(schemes) != 1 || schemes[0].(map[string]any)[want] == nil {
				t.Fatalf("incorrect authentication for %s", path)
			}
		}
	}
	file, err := parser.ParseFile(token.NewFileSet(), "../internal/http/route/DocProcRoute.go", nil, 0)
	check(t, err)
	registered := make(map[string]bool)
	ast.Inspect(file, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok || len(call.Args) == 0 {
			return true
		}
		selector, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || (selector.Sel.Name != "Handle" && selector.Sel.Name != "HandleFunc") {
			return true
		}
		literal, ok := call.Args[0].(*ast.BasicLit)
		if !ok {
			t.Fatal("route pattern must be a literal for documentation audit")
		}
		pattern, err := strconv.Unquote(literal.Value)
		check(t, err)
		if pattern != "GET /docs/" && pattern != "GET /openapi.json" {
			registered[pattern] = true
		}
		return true
	})
	if len(documented) != len(registered) {
		t.Fatalf("documented routes=%d registered=%d", len(documented), len(registered))
	}
	for pattern := range registered {
		if !documented[pattern] {
			t.Errorf("undocumented endpoint: %s", pattern)
		}
	}
	var checkRefs func(any)
	checkRefs = func(value any) {
		switch value := value.(type) {
		case map[string]any:
			if ref, ok := value["$ref"].(string); ok {
				var target any = spec
				for _, segment := range strings.Split(strings.TrimPrefix(ref, "#/"), "/") {
					object, ok := target.(map[string]any)
					if !ok || object[segment] == nil {
						t.Fatalf("unresolved OpenAPI reference: %s", ref)
					}
					target = object[segment]
				}
			}
			for _, child := range value {
				checkRefs(child)
			}
		case []any:
			for _, child := range value {
				checkRefs(child)
			}
		}
	}
	checkRefs(spec)
	for _, path := range []string{"/docs/", "/docs/swagger-ui.css", "/docs/swagger-ui-bundle.js", "/docs/swagger-ui-standalone-preset.js"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != 200 || w.Body.Len() < 100 || w.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Fatalf("embedded documentation asset unavailable: %s (%d)", path, w.Code)
		}
		if path == "/docs/" && (!strings.Contains(w.Body.String(), "/openapi.json") || !strings.Contains(w.Body.String(), "validatorUrl: null") || !strings.Contains(w.Body.String(), "persistAuthorization: false")) {
			t.Fatal("UI lost its local spec, disabled external validator, or session-only credentials")
		}
	}
}
