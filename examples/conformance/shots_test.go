//go:build darwin || windows

package conformance

import (
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/crgimenes/glaze"
)

// Screenshots.
//
// Every test that opens a window photographs it at the moment that shows what
// the test proved — the page loaded, the tray up, the menu installed, the
// dialog open — when -conformance.shots (or $CONFORMANCE_SHOTS) names a
// directory. The picture is written there as <os>/<TestName>.png, os being
// "mac" or "windows", the names internal/glazecheck files a target's record
// under.
//
// A capture never fails a test. It is evidence about the test, not part of
// what the test checks, and a runner without Screen Recording permission is
// not a broken glaze. What happened is logged in one of two fixed forms, which
// glazecheck reads out of the test2json events and does not mistake for the
// test's own first message:
//
//	screenshot: mac/TestAppScheme.png
//	screenshot not captured: <why>
//
// A picture that is one colour is a failed capture, not a picture: a black
// frame is what WebView2 content gives a capture that cannot see it, and a
// uniform frame is what a desktop that refused the capture looks like. It is
// reported as not captured and not written, so nothing publishes it — except
// where black is the answer (TestNoCapture: a window excluded from capture).
//
// The mechanism is per OS and cgo-free like the rest: PrintWindow with
// PW_RENDERFULLCONTENT on Windows (shots_windows_test.go), screencapture -l
// with the NSWindow's windowNumber on macOS (shots_darwin_test.go).

var shotsDir = flag.String("conformance.shots", os.Getenv("CONFORMANCE_SHOTS"),
	"directory to write each windowed test's screenshot into, as <os>/<TestName>.png; empty takes none")

// Prefixes glazecheck matches. Changing one means changing it there too
// (internal/glazecheck, shotLine), and its test says so.
const (
	shotOK   = "screenshot: "
	shotNone = "screenshot not captured: "
)

// shotTimeout bounds one capture. A capture that talks to a wedged UI thread
// (PrintWindow sends WM_PRINT) would otherwise hang the test that asked for
// it; past this it is abandoned and recorded as not captured.
const shotTimeout = 10 * time.Second

// settle is how long a window is given to paint before it is photographed. A
// dispatched function running means the loop is draining, not that the web
// view has composited its first frame.
const settle = 700 * time.Millisecond

// maxShotWidth bounds a picture's width. A Retina capture of a 480-point
// window is 960 pixels across; halved it is what a reader sees on screen, and
// a fifth of the bytes in the repository.
const maxShotWidth = 800

// osDir is the directory a target's pictures go in: glazecheck's target names.
func osDir() string {
	if runtime.GOOS == "darwin" {
		return "mac"
	}
	return runtime.GOOS
}

// grab takes one picture.
type grab func() (image.Image, error)

// shoot captures one picture for t with g and records what happened.
func shoot(t *testing.T, g grab) {
	t.Helper()
	record(t, attempt(g, ""))
}

// shootExpecting is shoot where a black frame is the expected result, and
// blackMeans says why.
func shootExpecting(t *testing.T, blackMeans string, g grab) {
	t.Helper()
	record(t, attempt(g, blackMeans))
}

// shootOr tries g, and when it cannot produce a picture takes fallback
// instead, saying so beside the picture: fallbackIs names what the fallback
// shows, and the note carries why g did not work. For the tray and the menu
// bar, which on macOS are drawn outside the process and not always
// capturable: the window the test opened is still a picture of the test.
func shootOr(t *testing.T, g grab, fallback grab, fallbackIs string) {
	t.Helper()
	first := attempt(g, "")
	if first.err == nil || *shotsDir == "" {
		record(t, first)
		return
	}
	second := attempt(fallback, "")
	if second.err == nil {
		second.note = fallbackIs + "; " + first.err.Error()
	}
	record(t, second)
}

// taken is one attempt's picture, or why there is none.
type taken struct {
	img  image.Image
	note string
	err  error
}

// attempt runs g, bounded by shotTimeout, and checks what it returned.
func attempt(g grab, blackMeans string) taken {
	if *shotsDir == "" {
		return taken{}
	}
	time.Sleep(settle)
	got := make(chan taken, 1)
	go func() {
		// One OS thread for the whole capture: the Windows path holds GDI
		// handles and a device context between calls.
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		img, err := g()
		got <- taken{img: img, err: err}
	}()
	var r taken
	select {
	case r = <-got:
	case <-time.After(shotTimeout):
		return taken{err: fmt.Errorf("the capture did not return within %s", shotTimeout)}
	}
	if r.err != nil {
		return r
	}
	if why := blank(r.img); why != "" {
		if blackMeans == "" || !strings.HasPrefix(why, "black") {
			return taken{err: fmt.Errorf("the picture is %s — a failed capture, not published", why)}
		}
		r.note = blackMeans
	}
	return r
}

// record writes the picture and logs the line glazecheck reads.
func record(t *testing.T, r taken) {
	t.Helper()
	if *shotsDir == "" {
		return
	}
	if r.err != nil {
		t.Logf("%s%v", shotNone, r.err)
		return
	}
	rel := osDir() + "/" + strings.ReplaceAll(t.Name(), "/", "_") + ".png"
	if err := writePNG(filepath.Join(*shotsDir, filepath.FromSlash(rel)), shrink(r.img, maxShotWidth)); err != nil {
		t.Logf("%swriting it: %v", shotNone, err)
		return
	}
	if r.note != "" {
		t.Logf("%s%s (%s)", shotOK, rel, r.note)
		return
	}
	t.Logf("%s%s", shotOK, rel)
}

// blank says why img is not a picture — black, or one colour throughout — or
// "" when it is one. Sampled on a grid: a title bar's text or a page's words
// are enough to break uniformity, and a capture that saw nothing has none.
func blank(img image.Image) string {
	b := img.Bounds()
	if b.Dx() < 8 || b.Dy() < 8 {
		return fmt.Sprintf("%dx%d pixels", b.Dx(), b.Dy())
	}
	counts := map[color.RGBA]int{}
	n := 0
	stepX, stepY := max(1, b.Dx()/120), max(1, b.Dy()/120)
	for y := b.Min.Y; y < b.Max.Y; y += stepY {
		for x := b.Min.X; x < b.Max.X; x += stepX {
			r, g, bl, a := img.At(x, y).RGBA()
			if a == 0 {
				continue // a macOS window's rounded corners
			}
			counts[color.RGBA{uint8(r >> 8), uint8(g >> 8), uint8(bl >> 8), 255}]++
			n++
		}
	}
	if n == 0 {
		return "fully transparent"
	}
	var top color.RGBA
	best := 0
	for c, k := range counts {
		if k > best {
			top, best = c, k
		}
	}
	if float64(best)/float64(n) < 0.995 {
		return ""
	}
	if top.R < 8 && top.G < 8 && top.B < 8 {
		return "black"
	}
	return fmt.Sprintf("one colour (#%02x%02x%02x) throughout", top.R, top.G, top.B)
}

// shrink halves img until it is at most maxW wide, averaging each 2x2 block —
// enough for a screenshot, and no dependency outside the standard library.
func shrink(img image.Image, maxW int) image.Image {
	for img.Bounds().Dx() > maxW {
		b := img.Bounds()
		w, h := b.Dx()/2, b.Dy()/2
		out := image.NewRGBA(image.Rect(0, 0, w, h))
		for y := range h {
			for x := range w {
				var r, g, bl, a uint32
				for _, d := range [4][2]int{{0, 0}, {1, 0}, {0, 1}, {1, 1}} {
					cr, cg, cb, ca := img.At(b.Min.X+2*x+d[0], b.Min.Y+2*y+d[1]).RGBA()
					r, g, bl, a = r+cr, g+cg, bl+cb, a+ca
				}
				out.SetRGBA(x, y, color.RGBA{uint8(r >> 10), uint8(g >> 10), uint8(bl >> 10), uint8(a >> 10)})
			}
		}
		img = out
	}
	return img
}

func writePNG(path string, img image.Image) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	if err := png.Encode(f, img); err != nil {
		_ = f.Close() // already failing
		_ = os.Remove(path)
		return err
	}
	return f.Close()
}

// labelled is a load function that puts the test's name in its window, so the
// picture of a window that has no page of its own says which test it is.
func labelled(t *testing.T) func(glaze.WebView) {
	return func(w glaze.WebView) {
		w.SetHtml(`<!doctype html><html><body style="font:16px -apple-system,Segoe UI,sans-serif;margin:24px">` +
			`<h2 style="margin:0 0 8px">` + t.Name() + `</h2><p>irgo conformance suite</p></body></html>`)
	}
}
