package main

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/joeblew999/irgo-windows-vm/wire"
)

// The secrets' names, from the table.
var (
	varGlazeToken      = secret(wire.ScopeGlazeWrite)
	varGoldenToken     = secret(wire.ScopeGoldenRead)
	varGoldenPushToken = secret(wire.ScopeGoldenWrite)
)

func secret(s wire.Scope) string {
	i, _ := s.Info()
	return i.Secret
}

var testNow = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

func testEnv(vars map[string]string) (Env, *memStore) {
	env, st, _ := testEnvGolden(vars)
	return env, st
}

func testEnvGolden(vars map[string]string) (Env, *memStore, *memBlobs) {
	st, gb := newMemStore(), newMemBlobs()
	return Env{
		Var:    func(n string) string { return vars[n] },
		Site:   func() (Store, error) { return st, nil },
		Golden: func() (Blobs, error) { return gb, nil },
		Now:    func() time.Time { return testNow },
	}, st, gb
}

var png1 = append([]byte("\x89PNG\r\n\x1a\n"), "one"...)

func manifestJSON(target, when string, pictures ...string) string {
	tests := make([]string, len(pictures))
	for i, p := range pictures {
		tests[i] = `{"Test":"T` + p + `","Result":"PASS","Picture":"` + p + `"}`
	}
	return `{"Target":"` + target + `","When":"` + when + `","Verdict":"YES","Tests":[` + strings.Join(tests, ",") + `]}`
}

// runForm builds the multipart body CI posts.
func runForm(t *testing.T, manifest string, files map[string][]byte) (*bytes.Buffer, string) {
	t.Helper()
	var b bytes.Buffer
	mw := multipart.NewWriter(&b)
	if manifest != "" {
		fw, _ := mw.CreateFormFile("manifest", "shots.json")
		_, _ = fw.Write([]byte(manifest))
	}
	for n, c := range files {
		fw, _ := mw.CreateFormFile(n, n)
		_, _ = fw.Write(c)
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	return &b, mw.FormDataContentType()
}

func do(h http.Handler, method, path, token string, body *bytes.Buffer, ct string) *httptest.ResponseRecorder {
	if body == nil {
		body = &bytes.Buffer{}
	}
	r := httptest.NewRequest(method, path, body)
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	if ct != "" {
		r.Header.Set("Content-Type", ct)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

// Negative control (by hand, 1 Oct 2026): disabling the token comparison in
// authorized fails five cases here and in TestGoldenRefusals ("want 401");
// restored.
func TestGlazePostRefusals(t *testing.T) {
	env, st := testEnv(map[string]string{varGlazeToken: "s3cret"})
	h := Handler(env)
	good := manifestJSON("windows", "2026-09-30T15:43:16+07:00", "A.png")
	for _, c := range []struct {
		name, token, target, manifest string
		files                         map[string][]byte
		want                          int
	}{
		{"no token", "", "windows", good, map[string][]byte{"A.png": png1}, 401},
		{"wrong token", "nope", "windows", good, map[string][]byte{"A.png": png1}, 401},
		{"unknown target", "s3cret", "linux", good, map[string][]byte{"A.png": png1}, 404},
		{"target mismatch", "s3cret", "mac", good, map[string][]byte{"A.png": png1}, 400},
		{"picture missing", "s3cret", "windows", good, nil, 400},
		{"picture not named", "s3cret", "windows", good, map[string][]byte{"A.png": png1, "B.png": png1}, 400},
		{"not a png", "s3cret", "windows", good, map[string][]byte{"A.png": []byte("GIF89a")}, 400},
		{"unsafe name", "s3cret", "windows", manifestJSON("windows", "2026-09-30T15:43:16+07:00", "../x.png"), map[string][]byte{"../x.png": png1}, 400},
		{"no manifest", "s3cret", "windows", "", map[string][]byte{"A.png": png1}, 400},
	} {
		body, ct := runForm(t, c.manifest, c.files)
		if w := do(h, "POST", "/api/glaze-status/"+c.target, c.token, body, ct); w.Code != c.want {
			t.Errorf("%s: %d %s, want %d", c.name, w.Code, w.Body, c.want)
		}
	}
	if len(st.m) != 0 {
		t.Fatalf("a refused post stored %d objects", len(st.m))
	}
}

func TestGlazePostUnconfiguredRefuses(t *testing.T) {
	env, _ := testEnv(nil)
	body, ct := runForm(t, manifestJSON("mac", "2026-09-30T15:00:00Z"), nil)
	if w := do(Handler(env), "POST", "/api/glaze-status/mac", "", body, ct); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("no secret set: %d, want 503", w.Code)
	}
}

func TestGlazeRoundTrip(t *testing.T) {
	env, _ := testEnv(map[string]string{varGlazeToken: "s3cret"})
	h := Handler(env)

	if w := do(h, "GET", "/api/glaze-status", "", nil, ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"windows":null`) {
		t.Fatalf("empty: %d %s", w.Code, w.Body)
	}

	m := manifestJSON("windows", "2026-09-30T15:43:16+07:00", "A.png")
	body, ct := runForm(t, m, map[string][]byte{"A.png": png1})
	w := do(h, "POST", "/api/glaze-status/windows", "s3cret", body, ct)
	if w.Code != http.StatusCreated {
		t.Fatalf("post: %d %s", w.Code, w.Body)
	}

	w = do(h, "GET", "/api/glaze-status", "", nil, "")
	var got wire.GlazeLatest
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	win := got["windows"]
	if win == nil || got["mac"] != nil || string(win.Manifest) != m || !win.Received.Equal(testNow) {
		t.Fatalf("latest: %s", w.Body)
	}
	pic := do(h, "GET", win.Base+"A.png", "", nil, "")
	if pic.Code != 200 || !bytes.Equal(pic.Body.Bytes(), png1) || pic.Header().Get("Content-Type") != "image/png" {
		t.Fatalf("picture: %d %q", pic.Code, pic.Header().Get("Content-Type"))
	}

	// An older run is refused and leaves the newer one in place.
	old := manifestJSON("windows", "2026-09-29T10:00:00Z")
	body, ct = runForm(t, old, nil)
	if w := do(h, "POST", "/api/glaze-status/windows", "s3cret", body, ct); w.Code != http.StatusConflict {
		t.Fatalf("older run: %d %s, want 409", w.Code, w.Body)
	}
	w = do(h, "GET", "/api/glaze-status", "", nil, "")
	if !strings.Contains(w.Body.String(), win.Run) {
		t.Fatalf("older run replaced the newer: %s", w.Body)
	}
}

func TestGlazeFileRejectsTraversal(t *testing.T) {
	env, _ := testEnv(nil)
	h := Handler(env)
	for _, p := range []string{
		"/api/glaze-status/windows/runs/0123456789abcdef/..%2Flatest.json",
		"/api/glaze-status/windows/runs/nothex/A.png",
		"/api/glaze-status/linux/runs/0123456789abcdef/A.png",
	} {
		if w := do(h, "GET", p, "", nil, ""); w.Code != 404 {
			t.Errorf("%s: %d, want 404", p, w.Code)
		}
	}
}
