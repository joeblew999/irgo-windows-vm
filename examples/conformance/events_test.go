//go:build darwin || windows

package conformance

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/crgimenes/glaze"
)

// Can the Events bridge replace SSE for a reactive desktop UI, with no server?
// JS -> Go, unsolicited Go -> JS pushes arriving after load (the SSE
// direction), and the page answering back with what its DOM actually holds.
//
// The script is referenced RELATIVELY, not as app://home/app.js, so this
// measures the Events bridge rather than re-measuring docs/UPSTREAM.md §1b,
// which TestAppScheme/absolute_subresources owns.
const eventsIndex = `<!doctype html><html><head><meta charset="utf-8"></head>
<body><ul id="log"></ul><script src="/events.js"></script></body></html>`

const eventsJS = `
window.addEventListener('load', () => {
  const seen = [];
  glaze.events.on("tick", (n) => {
    seen.push(n);
    const li = document.createElement('li');
    li.textContent = 'tick ' + n;
    document.getElementById('log').appendChild(li);
    if (seen.length === 3) {
      glaze.events.emit("done", seen, document.getElementById('log').children.length);
    }
  });
  glaze.events.emit("ready", "js-listener-installed");
});`

func TestEvents(t *testing.T) {
	ready := make(chan string, 1)
	type done struct {
		seen []int
		dom  int
	}
	dones := make(chan done, 1)

	var ev *glaze.Events
	openWindow(t, glaze.Options{
		SchemeHandlers: map[string]glaze.SchemeHandler{
			"app": func(req *glaze.SchemeRequest) *glaze.SchemeResponse {
				if strings.HasSuffix(req.URL, "/events.js") {
					return &glaze.SchemeResponse{Body: []byte(eventsJS), MIMEType: "text/javascript"}
				}
				return &glaze.SchemeResponse{Body: []byte(eventsIndex), MIMEType: "text/html"}
			},
		},
	}, func(w glaze.WebView) {
		var err error
		if ev, err = glaze.NewEvents(w); err != nil {
			t.Errorf("glaze.NewEvents: %v", err)
			return
		}
		ev.On("ready", func(args ...json.RawMessage) {
			var s string
			if len(args) > 0 {
				_ = json.Unmarshal(args[0], &s)
			}
			ready <- s
		})
		ev.On("done", func(args ...json.RawMessage) {
			var d done
			if len(args) != 2 {
				t.Errorf("done arrived with %d arguments, want 2", len(args))
			} else {
				if err := json.Unmarshal(args[0], &d.seen); err != nil {
					t.Errorf("done's first argument %s: %v", args[0], err)
				}
				if err := json.Unmarshal(args[1], &d.dom); err != nil {
					t.Errorf("done's second argument %s: %v", args[1], err)
				}
			}
			dones <- d
		})
		w.Navigate("app://home/index.html")
	})
	if t.Failed() {
		t.FailNow()
	}

	t.Run("js_to_go", func(t *testing.T) {
		select {
		case s := <-ready:
			if s != "js-listener-installed" {
				t.Fatalf("ready carried %q, want js-listener-installed", s)
			}
		case <-time.After(15 * time.Second):
			t.Fatal("the page never emitted ready")
		}
	})
	if t.Failed() {
		return // no listener, so the pushes below would only time out
	}

	t.Run("go_to_js_unsolicited", func(t *testing.T) {
		for i := 1; i <= 3; i++ {
			time.Sleep(100 * time.Millisecond)
			if err := ev.Emit("tick", i); err != nil {
				t.Fatalf("Emit(tick, %d): %v", i, err)
			}
		}
		select {
		case d := <-dones:
			if len(d.seen) != 3 || d.seen[0] != 1 || d.seen[1] != 2 || d.seen[2] != 3 {
				t.Errorf("the page saw ticks %v, want [1 2 3] in order", d.seen)
			}
			if d.dom != 3 {
				t.Errorf("the page's DOM has %d entries, want 3: the handler ran but did not change the page", d.dom)
			}
		case <-time.After(15 * time.Second):
			t.Fatal("three pushes emitted and the page never confirmed them")
		}
	})
}
