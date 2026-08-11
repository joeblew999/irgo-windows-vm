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

const indexHTML = `<!doctype html>
<html><head><meta charset="utf-8"><title>verify</title>
<link rel="stylesheet" href="app://home/app.css"></head>
<body><h1 id="h">checking…</h1>
<script src="app://home/app.js"></script>
</body></html>`

const appCSS = `body { font-family: system-ui; padding: 2rem; }`

// Exercises a sub-resource load from the same origin, then probes the origin
// capabilities that decide whether client-side routing works, then the binding.
const appJS = `
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
			"index.html": strings.ReplaceAll(indexHTML, "app://home/", ""),
			"app.css":    appCSS,
			"app.js":     appJS,
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
