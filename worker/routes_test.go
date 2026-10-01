package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/joeblew999/irgo-windows-vm/wire"
	"github.com/joeblew999/irgo-windows-vm/wire/openapi"
)

// TestHandlersAreTheTable: one handler per route in wire.Routes, and no
// handler for a route the table does not have.
//
// Negative control (by hand, 1 Oct 2026): deleting the golden-list entry
// from handlers fails this naming it, and so does adding one for "nope";
// restored.
func TestHandlersAreTheTable(t *testing.T) {
	if err := checkHandlers(); err != nil {
		t.Fatal(err)
	}
	if len(handlers) != len(wire.Routes) {
		t.Fatalf("%d handlers, %d routes", len(handlers), len(wire.Routes))
	}
}

// sampleParams are path parameters each route accepts, so a request reaches
// the dispatcher's checks and then the handler.
const sampleJob = "0123456789abcdef0123456789abcdef"

var sampleParams = map[string][]string{
	wire.RouteGlazePost:       {"windows"},
	wire.RouteGlazeFile:       {"mac", "0123456789abcdef", "A.png"},
	wire.RouteGoldenHead:      {wire.GoldenLatestKey},
	wire.RouteGoldenGet:       {wire.GoldenLatestKey},
	wire.RouteGoldenPut:       {wire.GoldenLatestKey},
	wire.RouteGoldenDelete:    {wire.GoldenLatestKey},
	wire.RouteGoldenList:      {"chunks"},
	wire.RouteJobInput:        {sampleJob},
	wire.RouteJobGet:          {sampleJob},
	wire.RouteJobLog:          {sampleJob},
	wire.RouteJobFile:         {sampleJob, "desktop.png"},
	wire.RouteJobAdminFile:    {sampleJob, "desktop.png"},
	wire.RouteJobCancel:       {sampleJob},
	wire.RouteRunnerHeartbeat: {sampleJob},
	wire.RouteRunnerInput:     {sampleJob},
	wire.RouteRunnerLog:       {sampleJob},
	wire.RouteRunnerFile:      {sampleJob, "desktop.png"},
	wire.RouteRunnerFinish:    {sampleJob},
}

// TestEveryRouteEnforcesItsScope: the Worker serves exactly the table's
// routes, each behind exactly its own scope. Each handler is replaced by a
// spy, so this tests the routing and the dispatcher's checks, not what a
// handler does. For every route: its URL reaches its own handler with its own
// token; with no token, a wrong one, or every other scope's token it is 401
// and the handler never runs; with its secret unset it is 503. A path no
// route has is 404, and a known path with another method 405, before any
// handler.
//
// Negative control (by hand, 1 Oct 2026): making authorized return true for
// every scope fails the "other scope" and "no token" cases of every golden
// route and of glaze-post; a spy for glaze-latest reporting health fails
// "reached"; restored each. This takes each route's scope from the table, so
// it cannot catch a wrong scope in the table itself: TestGoldenRefusals and
// TestGlazePostRefusals pin those literally (giving golden-list
// ScopeGoldenRead fails TestGoldenRefusals, checked the same day).
func TestEveryRouteEnforcesItsScope(t *testing.T) {
	saved := handlers
	defer func() { handlers = saved }()
	var reached []string
	handlers = map[string]handlerFunc{}
	for name := range saved {
		handlers[name] = func(_ Env, w http.ResponseWriter, _ *http.Request, _ []string) {
			reached = append(reached, name)
			w.WriteHeader(299)
		}
	}

	tokens := map[wire.Scope]string{}
	vars := map[string]string{}
	for _, s := range wire.Scopes {
		tokens[s.Scope] = "token-" + string(s.Scope)
		vars[s.Secret] = tokens[s.Scope]
	}
	env, _, _ := testEnvGolden(vars)
	h := Handler(env)

	send := func(r wire.Route, token string, vars map[string]string) (int, []string) {
		reached = nil
		hh := h
		if vars != nil {
			e, _, _ := testEnvGolden(vars)
			hh = Handler(e)
		}
		req := httptest.NewRequest(r.Method, r.URL("", sampleParams[r.Name]...), bytes.NewReader([]byte("x")))
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		hh.ServeHTTP(w, req)
		return w.Code, reached
	}

	for _, r := range wire.Routes {
		code, got := send(r, tokens[r.Scope], nil)
		if code != 299 || len(got) != 1 || got[0] != r.Name {
			t.Errorf("%s %s with its token: %d, reached %v", r.Method, r.Path, code, got)
		}
		if r.Scope == wire.ScopeNone {
			if code, got := send(r, "", nil); code != 299 || len(got) != 1 {
				t.Errorf("%s needs no token, and without one got %d", r.Name, code)
			}
			continue
		}
		if code, got := send(r, "", nil); code != http.StatusUnauthorized || got != nil {
			t.Errorf("%s with no token: %d, reached %v", r.Name, code, got)
		}
		if code, got := send(r, "wrong", nil); code != http.StatusUnauthorized || got != nil {
			t.Errorf("%s with a wrong token: %d, reached %v", r.Name, code, got)
		}
		for s, tok := range tokens {
			if s == r.Scope {
				continue
			}
			if code, got := send(r, tok, nil); code != http.StatusUnauthorized || got != nil {
				t.Errorf("%s with the %s token: %d, reached %v", r.Name, s, code, got)
			}
		}
		info, _ := r.Scope.Info()
		unset := map[string]string{}
		for k, v := range vars {
			if k != info.Secret {
				unset[k] = v
			}
		}
		if code, got := send(r, tokens[r.Scope], unset); code != http.StatusServiceUnavailable || got != nil {
			t.Errorf("%s with %s unset: %d, reached %v", r.Name, info.Secret, code, got)
		}
	}

	for _, c := range []struct {
		method, path string
		want         int
	}{
		{"GET", "/api/nope", 404},
		{"GET", "/api/health/extra", 404},
		{"GET", "/api/golden", 404},
		{"GET", "/", 404},
		{"POST", "/api/health", 405},
		{"PATCH", "/api/golden/golden/latest", 405},
		{"HEAD", "/api/glaze-status", 405},
	} {
		reached = nil
		w := httptest.NewRecorder()
		req := httptest.NewRequest(c.method, c.path, nil)
		req.Header.Set("Authorization", "Bearer "+tokens[wire.ScopeGoldenWrite])
		h.ServeHTTP(w, req)
		if w.Code != c.want || reached != nil {
			t.Errorf("%s %s: %d, reached %v; want %d", c.method, c.path, w.Code, reached, c.want)
		}
		if c.want == 405 && w.Header().Get("Allow") == "" {
			t.Errorf("%s %s: 405 with no Allow", c.method, c.path)
		}
	}
}

// TestBodyLimitsFromTheTable: a route's MaxBody and NeedLength are enforced
// before its handler.
//
// Negative control (by hand, 1 Oct 2026): removing the MaxBody check from
// Handler fails the 413 case; restored.
func TestBodyLimitsFromTheTable(t *testing.T) {
	env, _, gb := testEnvGolden(goldenVars)
	h := Handler(env)
	r := wire.MustFind(wire.RouteGoldenPut)
	req := httptest.NewRequest(r.Method, r.URL("", wire.GoldenLatestKey), strings.NewReader("x"))
	req.Header.Set("Authorization", "Bearer push")
	req.ContentLength = r.MaxBody + 1
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusRequestEntityTooLarge || !strings.Contains(w.Body.String(), string(wire.CodeTooLarge)) {
		t.Errorf("over the limit: %d %s", w.Code, w.Body)
	}
	req.ContentLength = -1
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusLengthRequired {
		t.Errorf("no length: %d %s", w.Code, w.Body)
	}
	if len(gb.m) != 0 {
		t.Errorf("a refused put stored %d objects", len(gb.m))
	}
}

// TestOpenAPIIsCurrent: the document the Worker embeds is what wire/openapi
// makes of the table now.
//
// Negative control (by hand, 1 Oct 2026): editing a summary in wire.Routes
// without regenerating fails this; restored.
func TestOpenAPIIsCurrent(t *testing.T) {
	want, err := openapi.Document()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(openapiJSON, want) {
		if os.Getenv("UPDATE_OPENAPI") != "" {
			if err := os.WriteFile("openapi.json", want, 0o644); err != nil {
				t.Fatal(err)
			}
			return
		}
		t.Fatal("worker/openapi.json is stale: run `mise run worker:wasm` (or `go -C worker run ./cmd/openapi openapi.json`)")
	}
	env, _ := testEnv(nil)
	w := do(Handler(env), "GET", wire.MustFind(wire.RouteOpenAPI).URL(""), "", nil, "")
	if w.Code != 200 || !bytes.Equal(w.Body.Bytes(), want) || w.Header().Get("Content-Type") != wire.TypeJSON {
		t.Fatalf("GET /api/openapi.json: %d %q", w.Code, w.Header().Get("Content-Type"))
	}
}
