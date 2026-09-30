//go:build darwin || windows

package conformance

import (
	"encoding/json"
	"net/url"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/crgimenes/glaze"
)

// The portless path: an app:// scheme handler serves a page and its
// sub-resources, the page runs, and its script calls Go. No TCP anywhere.
//
// The sub-resources are named two ways, deliberately. An ABSOLUTE app:// URL is
// the obvious way to reference an asset and the way the scheme is documented.
// A RELATIVE one resolves against whatever origin the document actually ended
// up on.
//
// On macOS both work, because WKWebView registers `app` as a real scheme. On
// Windows they differ, and the difference is the finding: glaze emulates the
// scheme with a virtual host, so the document loads from https://app.localhost/
// and an absolute `app://home/abs.js` inside it names a scheme WebView2 has
// never heard of. It fails silently — no error, no console message, just a page
// with no stylesheet and no script. That is docs/UPSTREAM.md §1b, and
// TestAppScheme/absolute_subresources FAILS on Windows until glaze fixes it.
// It is not skipped: a skip would read as "not applicable", and it is the one
// thing about this path that is broken.
//
// Each stylesheet sets a different property to a value nothing else sets, so
// "was it applied" is a computed style, not a guess.
const schemeIndex = `<!doctype html>
<html><head><meta charset="utf-8"><title>app scheme</title>
<link rel="stylesheet" href="app://home/abs.css">
<link rel="stylesheet" href="/rel.css"></head>
<body><h1 id="h">checking…</h1>
<script src="app://home/abs.js"></script>
<script src="/rel.js"></script>
<script>
// Not named origin: window.origin is a built-in, and a function declaration
// of that name leaves origin a string, so calling it threw before the page
// could report anything.
function originCapabilities() {
  const r = { origin: location.origin, secureContext: window.isSecureContext };
  try { localStorage.setItem('k', 'v'); r.localStorage = localStorage.getItem('k') === 'v' ? 'ok' : 'wrong value'; }
  catch (e) { r.localStorage = 'threw ' + e.name; }
  try { history.pushState({}, '', '/deep/link/route'); r.pushState = location.pathname; history.back(); }
  catch (e) { r.pushState = 'threw ' + e.name; }
  return r;
}
// load fires once every sub-resource has loaded or failed, so both stylesheets
// have been applied by now, or never will be.
window.addEventListener('load', async () => {
  // Everything inside the try, so a script error reaches Go as a message
  // instead of leaving the test to time out saying nothing.
  try {
    const s = getComputedStyle(document.body);
    const r = Object.assign(originCapabilities(), {
      absScript: !!window.__abs, relScript: !!window.__rel,
      absCSS: s.paddingLeft, relCSS: s.marginLeft,
    });
    const token = await report(JSON.stringify(r));
    await handBack(token);
  } catch (e) {
    await handBack('JS error: ' + e);
  }
});
</script>
</body></html>`

const (
	absCSS = `body { padding-left: 11px; }`
	relCSS = `body { margin-left: 13px; }`
	absJS  = `window.__abs = true;`
	relJS  = `window.__rel = true;`
	// token is what report returns to JS and JS hands back through handBack: a
	// Go value that crossed into the page and came back, which is the
	// Go -> JS half of the binding.
	//
	// handBack was first called confirm. Bind("confirm") returned nil, and the
	// page went on calling the browser's own window.confirm, so Go never heard
	// back: a binding that shadows a window built-in is silently not installed
	// (seen on macOS, glaze v0.0.61; not yet reported upstream).
	token = "irgo-round-trip-7f3a"
)

type pageReport struct {
	Origin        string `json:"origin"`
	SecureContext bool   `json:"secureContext"`
	LocalStorage  string `json:"localStorage"`
	PushState     string `json:"pushState"`
	AbsScript     bool   `json:"absScript"`
	RelScript     bool   `json:"relScript"`
	AbsCSS        string `json:"absCSS"`
	RelCSS        string `json:"relCSS"`
}

func TestAppScheme(t *testing.T) {
	var (
		mu     sync.Mutex
		served = map[string]bool{} // path the handler was asked for
	)
	reports := make(chan pageReport, 1)
	confirms := make(chan string, 1)

	openWindow(t, glaze.Options{
		SchemeHandlers: map[string]glaze.SchemeHandler{
			"app": func(req *glaze.SchemeRequest) *glaze.SchemeResponse {
				u, err := url.Parse(req.URL)
				path := req.URL
				if err == nil {
					path = u.Path
				}
				mu.Lock()
				served[path] = true
				mu.Unlock()
				body, mime := schemeIndex, "text/html"
				switch path {
				case "/abs.css":
					body, mime = absCSS, "text/css"
				case "/rel.css":
					body, mime = relCSS, "text/css"
				case "/abs.js":
					body, mime = absJS, "text/javascript"
				case "/rel.js":
					body, mime = relJS, "text/javascript"
				}
				return &glaze.SchemeResponse{Body: []byte(body), MIMEType: mime}
			},
		},
	}, func(w glaze.WebView) {
		if err := w.Bind("report", func(raw string) (string, error) {
			var r pageReport
			if err := json.Unmarshal([]byte(raw), &r); err != nil {
				t.Errorf("the page reported something that is not its JSON: %q: %v", raw, err)
			}
			reports <- r
			return token, nil
		}); err != nil {
			t.Errorf("Bind(report): %v", err)
		}
		if err := w.Bind("handBack", func(s string) {
			confirms <- s
		}); err != nil {
			t.Errorf("Bind(handBack): %v", err)
		}
		w.Navigate("app://home/index.html")
	})
	if t.Failed() {
		t.FailNow()
	}

	var r pageReport
	select {
	case r = <-reports:
	case msg := <-confirms:
		t.Fatalf("the page called handBack before report, which is its error path: %s", msg)
	case <-time.After(20 * time.Second):
		mu.Lock()
		defer mu.Unlock()
		t.Fatalf("the page never called Go within 20s; the scheme handler was asked for %v", keys(served))
	}
	wasServed := func(p string) bool {
		mu.Lock()
		defer mu.Unlock()
		return served[p]
	}

	t.Run("js_calls_go", func(t *testing.T) {
		// Reaching here means the page loaded over app:// and its script
		// called a bound Go function. What remains is the value going back.
		select {
		case got := <-confirms:
			if got != token {
				t.Fatalf("JS handed back %q, want the %q report returned to it", got, token)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("report returned to JS, and JS never called handBack with the result")
		}
	})

	t.Run("relative_subresources", func(t *testing.T) {
		if !r.RelScript {
			t.Errorf("/rel.js did not run (the handler was asked for it: %t)", wasServed("/rel.js"))
		}
		if r.RelCSS != "13px" {
			t.Errorf("/rel.css not applied: body margin-left is %q, want 13px (the handler was asked for it: %t)", r.RelCSS, wasServed("/rel.css"))
		}
	})

	t.Run("absolute_subresources", func(t *testing.T) {
		if !r.AbsScript {
			t.Errorf("app://home/abs.js did not run; the scheme handler was asked for it: %t. The document's origin is %s — "+
				"on Windows this is glaze bug docs/UPSTREAM.md §1b: the scheme is emulated with a virtual host, so an absolute app:// "+
				"URL inside the page names a scheme WebView2 does not know", wasServed("/abs.js"), r.Origin)
		}
		if r.AbsCSS != "11px" {
			t.Errorf("app://home/abs.css not applied: body padding-left is %q, want 11px (the handler was asked for it: %t)", r.AbsCSS, wasServed("/abs.css"))
		}
	})

	// What decides whether a client-side-routed single-page app works on this
	// origin at all. verify printed these and asserted none of them.
	t.Run("origin_capabilities", func(t *testing.T) {
		if !r.SecureContext {
			t.Errorf("window.isSecureContext is false on %s; glaze registers the scheme as a secure context", r.Origin)
		}
		if r.LocalStorage != "ok" {
			t.Errorf("localStorage on %s: %s", r.Origin, r.LocalStorage)
		}
		if r.PushState != "/deep/link/route" {
			t.Errorf("history.pushState on %s: location.pathname became %q, want /deep/link/route", r.Origin, r.PushState)
		}
	})
}

func keys(m map[string]bool) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	if out == nil {
		return []string{"nothing"}
	}
	slices.Sort(out)
	return out
}
