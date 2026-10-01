//go:build darwin || windows

package drive

import (
	"bufio"
	"encoding/json"
	"fmt"
	"image"
	"os"
	"sync"
	"time"

	"github.com/crgimenes/glaze"
)

// appEnv names the app a process should serve instead of doing what it
// normally does. Launch-by-re-exec: a test binary starts itself with this set,
// and its TestMain calls Serve, so the app needs no separate build — which
// matters on the Windows VM, where the test binary is pushed alone and there
// is no Go toolchain to build anything else.
const appEnv = "IRGO_DRIVE_APP"

// AppName is the app this process was started to serve, or "" when it was not
// started by Launch with AppEnv.
func AppName() string { return os.Getenv(appEnv) }

// AppEnv is the environment entry that makes a process serve the named app;
// append it to the Cmd given to Launch.
func AppEnv(name string) string { return appEnv + "=" + name }

// Page is what Serve shows.
type Page struct {
	Title         string
	HTML          string
	Width, Height int // content size in points
}

// lifetime bounds an app whose test died without closing its stdin.
const lifetime = 5 * time.Minute

// Serve runs the app: a glaze window showing p, which never activates, with
// the bridge installed. It writes the event stream on stdout, answers the
// requests that arrive on stdin, and exits when stdin closes. It never
// returns, and must be called on the main thread (from TestMain or main).
func Serve(p Page) {
	if p.Width == 0 {
		p.Width, p.Height = 480, 360
	}
	go func() {
		time.Sleep(lifetime)
		fail(fmt.Errorf("still running after %s; exiting", lifetime))
	}()

	win, err := backgroundWindow(p)
	if err != nil {
		fail(err)
	}
	w, err := glaze.NewWithOptions(glaze.Options{Window: win, AcceptsFirstMouse: true})
	if err != nil {
		fail(err)
	}
	defer w.Destroy()
	if win == nil {
		// glaze made the window, so it still needs a title and a size.
		w.SetTitle(p.Title)
		w.SetSize(p.Width, p.Height, glaze.HintNone)
	}

	if err := w.Bind("__drive_event", func(ev map[string]any) {
		if ev["type"] != "ready" {
			emit(ev)
			return
		}
		w.Dispatch(func() {
			onReady(w)
			emit(ev)
		})
	}); err != nil {
		fail(err)
	}
	if err := w.Bind("__drive_reply", func(id int, value, errText string) {
		r := reply{Type: "reply", ID: id, Error: errText}
		if errText == "" {
			r.Value = json.RawMessage(value)
			if !json.Valid(r.Value) {
				r.Value, r.Error = nil, "the page returned something that is not JSON: "+value
			}
		}
		emit(r)
	}); err != nil {
		fail(err)
	}
	w.Init(bridgeJS)

	id, content, err := windowInfo(w)
	if err != nil {
		fail(err)
	}
	emit(start{Type: "start", PID: os.Getpid(), Window: id, Content: content})

	go serveRequests(w)
	w.SetHtml(p.HTML)
	w.Run()
	os.Exit(0)
}

// serveRequests evaluates each script the test sends on the UI thread; the
// page answers through __drive_reply. Stdin closing — the test closed its end,
// or died — ends the app.
func serveRequests(w glaze.WebView) {
	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for sc.Scan() {
		var req request
		if err := json.Unmarshal(sc.Bytes(), &req); err != nil {
			emit(reply{Type: "reply", ID: -1, Error: "a request that is not JSON: " + err.Error()})
			continue
		}
		js := evalJS(req.ID, req.JS)
		w.Dispatch(func() { w.Eval(js) })
	}
	os.Exit(0)
}

type start struct {
	Type    string      `json:"type"`
	PID     int         `json:"pid"`
	Window  uint32      `json:"window"`
	Content image.Point `json:"content"`
}

type request struct {
	ID int    `json:"id"`
	JS string `json:"js"`
}

type reply struct {
	Type  string          `json:"type"`
	ID    int             `json:"id"`
	Value json.RawMessage `json:"value,omitempty"`
	Error string          `json:"error,omitempty"`
}

var out struct {
	sync.Mutex
	enc *json.Encoder
}

func emit(v any) {
	out.Lock()
	defer out.Unlock()
	if out.enc == nil {
		out.enc = json.NewEncoder(os.Stdout)
	}
	_ = out.enc.Encode(v) // stdout gone means the test is gone; stdin's EOF ends us
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "drive app:", err)
	os.Exit(1)
}
