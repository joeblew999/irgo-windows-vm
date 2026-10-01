package workerclient

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/joeblew999/irgo-windows-vm/wire"
)

// fakeWorker answers every route of the table with its success, recording
// which route each request matched and the bearer token it carried. A path
// the table does not have is 404.
type fakeWorker struct {
	mu   sync.Mutex
	seen map[string]string // route -> token
}

func (f *fakeWorker) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	route, _, _, ok := wire.Match(r.Method, r.URL.Path)
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	f.mu.Lock()
	f.seen[route.Name] = strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	f.mu.Unlock()
	switch route.Name {
	case wire.RouteGoldenHead:
		w.Header().Set(wire.HeaderSize, "7")
	case wire.RouteGoldenGet:
		_, _ = w.Write([]byte("bytes"))
		return
	}
	if route.Response != nil {
		w.Header().Set("Content-Type", wire.TypeJSON)
		w.WriteHeader(route.Success)
		_ = json.NewEncoder(w).Encode(route.Response)
		return
	}
	w.WriteHeader(route.Success)
}

// TestEachCallCarriesItsScopesToken: every typed call reaches its own route
// with the token of that route's scope, and no other.
//
// Negative control (by hand, 1 Oct 2026): making Header always send the
// golden-read token fails the put, delete, list and glaze cases; restored.
func TestEachCallCarriesItsScopesToken(t *testing.T) {
	f := &fakeWorker{seen: map[string]string{}}
	srv := httptest.NewServer(f)
	defer srv.Close()
	tokens := map[wire.Scope]string{}
	for _, s := range wire.Scopes {
		tokens[s.Scope] = "tok-" + string(s.Scope)
	}
	c := New(srv.URL, tokens)
	ctx := context.Background()
	key := wire.GoldenLatestKey

	if _, err := c.Health(ctx); err != nil {
		t.Fatal(err)
	}
	if info, ok, err := c.GoldenHead(ctx, key); err != nil || !ok || info.Size != 7 {
		t.Fatalf("head: %+v %v %v", info, ok, err)
	}
	if b, ok, err := c.GoldenGet(ctx, key, 100); err != nil || !ok || string(b) != "bytes" {
		t.Fatalf("get: %q %v %v", b, ok, err)
	}
	if err := c.GoldenPut(ctx, key, []byte("x"), strings.Repeat("0", 64)); err != nil {
		t.Fatal(err)
	}
	if err := c.GoldenDelete(ctx, key); err != nil {
		t.Fatal(err)
	}
	if _, err := c.GoldenList(ctx, "chunks"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.GlazeLatest(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := c.GlazePost(ctx, "mac", []byte(`{}`), map[string][]byte{"A.png": []byte("png")}); err != nil {
		t.Fatal(err)
	}
	for name, got := range f.seen {
		want := tokens[wire.MustFind(name).Scope]
		if got != want {
			t.Errorf("%s carried %q, want %q", name, got, want)
		}
	}
	if len(f.seen) < 8 {
		t.Errorf("only %d routes reached: %v", len(f.seen), f.seen)
	}
}

// TestErrorsCarryTheCode: an answer that is not the route's success is an
// *Error with the Worker's code, and a 401 names the variable and secret
// the scope needs.
func TestErrorsCarryTheCode(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(wire.Error{Error: "refused: no valid bearer token", Code: wire.CodeUnauthorized})
	}))
	defer srv.Close()
	c := New(srv.URL, nil)
	err := c.GoldenPut(context.Background(), wire.GoldenLatestKey, []byte("x"), "s")
	if !IsCode(err, wire.CodeUnauthorized) {
		t.Fatalf("got %v", err)
	}
	if !strings.Contains(err.Error(), "IRGO_GOLDEN_PUSH_TOKEN") || !strings.Contains(err.Error(), "GOLDEN_PUSH_TOKEN") {
		t.Errorf("a 401 does not name the token: %v", err)
	}
	if _, ok, err := c.GoldenHead(context.Background(), wire.GoldenLatestKey); ok || err == nil {
		t.Errorf("a 401 on HEAD read as %v %v", ok, err)
	}
}

func TestCheckOrigin(t *testing.T) {
	for s, ok := range map[string]bool{
		"https://w.example.workers.dev": true, "https://w.example.workers.dev/": true, "http://localhost:8787": true,
		"http://w.example.workers.dev": false, "https://w.example.workers.dev/api/golden": false, "https://w.example?x=1": false,
	} {
		if err := CheckOrigin(s); (err == nil) != ok {
			t.Errorf("CheckOrigin(%q) = %v", s, err)
		}
	}
}

// TestNoURLsOutsideTheClient: no code outside wire, this package and the
// Worker itself writes a Worker route's path. A grep, because the compiler
// cannot tell a URL from any other string; the paths searched for are each
// route's literal prefix up to its first parameter, from the table, so a new
// route is covered without editing this test. Comments are skipped, and so
// are test files, whose fixtures (a fake Worker, an origin that is wrongly a
// path) are not clients.
//
// Negative control (by hand, 1 Oct 2026): putting the old
// `c.WorkerURL + "/api/golden"` back into vm_golden_r2.go fails this naming
// the line; restored.
func TestNoURLsOutsideTheClient(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	var needles []string
	for _, r := range wire.Routes {
		p := strings.TrimPrefix(r.Path, "/")
		if i := strings.Index(p, "{"); i >= 0 {
			p = p[:i]
		}
		needles = append(needles, strings.TrimSuffix(p, "/"))
	}
	allowed := []string{"wire", "internal/workerclient", "worker", "docs", ".plans", ".git", ".claude", "site/dist", ".bin"}
	scanned := 0
	err = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			for _, a := range allowed {
				if rel == a {
					return filepath.SkipDir
				}
			}
			return nil
		}
		ext := filepath.Ext(rel)
		code := map[string]bool{".go": true, ".yml": true, ".yaml": true, ".sh": true, ".ps1": true, ".tmpl": true, ".js": true, ".toml": true}[ext] ||
			strings.HasPrefix(rel, "mise-tasks/")
		if !code || strings.HasSuffix(rel, "_test.go") {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		scanned++
		for i, line := range strings.Split(string(b), "\n") {
			l := strings.TrimSpace(line)
			if strings.HasPrefix(l, "//") || strings.HasPrefix(l, "#") || strings.HasPrefix(l, "<!--") {
				continue
			}
			for _, n := range needles {
				if strings.Contains(l, n) {
					t.Errorf("%s:%d writes the Worker path %q; build it with internal/workerclient (or wire.Route.URL): %s", rel, i+1, n, l)
					break
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if scanned < 50 {
		t.Fatalf("scanned only %d files from %s; this would pass vacuously", scanned, root)
	}
}
