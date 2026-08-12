// Verifies the portless path end to end: an app:// scheme handler serves the
// page, the page runs JS, and the JS calls back into Go. No TCP anywhere.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/crgimenes/glaze"
)

// Two ways of naming the same sub-resources, deliberately.
//
// An ABSOLUTE app:// URL is the obvious way to reference an asset and the way
// the scheme is documented. A RELATIVE one resolves against whatever origin the
// document actually ended up on.
//
// On macOS both work, because WKWebView registers `app` as a real scheme. On
// Windows they differ, and the difference is the whole finding: glaze emulates
// the scheme with a virtual host, so the document loads from
// https://app.localhost/ and an absolute `app://home/app.js` inside it names a
// scheme WebView2 has never heard of. It fails silently — no error, no console
// message, just a page with no stylesheet and no script.
//
// Loading both means the report says WHICH failed rather than "timed out".
const indexHTML = `<!doctype html>
<html><head><meta charset="utf-8"><title>verify</title>
<link rel="stylesheet" href="app://home/app.css">
<link rel="stylesheet" href="/rel.css"></head>
<body><h1 id="h">checking…</h1>
<script src="app://home/app.js"></script>
<script src="/rel.js"></script>
</body></html>`

// relCSS and relJS are the same assets under relative names. If the absolute
// pair never arrives, these still run and report that fact instead of leaving
// the probe to time out with nothing to say.
const relCSS = `body { margin-left: 3rem; }`

const relJS = `
window.__relLoaded = true;
window.addEventListener('load', () => {
  // Give the absolute-URL script a moment to win the race, then report if it
  // never loaded at all.
  setTimeout(() => {
    if (!window.__absLoaded) {
      report("ABSOLUTE-SUBRESOURCES-FAILED", -1, JSON.stringify(probeRel()));
    }
  }, 3000);
});
function probeRel() {
  return {
    origin: location.origin,
    href: location.href,
    secureContext: window.isSecureContext,
    absoluteAppURLsLoaded: !!window.__absLoaded,
    relativeURLsLoaded: true,
    css: getComputedStyle(document.body).marginLeft,
  };
}`

const appCSS = `body { font-family: system-ui; padding: 2rem; }`

// Exercises a sub-resource load from the same origin, then probes the origin
// capabilities that decide whether client-side routing works, then the binding.
const appJS = `
window.__absLoaded = true;
function probe() {
  const r = {};
  r.origin = location.origin;
  r.href = location.href;
  r.secureContext = window.isSecureContext;
  r.cryptoSubtle = !!(window.crypto && window.crypto.subtle);
  try { localStorage.setItem('k','v'); r.localStorage = localStorage.getItem('k') === 'v'; }
    catch (e) { r.localStorage = 'BLOCKED: ' + e.name; }
  try { history.pushState({}, '', '/deep/link/route'); r.pushState = location.pathname; history.back(); }
    catch (e) { r.pushState = 'BLOCKED: ' + e.name; }
  r.css = getComputedStyle(document.body).paddingLeft;
  return r;
}
window.addEventListener('load', async () => {
  try {
    const res = await report("hello-from-js", 42, JSON.stringify(probe()));
    document.getElementById('h').textContent = 'ok: ' + res;
  } catch (e) {
    await report("JS-ERROR:" + e, -1, "");
  }
});`

func main() {
	done := make(chan string, 1)

	w, err := glaze.NewWithOptions(glaze.Options{
		SchemeHandlers: map[string]glaze.SchemeHandler{
			"app": func(req *glaze.SchemeRequest) *glaze.SchemeResponse {
				body, mime := indexHTML, "text/html"
				switch {
				case strings.HasSuffix(req.URL, "app.js"):
					body, mime = appJS, "text/javascript"
				case strings.HasSuffix(req.URL, "app.css"):
					body, mime = appCSS, "text/css"
				case strings.HasSuffix(req.URL, "rel.js"):
					body, mime = relJS, "text/javascript"
				case strings.HasSuffix(req.URL, "rel.css"):
					body, mime = relCSS, "text/css"
				}
				fmt.Println("  scheme handler served:", req.URL, "->", mime)
				return &glaze.SchemeResponse{Body: []byte(body), MIMEType: mime}
			},
		},
	})
	if err != nil {
		fmt.Println("FAIL: NewWithOptions:", err)
		os.Exit(1)
	}
	defer w.Destroy()

	if err := w.Bind("report", func(msg string, n int, caps string) (string, error) {
		var m map[string]any
		if json.Unmarshal([]byte(caps), &m) == nil {
			var b strings.Builder
			fmt.Fprintf(&b, "msg=%q n=%d\n", msg, n)
			for _, k := range []string{"origin", "href", "secureContext", "cryptoSubtle", "localStorage", "pushState", "css"} {
				fmt.Fprintf(&b, "    %-14s %v\n", k+":", m[k])
			}
			done <- b.String()
		} else {
			done <- fmt.Sprintf("msg=%q n=%d raw=%s", msg, n, caps)
		}
		return "round-trip-complete", nil
	}); err != nil {
		fmt.Println("FAIL: Bind:", err)
		os.Exit(1)
	}

	w.SetTitle("glaze verify")
	w.SetSize(480, 240, glaze.HintNone)

	// "file" stages the same assets in a temp dir and loads them over file://,
	// the way the zero_tcp example does, so the two origins can be compared.
	if len(os.Args) > 1 && os.Args[1] == "file" {
		dir, err := os.MkdirTemp("", "glaze-verify-*")
		if err != nil {
			fmt.Println("FAIL: temp dir:", err)
			os.Exit(1)
		}
		defer os.RemoveAll(dir)
		for name, body := range map[string]string{
			"index.html": strings.ReplaceAll(strings.ReplaceAll(indexHTML, "app://home/", ""), `href="/`, `href="`),
			"app.css":    appCSS,
			"app.js":     appJS,
			"rel.css":    relCSS,
			"rel.js":     relJS,
		} {
			if err := os.WriteFile(dir+"/"+name, []byte(body), 0o600); err != nil {
				fmt.Println("FAIL: write:", err)
				os.Exit(1)
			}
		}
		fmt.Println("  loading over file:// from", dir)
		w.Navigate("file://" + dir + "/index.html")
	} else {
		w.Navigate("app://home/index.html")
	}

	go func() {
		select {
		case got := <-done:
			fmt.Println("PASS: JS -> Go round trip:", got)
		case <-time.After(20 * time.Second):
			fmt.Println("FAIL: timed out waiting for JS to call Go")
		}
		w.Terminate()
	}()

	w.Run()
}
