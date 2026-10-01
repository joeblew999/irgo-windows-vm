//go:build darwin || windows

package drive

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"io"
	"math"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/crgimenes/native/input"
	"github.com/crgimenes/native/screen"
)

// Event is one thing the page saw, from the bridge: a DOM event, "focus"
// (the page gained or lost keyboard focus), or "ready".
type Event struct {
	Type    string   `json:"type"`
	Target  string   `json:"target"` // tag#id of the element it went to
	X       float64  `json:"x"`      // page coordinates, CSS pixels
	Y       float64  `json:"y"`
	Button  int      `json:"button"`
	Key     string   `json:"key"`
	Code    string   `json:"code"`
	Mods    []string `json:"mods"`
	Value   string   `json:"value"` // an input's value after the event
	DX      float64  `json:"dx"`
	DY      float64  `json:"dy"`
	Trusted bool     `json:"trusted"` // isTrusted: produced by the browser from OS input, not by script
	Focused bool     `json:"focused"` // ready and focus: whether the page has keyboard focus

	Raw json.RawMessage `json:"-"`
}

func (e Event) String() string { return string(e.Raw) }

// Rect is an element's bounding box in CSS pixels from the top-left of the
// page's viewport.
type Rect struct {
	X, Y, W, H float64
}

// Center is the box's centre, rounded to whole points.
func (r Rect) Center() image.Point {
	return image.Pt(int(math.Round(r.X+r.W/2)), int(math.Round(r.Y+r.H/2)))
}

// Session is one running app.
type Session struct {
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	stderr  *bytes.Buffer
	app     *input.App
	pid     int
	window  uint32
	content image.Point

	mu      sync.Mutex
	changed chan struct{} // closed and replaced whenever events, replies or exited change
	events  []Event
	seen    int // Expect's cursor into events
	replies map[int]reply
	nextID  int
	exited  error // set once the process has gone
	gone    bool

	began     time.Time
	trace     []string // what the session did, step by step: Trace
	tookFront string   // the first trace line with the app in front: TookForeground
}

// Launch starts cmd, an app that calls Serve, and returns once its window is
// up and its page has loaded. cmd's Stdin, Stdout and Stderr must be unset.
// ctx bounds the wait for the window, not the session; Close ends that.
func Launch(ctx context.Context, cmd *exec.Cmd) (*Session, error) {
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	s := &Session{cmd: cmd, stdin: stdin, stderr: &bytes.Buffer{}, changed: make(chan struct{}), replies: map[int]reply{}, began: time.Now()}
	cmd.Stderr = &lockedWriter{w: s.stderr, mu: &s.mu}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	started := make(chan start, 1)
	go s.read(stdout, started)

	var st start
	select {
	case st = <-started:
	case <-ctx.Done():
		_ = s.Close()
		return nil, fmt.Errorf("the app did not report its window: %w%s", ctx.Err(), s.stderrNote())
	}
	if st.PID == 0 {
		_ = s.Close()
		return nil, fmt.Errorf("the app exited before reporting its window: %v%s", s.exitErr(), s.stderrNote())
	}
	s.pid, s.window, s.content = st.PID, st.Window, st.Content
	s.app = input.Target(st.PID)
	s.note("window up: pid %d, window %#x, content at %v in its frame", st.PID, st.Window, st.Content)
	ready, err := s.Expect(ctx, func(e Event) bool { return e.Type == "ready" })
	if err != nil {
		_ = s.Close()
		return nil, fmt.Errorf("the page did not load: %w", err)
	}
	s.note("page loaded: %s", ready)
	return s, nil
}

// read consumes the app's stdout until it closes.
func (s *Session) read(stdout io.Reader, started chan<- start) {
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
	sentStart := false
	for sc.Scan() {
		line := append([]byte(nil), sc.Bytes()...)
		var head struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(line, &head) != nil {
			continue // not ours: something in the app printed to stdout
		}
		switch head.Type {
		case "start":
			var st start
			if json.Unmarshal(line, &st) == nil && !sentStart {
				sentStart = true
				started <- st
			}
		case "reply":
			var r reply
			if json.Unmarshal(line, &r) == nil {
				s.update(func() { s.replies[r.ID] = r })
			}
		default:
			var e Event
			if json.Unmarshal(line, &e) == nil {
				e.Raw = line
				s.update(func() { s.events = append(s.events, e) })
			}
		}
	}
	err := s.cmd.Wait()
	if !sentStart {
		started <- start{}
	}
	s.update(func() { s.gone, s.exited = true, err })
}

// update changes the session's state under its lock and wakes every waiter.
func (s *Session) update(f func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f()
	close(s.changed)
	s.changed = make(chan struct{})
}

// wait calls check under the lock until it reports done, ctx ends, or the app
// exits.
func (s *Session) wait(ctx context.Context, check func() bool) error {
	for {
		s.mu.Lock()
		done, gone, ch := check(), s.gone, s.changed
		s.mu.Unlock()
		if done {
			return nil
		}
		if gone {
			return fmt.Errorf("the app exited: %v%s", s.exitErr(), s.stderrNote())
		}
		select {
		case <-ch:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// PID is the app's process ID.
func (s *Session) PID() int { return s.pid }

// Window is the app window's ID: a CGWindowID on macOS, an HWND on Windows.
func (s *Session) Window() uint32 { return s.window }

// Close ends the app — closing its stdin, which it exits on — and waits for
// it, killing it if it has not gone within five seconds.
func (s *Session) Close() error {
	s.note("closing the app")
	_ = s.stdin.Close() // a pipe; the app may already be gone
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if s.wait(ctx, func() bool { return s.gone }) == nil {
		return nil
	}
	if err := s.cmd.Process.Kill(); err != nil {
		return err
	}
	return errors.New("the app did not exit within 5s of its stdin closing, and was killed")
}

func (s *Session) exitErr() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.exited
}

func (s *Session) stderrNote() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if t := strings.TrimSpace(s.stderr.String()); t != "" {
		return "; it wrote: " + t
	}
	return ""
}

// Events is every event the page has reported so far.
func (s *Session) Events() []Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Event(nil), s.events...)
}

// Expect waits for an event that match accepts, among those that arrived
// after the one the previous Expect returned, and returns it. So successive
// Expects assert an order.
func (s *Session) Expect(ctx context.Context, match func(Event) bool) (Event, error) {
	var got Event
	err := s.wait(ctx, func() bool {
		for i := s.seen; i < len(s.events); i++ {
			if match(s.events[i]) {
				got, s.seen = s.events[i], i+1
				return true
			}
		}
		return false
	})
	if err != nil {
		return Event{}, fmt.Errorf("no matching event (%d so far): %w", len(s.Events()), err)
	}
	return got, nil
}

// Eval runs src, a script or an expression, in the page and decodes its value
// (a promise is awaited) into out, which may be nil. Bridge-assisted: it reads
// the page through JavaScript.
func (s *Session) Eval(ctx context.Context, src string, out any) error {
	s.mu.Lock()
	s.nextID++
	id := s.nextID
	s.mu.Unlock()
	b, err := json.Marshal(request{ID: id, JS: src})
	if err != nil {
		return err
	}
	if _, err := s.stdin.Write(append(b, '\n')); err != nil {
		return fmt.Errorf("sending to the app: %w", err)
	}
	var r reply
	err = s.wait(ctx, func() bool {
		var ok bool
		if r, ok = s.replies[id]; ok {
			delete(s.replies, id)
		}
		return ok
	})
	switch {
	case err != nil:
		return fmt.Errorf("eval %q: %w", src, err)
	case r.Error != "":
		return fmt.Errorf("eval %q: the page threw: %s", src, r.Error)
	case out != nil:
		return json.Unmarshal(r.Value, out)
	}
	return nil
}

// poll is how often a WaitFor asks the page again.
const poll = 50 * time.Millisecond

// WaitFor waits until the expression expr is truthy in the page.
// Bridge-assisted.
func (s *Session) WaitFor(ctx context.Context, expr string) error {
	var last error
	for {
		var ok bool
		last = s.Eval(ctx, "!!("+expr+")", &ok)
		if last == nil && ok {
			return nil
		}
		select {
		case <-ctx.Done():
			if last != nil {
				return fmt.Errorf("waiting for %s: %w", expr, last)
			}
			return fmt.Errorf("waiting for %s: still false: %w", expr, ctx.Err())
		case <-time.After(poll):
		}
	}
}

// WaitForSelector waits until an element matches sel. Bridge-assisted.
func (s *Session) WaitForSelector(ctx context.Context, sel string) error {
	return s.WaitFor(ctx, "document.querySelector("+jsString(sel)+")")
}

// WaitForText waits until the element sel matches has exactly text as its
// value (a form field) or its text content (anything else), and says what it
// held instead when it never does. Bridge-assisted.
func (s *Session) WaitForText(ctx context.Context, sel, text string) error {
	err := s.WaitFor(ctx, textJS(sel)+" === "+jsString(text))
	if err != nil {
		var got any
		if s.Eval(context.Background(), textJS(sel), &got) == nil {
			return fmt.Errorf("%s holds %q, want %q: %w", sel, got, text, err)
		}
	}
	return err
}

// Text is what the element sel matches holds: its value for a form field, its
// text content for anything else. Bridge-assisted.
func (s *Session) Text(ctx context.Context, sel string) (string, error) {
	var got *string
	if err := s.Eval(ctx, textJS(sel), &got); err != nil {
		return "", err
	}
	if got == nil {
		return "", fmt.Errorf("no element matches %s", sel)
	}
	return *got, nil
}

// Focus is whether the page has keyboard focus (document.hasFocus()) and the
// element that holds it, as tag#id. Keys reach only a focused page.
// Bridge-assisted.
func (s *Session) Focus(ctx context.Context) (focused bool, active string, err error) {
	var r struct {
		Focused bool
		Active  string
	}
	err = s.Eval(ctx, `(function(){var e=document.activeElement;`+
		`return {focused: document.hasFocus(), active: e ? e.tagName.toLowerCase() + (e.id ? '#' + e.id : '') : ''}})()`, &r)
	return r.Focused, r.Active, err
}

func textJS(sel string) string {
	return "(function(){var e=document.querySelector(" + jsString(sel) + ");" +
		"return e ? ('value' in e && e.tagName !== 'BUTTON' ? e.value : e.textContent) : null})()"
}

// Locate finds the element sel matches and checks that a click at its centre
// would reach it: on screen, and not covered by another element.
// Bridge-assisted.
func (s *Session) Locate(ctx context.Context, sel string) (Rect, error) {
	var r *struct {
		X, Y, W, H, VW, VH float64
		Covered            string
	}
	if err := s.Eval(ctx, rectJS(sel), &r); err != nil {
		return Rect{}, err
	}
	switch {
	case r == nil:
		return Rect{}, fmt.Errorf("no element matches %s", sel)
	case r.W == 0 || r.H == 0:
		return Rect{}, fmt.Errorf("%s has no size: it is not rendered", sel)
	}
	rect := Rect{r.X, r.Y, r.W, r.H}
	c := rect.Center()
	if c.X < 0 || c.Y < 0 || float64(c.X) >= r.VW || float64(c.Y) >= r.VH {
		return rect, fmt.Errorf("%s is outside the viewport (centre %v, viewport %.0fx%.0f): scroll it into view first", sel, c, r.VW, r.VH)
	}
	if r.Covered != "" {
		return rect, fmt.Errorf("%s is covered at its centre by %s", sel, r.Covered)
	}
	return rect, nil
}

// Click clicks the centre of the element sel matches with the left button.
// The element is found through the bridge; the click is real OS input.
func (s *Session) Click(ctx context.Context, sel string) error {
	r, err := s.Locate(ctx, sel)
	if err != nil {
		return err
	}
	c := r.Center()
	return s.ClickAt(c.X, c.Y)
}

// ClickAt clicks the left button at (x, y) in page coordinates: CSS pixels
// from the top-left of the web content, as clientX and clientY count them.
// Real OS input.
func (s *Session) ClickAt(x, y int) error {
	err := s.app.Click(x+s.content.X, y+s.content.Y, input.Left)
	s.note("OS click at page (%d, %d), window (%d, %d): %v", x, y, x+s.content.X, y+s.content.Y, errText(err))
	return err
}

// Type types text into whatever has focus in the page, any Unicode as itself.
// Real OS input.
func (s *Session) Type(text string) error {
	err := s.app.TypeString(text)
	s.note("OS type %q: %v", text, errText(err))
	return err
}

// Press presses and releases key with mods held. Real OS input.
func (s *Session) Press(key input.Key, mods ...input.Modifier) error {
	err := s.app.KeyTap(key, mods...)
	s.note("OS key %v %v: %v", key, mods, errText(err))
	return err
}

// Scroll scrolls the window by dx, dy lines with the pointer at its centre;
// positive dy scrolls up (towards the top of the page), as native/input
// counts. Real OS input.
func (s *Session) Scroll(dx, dy int) error {
	err := s.app.Scroll(dx, dy)
	s.note("OS scroll (%d, %d): %v", dx, dy, errText(err))
	return err
}

// Trace is what the session has done so far, one line per step — the
// window coming up, the page loading, every OS input and its result, the
// close — each with the time since Launch and, on Windows, the foreground
// window straight after it. A test logs it when it fails, so the log shows
// where focus or the foreground went, and when.
func (s *Session) Trace() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.trace...)
}

func (s *Session) note(format string, args ...any) {
	line := fmt.Sprintf("+%.2fs ", time.Since(s.began).Seconds()) + fmt.Sprintf(format, args...)
	fg, asked, err := stepForeground()
	switch {
	case !asked:
	case err != nil:
		line += "; foreground: " + err.Error()
	default:
		line += "; foreground: " + fg.String()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.trace = append(s.trace, line)
	if asked && err == nil && s.pid != 0 && fg.PID == s.pid && s.tookFront == "" {
		s.tookFront = line
	}
}

// TookForeground is the first step of the trace after which the app's own
// window was the foreground window, or "" when it never was (or, on macOS,
// where the trace does not ask). The app is made never to activate, so this
// is the driver taking over the desktop, even if the foreground has moved
// on by the time the test checks it: closing an app that holds it hands it
// to another window.
func (s *Session) TookForeground() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.tookFront
}

func errText(err error) string {
	if err == nil {
		return "ok"
	}
	return err.Error()
}

// Screenshot is the window's own pixels, frame included, even while it is
// behind other windows: native/screen's CaptureWindow.
func (s *Session) Screenshot() (image.Image, error) {
	img, err := screen.CaptureWindow(s.window)
	if err != nil {
		return nil, err
	}
	return img, nil
}

func jsString(s string) string {
	b, _ := json.Marshal(s) // a string always marshals
	return string(b)
}

// lockedWriter guards the app's stderr buffer, written by exec's copying
// goroutine and read by error messages.
type lockedWriter struct {
	w  io.Writer
	mu *sync.Mutex
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}
