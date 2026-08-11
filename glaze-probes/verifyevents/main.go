// Can the Events bridge replace SSE for a reactive desktop UI, with no server?
// Proves: Go->JS push (the SSE direction), JS->Go, and unsolicited Go pushes
// arriving after load — all over app://, with zero sockets.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/crgimenes/glaze"
)

const indexHTML = `<!doctype html><html><head><meta charset="utf-8"></head>
<body><ul id="log"></ul><script src="app://home/app.js"></script></body></html>`

const appJS = `
window.addEventListener('load', () => {
  const seen = [];
  // The SSE direction: server-initiated push arriving without a request.
  glaze.events.on("tick", (n, at) => {
    seen.push("tick:" + n);
    const li = document.createElement('li');
    li.textContent = 'tick ' + n + ' @ ' + at;
    document.getElementById('log').appendChild(li);
    if (seen.length === 3) {
      // Answer back over the same bridge, including what the DOM actually has.
      glaze.events.emit("done", seen, document.getElementById('log').children.length);
    }
  });
  glaze.events.emit("ready", "js-listener-installed");
});`

func main() {
	ready := make(chan string, 1)
	done := make(chan string, 1)

	w, err := glaze.NewWithOptions(glaze.Options{
		SchemeHandlers: map[string]glaze.SchemeHandler{
			"app": func(req *glaze.SchemeRequest) *glaze.SchemeResponse {
				body, mime := indexHTML, "text/html"
				if strings.HasSuffix(req.URL, "app.js") {
					body, mime = appJS, "text/javascript"
				}
				return &glaze.SchemeResponse{Body: []byte(body), MIMEType: mime}
			},
		},
	})
	if err != nil {
		fmt.Println("FAIL: NewWithOptions:", err)
		os.Exit(1)
	}
	defer w.Destroy()

	ev, err := glaze.NewEvents(w)
	if err != nil {
		fmt.Println("FAIL: NewEvents:", err)
		os.Exit(1)
	}

	ev.On("ready", func(args ...json.RawMessage) {
		ready <- string(args[0])
	})
	ev.On("done", func(args ...json.RawMessage) {
		done <- fmt.Sprintf("received=%s domChildren=%s", args[0], args[1])
	})

	w.SetTitle("glaze events")
	w.SetSize(420, 300, glaze.HintNone)
	w.Navigate("app://home/index.html")

	go func() {
		select {
		case r := <-ready:
			fmt.Println("PASS: JS -> Go   :", r)
		case <-time.After(15 * time.Second):
			fmt.Println("FAIL: JS never signalled ready")
			w.Terminate()
			return
		}

		// Unsolicited server-side pushes — what SSE would be doing.
		for i := 1; i <= 3; i++ {
			time.Sleep(300 * time.Millisecond)
			if err := ev.Emit("tick", i, time.Now().Format("15:04:05.000")); err != nil {
				fmt.Println("FAIL: Emit:", err)
			}
		}

		select {
		case d := <-done:
			fmt.Println("PASS: Go -> JS   : 3 unsolicited pushes delivered")
			fmt.Println("PASS: round trip :", d)
		case <-time.After(15 * time.Second):
			fmt.Println("FAIL: JS did not confirm receipt of pushes")
		}
		w.Terminate()
	}()

	w.Run()
}
