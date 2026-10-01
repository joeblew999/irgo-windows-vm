package wire

import (
	"os/exec"
	"strings"
	"testing"
)

// TestStandardLibraryOnly: the TinyGo Worker builds this package, so it must
// not reach a third-party module, nor regexp (see the package comment).
//
// Negative control (by hand, 1 Oct 2026): importing regexp in keys.go fails
// this naming it; restored.
func TestStandardLibraryOnly(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", ".").Output()
	if err != nil {
		t.Fatalf("go list: %v", err)
	}
	pkgs := strings.Fields(string(out))
	if len(pkgs) < 3 {
		t.Fatalf("go list returned %v; this would pass vacuously", pkgs)
	}
	for _, p := range pkgs {
		first, _, _ := strings.Cut(p, "/")
		switch {
		case p == "github.com/joeblew999/irgo-windows-vm/wire":
		case strings.Contains(first, "."):
			t.Errorf("wire reaches %s, which is not the standard library", p)
		case p == "regexp":
			t.Error("wire reaches regexp, which overflows TinyGo's stack when compiled at init")
		}
	}
}

// TestTable checks the table against itself: unique names and method+path
// pairs, every {name} described, every scope and code declared.
//
// Negative control (by hand, 1 Oct 2026): renaming golden-put's {key...} to
// {k...} fails the params check, and a second route named health fails the
// names check; restored.
func TestTable(t *testing.T) {
	names, routes := map[string]bool{}, map[string]bool{}
	codes := map[Code]bool{}
	for _, c := range Codes {
		if codes[c.Code] {
			t.Errorf("code %s declared twice", c.Code)
		}
		codes[c.Code] = true
	}
	for _, r := range Routes {
		if names[r.Name] {
			t.Errorf("route %s declared twice", r.Name)
		}
		names[r.Name] = true
		if k := r.Method + " " + r.Path; routes[k] {
			t.Errorf("%s declared twice", k)
		} else {
			routes[k] = true
		}
		if !strings.HasPrefix(r.Path, Prefix) {
			t.Errorf("%s: %s is not under %s, which is all the Worker is sent", r.Name, r.Path, Prefix)
		}
		if r.Summary == "" || r.Success == 0 {
			t.Errorf("%s: no summary or no success status", r.Name)
		}
		if r.Scope != ScopeNone {
			if _, ok := r.Scope.Info(); !ok {
				t.Errorf("%s: scope %q is not in Scopes", r.Name, r.Scope)
			}
		}
		if (r.RequestType != "") != (r.MaxBody > 0) {
			t.Errorf("%s: a body needs a limit, and a limit a body", r.Name)
		}
		if (r.Response != nil) != (r.ResponseType == TypeJSON && r.Name != RouteOpenAPI) {
			t.Errorf("%s: a JSON answer needs its Go type, and only a JSON answer has one", r.Name)
		}
		for _, c := range r.Errs() {
			if !codes[c] {
				t.Errorf("%s: code %s is not in Codes", r.Name, c)
			}
		}
		var inPath []string
		for _, seg := range strings.Split(r.Path, "/") {
			if strings.HasPrefix(seg, "{") {
				inPath = append(inPath, strings.TrimSuffix(strings.TrimSuffix(strings.TrimPrefix(seg, "{"), "}"), "..."))
			}
		}
		var described []string
		for _, p := range r.Params {
			if p.In == "path" {
				described = append(described, p.Name)
			}
		}
		if strings.Join(inPath, ",") != strings.Join(described, ",") {
			t.Errorf("%s: path parameters %v, described %v", r.Name, inPath, described)
		}
	}
}

// TestMatch: every route is found at its own URL and nowhere else, a path
// with the wrong method lists the right ones, and a path that is no route's
// is not found.
//
// Negative control (by hand, 1 Oct 2026): dropping the length check at the
// end of matchPath fails the "extra segment" case; restored.
func TestMatch(t *testing.T) {
	h64 := strings.Repeat("ab", 32)
	jobID := strings.Repeat("0f", 16)
	sample := map[string][]string{
		RouteGlazePost: {"windows"}, RouteGlazeFile: {"mac", "0123456789abcdef", "A.png"},
		RouteGoldenHead: {GoldenChunkKey(h64)}, RouteGoldenGet: {GoldenChunkKey(h64)},
		RouteGoldenPut: {GoldenManifestKey(h64)}, RouteGoldenDelete: {GoldenLatestKey},
		RouteGoldenList: {"chunks"},
		RouteJobInput:   {jobID}, RouteJobGet: {jobID}, RouteJobLog: {jobID}, RouteJobFile: {jobID, "desktop.png"},
		RouteJobCancel: {jobID}, RouteRunnerHeartbeat: {jobID}, RouteRunnerInput: {jobID}, RouteRunnerLog: {jobID},
		RouteRunnerFile: {jobID, "desktop.png"}, RouteRunnerFinish: {jobID},
	}
	for _, r := range Routes {
		u := r.URL("", sample[r.Name]...)
		got, params, _, ok := Match(r.Method, u)
		if !ok || got.Name != r.Name {
			t.Errorf("%s %s matched %q (%v)", r.Method, u, got.Name, ok)
			continue
		}
		if strings.Join(params, "|") != strings.Join(sample[r.Name], "|") {
			t.Errorf("%s: params %q, want %q", r.Name, params, sample[r.Name])
		}
	}
	for _, c := range []struct {
		method, path string
		route        string
		allowed      string
	}{
		{"GET", "/api/golden/", RouteGoldenGet, ""}, // empty key: the handler, behind the token, says 404
		{"GET", "/api/golden", "", ""},
		{"POST", "/api/golden/golden/latest", "", "HEAD,GET,PUT,DELETE"},
		{"POST", "/api/health", "", "GET"},
		{"GET", "/api/health/x", "", ""}, // extra segment
		{"GET", "/api/glaze-status/windows/runs/x", "", ""},
		{"GET", "/api/nope", "", ""},
		{"GET", "/health", "", ""},
	} {
		got, _, allowed, ok := Match(c.method, c.path)
		if ok != (c.route != "") || got.Name != c.route || strings.Join(allowed, ",") != c.allowed {
			t.Errorf("%s %s: %q %v allowed %v; want %q allowed %q", c.method, c.path, got.Name, ok, allowed, c.route, c.allowed)
		}
	}
}

func TestURLEscapes(t *testing.T) {
	r := MustFind(RouteGlazeFile)
	if got := r.URL("https://w.example", "mac", "0123456789abcdef", "a b.png"); got != "https://w.example/api/glaze-status/mac/runs/0123456789abcdef/a%20b.png" {
		t.Errorf("got %s", got)
	}
	if got := MustFind(RouteGoldenGet).URL("", "golden/latest"); got != "/api/golden/golden/latest" {
		t.Errorf("a {key...} keeps its slashes: %s", got)
	}
	if got := MustFind(RouteGlazeFile).URL("", "mac", "x", ""); got != "/api/glaze-status/mac/runs/x/" {
		t.Errorf("an empty last parameter: %s", got)
	}
	defer func() {
		if recover() == nil {
			t.Error("too few parameters did not panic")
		}
	}()
	r.URL("", "mac")
}

func TestValidators(t *testing.T) {
	h64 := strings.Repeat("0a", 32)
	for k, want := range map[string]bool{
		"golden/latest": true, "golden/manifests/" + h64 + ".json": true, "golden/chunks/" + h64 + ".zst": true,
		"golden/": false, "golden/latest/": false, "golden/chunks/" + h64 + ".json": false,
		"golden/chunks/" + strings.ToUpper(h64) + ".zst": false, "golden/chunks/" + h64[1:] + ".zst": false,
		"other/latest": false, "golden/manifests/../latest": false,
	} {
		if IsGoldenKey(k) != want {
			t.Errorf("IsGoldenKey(%q) = %v", k, !want)
		}
	}
	for p, want := range map[string]bool{
		"TestTray_running.png": true, "a.b-c.png": true,
		".png": false, ".hidden.png": false, "../x.png": false, "a/b.png": false, "x.PNG": false,
		strings.Repeat("a", 101) + ".png": false, "x.png.exe": false,
	} {
		if IsPicture(p) != want {
			t.Errorf("IsPicture(%q) = %v", p, !want)
		}
	}
	for k, prefix := range GoldenListKinds {
		if !strings.HasPrefix(prefix, GoldenPrefix) || !strings.HasSuffix(prefix, k+"/") {
			t.Errorf("list kind %s is %s", k, prefix)
		}
	}
}
