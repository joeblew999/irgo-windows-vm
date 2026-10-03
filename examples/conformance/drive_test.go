//go:build darwin || windows

package conformance

import (
	"context"
	"errors"
	"fmt"
	"image"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/crgimenes/native/input"
	"github.com/joeblew999/irgo-windows-vm/examples/drive"
)

// Interaction: a glaze app driven the way a user drives it, through
// examples/drive. The app is a separate process (this test binary, started
// again with drive.AppEnv, which TestMain hands to serveApp), and every click,
// key and scroll below is real OS input from native/input, delivered to that
// process in the background. Finding elements and reading the page back go
// through the bridge, in JavaScript.
//
// Each step is marked OS (native/input: the page receives it as an event the
// browser made from OS input) or bridge (JavaScript that reads the page). The
// page reports every event with its isTrusted flag, and the tests require it
// to be true for every OS step; TestDriveClick/script_click_is_untrusted is
// the control that shows the flag tells the two apart.
//
// None of this may take over the desktop it runs on: the app never activates,
// and launchApp fails the test if the frontmost app changed while it ran.

// formApp is the page the drive tests use: a text field and a counter button
// in a header that stays put, a target area, and a page long enough to scroll.
const formApp = "form"

const formHTML = `<!doctype html><html><head><meta charset="utf-8"><style>
body{margin:0;font:15px -apple-system,"Segoe UI",sans-serif;background:#fff;color:#222}
header{position:sticky;top:0;display:flex;gap:8px;padding:12px 16px;background:#eef1f8;border-bottom:1px solid #cdd3e0}
#name{flex:1;font:inherit;padding:4px 6px}
#inc{font:inherit;padding:4px 12px}
#pad{height:90px;margin:12px 16px;background:#dfe9ff;border:1px dashed #7a95d6;display:flex;align-items:center;justify-content:center}
.row{padding:6px 16px;border-bottom:1px solid #eee}
#end{padding:16px;font-weight:bold}
</style></head><body>
<header><input id="name" placeholder="type here" autocomplete="off"><button id="inc">count: 0</button></header>
<div id="pad">click target</div>
<section id="long"></section>
<p id="end">end of page</p>
<script>
var n = 0, inc = document.getElementById('inc');
inc.addEventListener('click', function () { n++; inc.textContent = 'count: ' + n; });
var long = document.getElementById('long');
for (var i = 1; i <= 60; i++) {
  var d = document.createElement('div');
  d.className = 'row';
  d.textContent = 'row ' + i;
  long.appendChild(d);
}
</script></body></html>`

// serveApp runs the app a drive test launched, by name.
func serveApp(name string) {
	switch name {
	case formApp:
		drive.Serve(drive.Page{Title: "irgo conformance: driven app", HTML: formHTML, Width: 480, Height: 360})
	default:
		fmt.Fprintf(os.Stderr, "no drive app named %q\n", name)
		os.Exit(2)
	}
}

// needInput skips a drive test where there is no OS input to drive with, and
// says why: native/input has no Windows backend yet, or this process lacks the
// Accessibility permission macOS requires for posting events.
func needInput(t *testing.T) {
	t.Helper()
	if _, _, err := input.MousePosition(); errors.Is(err, input.ErrUnsupported) {
		if runtime.GOOS == "windows" {
			t.Skip("waits on native/input's Windows backend (joeblew999/native feat/input-screen-windows): the native built here has none, so there is no OS input to drive the app with")
		}
		t.Fatalf("native/input reports no backend on %s, where it is supposed to have one: %v", runtime.GOOS, err)
	}
	if !input.Trusted() {
		t.Skip("this process has no Accessibility permission, which posting input needs (input.Trusted is false); grant it to the terminal or runner that started the suite")
	}
}

// launchApp starts the named app, waits for its page, and closes it when the
// test ends — failing the test if the frontmost app is not the same then as
// it was before, which would mean the driver took over the desktop.
func launchApp(t *testing.T, name string) *drive.Session {
	t.Helper()
	needGUI(t)
	needInput(t)

	before, frontErr := drive.Frontmost()
	if frontErr != nil {
		t.Logf("the frontmost app could not be read, so it is not checked: %v", frontErr)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("finding this test binary to start the app with: %v", err)
	}
	cmd := exec.Command(exe) // #nosec G204 -- this test binary, re-run as the app
	cmd.Env = append(os.Environ(), drive.AppEnv(name))
	ctx, cancel := context.WithTimeout(context.Background(), uiTimeout)
	defer cancel()
	s, err := drive.Launch(ctx, cmd)
	if err != nil {
		t.Fatalf("launching the app: %v", err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Errorf("closing the app: %v", err)
		}
		checkFrontmost(t, s, before, frontErr)
		if t.Failed() {
			// Where focus and the foreground went, step by step, and what the
			// page saw: the record a failure on a CI runner is diagnosed from.
			t.Logf("the session, step by step:\n  %s", strings.Join(s.Trace(), "\n  "))
			var seen []string
			for _, e := range s.Events() {
				seen = append(seen, e.String())
			}
			t.Logf("every event the page reported:\n  %s", strings.Join(seen, "\n  "))
		}
	})
	return s
}

// checkFrontmost fails the test if the frontmost app is not the one it was
// before the app was launched, or if the app's own window was ever the
// foreground window while it ran (on Windows, where the session records the
// foreground at every step). The second catches a takeover that the first
// misreads: an app that took the foreground hands it to some other window
// when it closes, so the change looks like someone else's.
func checkFrontmost(t *testing.T, s *drive.Session, before drive.App, frontErr error) {
	t.Helper()
	if took := s.TookForeground(); took != "" {
		t.Errorf("the driven app's window became the foreground window, which it must never do: driving the app took over the desktop (first seen after: %s)", took)
	}
	if frontErr != nil {
		return
	}
	after, err := drive.Frontmost()
	switch {
	case err != nil:
		t.Errorf("the frontmost app could not be read after the test: %v", err)
	case after.Same(before):
	case after.PID == s.PID() || after.PID == os.Getpid():
		t.Errorf("the frontmost app was %v before this test and %v after it, which is this test's own: driving the app took over the desktop", before, after)
	default:
		t.Errorf("the frontmost app was %v before this test and %v after it, which this test does not own: someone or something else switched apps, or the driver's input went astray", before, after)
	}
}

// retried logs that a step is being tried again, and why. The line starts
// "retry:", which glaze-check records against the test even when it passes,
// so a pass that needed a retry says so in GLAZE-STATUS.md.
func retried(t *testing.T, format string, args ...any) {
	t.Helper()
	t.Logf("retry: "+format, args...)
}

// focusField clicks the element sel matches (a background OS click, which
// is also how Chromium takes focus inside its own window) and checks that it
// is the page's active element, clicking again, at most five times and
// logging every retry, when it is not. document.hasFocus() is logged, not
// required: on macOS the window is never key, and keys reach the page anyway.
func focusField(t *testing.T, s *drive.Session, sel, target string) {
	t.Helper()
	for try := 1; ; try++ {
		if err := s.Click(ui(t), sel); err != nil { // bridge locates, OS clicks
			t.Fatalf("clicking %s: %v", sel, err)
		}
		expectTrusted(t, s, "mousedown", func(e drive.Event) bool { return e.Target == target })
		focused, active, err := s.Focus(ui(t)) // bridge
		if err != nil {
			t.Fatal(err)
		}
		if active == target {
			return
		}
		if try == 5 {
			t.Fatalf("five background clicks on %s and it is still not the active element (active element %q, document.hasFocus() %v)", sel, active, focused)
		}
		retried(t, "after click %d on %s the active element is %q (document.hasFocus() %v): clicking again", try, sel, active, focused)
	}
}

// keyed sends keyboard input with send, after giving sel focus, and waits for
// the page to report an event that match accepts. When nothing arrives it
// tries again, up to three times, but only if the field is unchanged — keys
// that were dropped whole, which is what a page without focus does to them.
// Anything partial is a failure, never retried. Every retry is logged with
// the page's focus at the time.
func keyed(t *testing.T, s *drive.Session, sel, target, what string, send func() error, match func(drive.Event) bool) drive.Event {
	t.Helper()
	for try := 1; ; try++ {
		focusField(t, s, sel, target)
		was, err := s.Text(ui(t), sel) // bridge
		if err != nil {
			t.Fatal(err)
		}
		if err := send(); err != nil { // OS
			t.Fatalf("%s: %v", what, err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
		e, err := s.Expect(ctx, match)
		cancel()
		if err == nil {
			if !e.Trusted {
				t.Fatalf("the event for %s was not trusted (isTrusted false): the page saw script, not OS input: %s", what, e)
			}
			return e
		}
		now, terr := s.Text(ui(t), sel)
		focused, active, ferr := s.Focus(ui(t))
		if terr != nil || ferr != nil {
			t.Fatalf("%s did not land (%v), and reading the page back failed: %v %v", what, err, terr, ferr)
		}
		if now != was {
			t.Fatalf("%s landed in part: %s held %q before and %q after, and no matching event arrived: %v", what, sel, was, now, err)
		}
		if try == 3 {
			t.Fatalf("%s never reached the page in three attempts (%v); the page's focus: document.hasFocus() %v, active element %q", what, err, focused, active)
		}
		retried(t, "%s did not reach the page on attempt %d (%s unchanged at %q; document.hasFocus() %v, active element %q): focusing it and sending again", what, try, sel, now, focused, active)
	}
}

// ui is a context bounded by uiTimeout, for one step.
func ui(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), uiTimeout)
	t.Cleanup(cancel)
	return ctx
}

// snap photographs the driven app's window as a subtest named label, so a
// test's before and after pictures are two rows with a picture each.
func snap(t *testing.T, s *drive.Session, label string) {
	t.Helper()
	t.Run(label, func(t *testing.T) {
		shoot(t, s.Screenshot)
	})
}

// expectTrusted waits for the next event of type typ that match accepts, and
// fails unless the browser made it from OS input.
func expectTrusted(t *testing.T, s *drive.Session, typ string, match func(drive.Event) bool) drive.Event {
	t.Helper()
	e, err := s.Expect(ui(t), func(e drive.Event) bool { return e.Type == typ && (match == nil || match(e)) })
	if err != nil {
		t.Fatalf("waiting for a %s event: %v", typ, err)
	}
	if !e.Trusted {
		t.Fatalf("the %s event was not trusted (isTrusted false): the page saw script, not OS input: %s", typ, e)
	}
	return e
}

// TestDriveType clicks into a text field and types into it — Unicode, an
// emoji, and a Backspace — and reads the field back. Keys reach only a page
// with focus (on Windows); keyed clicks into the field first, and retries,
// logged, only keys the page dropped whole.
func TestDriveType(t *testing.T) {
	s := launchApp(t, formApp)
	snap(t, s, "before")

	// keyed clicks into the field first (OS), and checks the page has focus
	// there (bridge) before it types.
	const typed = "héllo wörld 👋!"
	keyed(t, s, "#name", "input#name", "typing", func() error { return s.Type(typed) }, // OS
		func(e drive.Event) bool { return e.Type == "input" && strings.HasPrefix(e.Value, "h") })
	if err := s.WaitForText(ui(t), "#name", typed); err != nil { // bridge
		t.Fatal(err)
	}
	expectTrusted(t, s, "input", func(e drive.Event) bool { return e.Value == typed })

	keyed(t, s, "#name", "input#name", "pressing Backspace", func() error { return s.Press(input.KeyBackspace) }, // OS
		func(e drive.Event) bool { return e.Type == "keydown" && e.Key == "Backspace" })
	if err := s.WaitForText(ui(t), "#name", "héllo wörld 👋"); err != nil { // bridge
		t.Fatal(err)
	}
	for _, e := range s.Events() {
		if e.Type == "input" && !e.Trusted {
			t.Errorf("an input event was not trusted: %s", e)
		}
	}
	snap(t, s, "after")
}

// TestDriveClick clicks a counter button three times and reads its label.
func TestDriveClick(t *testing.T) {
	s := launchApp(t, formApp)
	snap(t, s, "before")

	for i := 1; i <= 3; i++ {
		if err := s.Click(ui(t), "#inc"); err != nil { // bridge locates, OS clicks
			t.Fatalf("click %d: %v", i, err)
		}
		expectTrusted(t, s, "click", func(e drive.Event) bool { return e.Target == "button#inc" })
	}
	if err := s.WaitForText(ui(t), "#inc", "count: 3"); err != nil { // bridge
		t.Fatal(err)
	}
	snap(t, s, "after")

	// The control for every isTrusted check in these tests: the same click
	// made by script reaches the page as untrusted, so a trusted event is one
	// the browser made from OS input.
	t.Run("script_click_is_untrusted", func(t *testing.T) {
		if err := s.Eval(ui(t), "document.getElementById('inc').click()", nil); err != nil {
			t.Fatal(err)
		}
		e, err := s.Expect(ui(t), func(e drive.Event) bool { return e.Type == "click" })
		if err != nil {
			t.Fatal(err)
		}
		if e.Trusted {
			t.Fatalf("a click made by script was reported trusted, so isTrusted proves nothing here: %s", e)
		}
	})
}

// TestDriveClickAt clicks a point in page coordinates and checks the page
// received it at exactly that point: the mapping from page to window to OS
// coordinates, which every Click relies on.
func TestDriveClickAt(t *testing.T) {
	s := launchApp(t, formApp)
	r, err := s.Locate(ui(t), "#pad") // bridge
	if err != nil {
		t.Fatal(err)
	}
	at := image.Pt(int(r.X)+17, int(r.Y)+23)
	if err := s.ClickAt(at.X, at.Y); err != nil { // OS
		t.Fatalf("clicking at %v: %v", at, err)
	}
	e := expectTrusted(t, s, "mousedown", nil)
	if e.Target != "div#pad" || int(e.X) != at.X || int(e.Y) != at.Y {
		t.Fatalf("the page received the click on %s at (%v, %v), want div#pad at %v", e.Target, e.X, e.Y, at)
	}
	snap(t, s, "clicked")
}

// TestDriveScroll scrolls the page down with the wheel and checks it moved.
func TestDriveScroll(t *testing.T) {
	s := launchApp(t, formApp)
	if err := s.WaitFor(ui(t), "scrollY === 0"); err != nil { // bridge
		t.Fatal(err)
	}
	snap(t, s, "before")

	// STANDING IN FOR AN UPSTREAM FIX — docs/reference/upstream.md §6: the first scroll
	// posted to a new process, after this one has posted input to another, is
	// dropped. Up to three posts; one when §6 is fixed or the test runs alone.
	var e drive.Event
	for post := 1; ; post++ {
		if err := s.Scroll(0, -5); err != nil { // OS: negative dy scrolls down
			t.Fatalf("scrolling: %v", err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		got, err := s.Expect(ctx, func(e drive.Event) bool { return e.Type == "wheel" })
		cancel()
		if err == nil {
			e = got
			break
		}
		if post == 3 {
			t.Fatalf("three scrolls posted and the page saw no wheel event: %v", err)
		}
		retried(t, "scroll post %d reached the page as no wheel event within 500ms (docs/reference/upstream.md §6): posting again", post)
	}
	if !e.Trusted {
		t.Fatalf("the wheel event was not trusted (isTrusted false): %s", e)
	}
	if e.DY <= 0 {
		t.Errorf("the wheel event's deltaY is %v, want positive (down)", e.DY)
	}
	if err := s.WaitFor(ui(t), "scrollY > 0"); err != nil { // bridge
		t.Fatal(err)
	}
	snap(t, s, "after")
}
