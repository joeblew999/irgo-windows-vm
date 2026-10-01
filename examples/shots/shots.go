// Package shots is how a conformance suite photographs what a test checked
// and tells internal/glazecheck about it: both suites, examples/conformance
// (glaze) and examples/vmconformance (the VM), take their pictures through it.
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
// where black is the answer (a window excluded from capture).
//
// Grabbing the pixels is the suite's business (PrintWindow, screencapture,
// native/screen); this package bounds the grab, judges the picture, shrinks
// it, writes it and logs the line.
package shots

import (
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
)

// Prefixes glazecheck matches. Changing one means changing it there too
// (internal/glazecheck, shotLine), and its test says so.
const (
	OK   = "screenshot: "
	None = "screenshot not captured: "
)

// Timeout bounds one capture. A capture that talks to a wedged UI thread
// (PrintWindow sends WM_PRINT) would otherwise hang the test that asked for
// it; past this it is abandoned and recorded as not captured.
const Timeout = 10 * time.Second

// Settle is how long a window is given to paint before it is photographed. A
// dispatched function running means the loop is draining, not that the web
// view has composited its first frame.
const Settle = 700 * time.Millisecond

// MaxWidth bounds a picture's width. A Retina capture of a 480-point window
// is 960 pixels across; halved it is what a reader sees on screen, and a
// fifth of the bytes in the repository.
const MaxWidth = 800

// Taken is one attempt's picture, or why there is none, and a note about it.
type Taken struct {
	Img  image.Image
	Note string
	Err  error
}

// Take waits Settle, runs grab on one locked OS thread (the Windows path holds
// GDI handles and a device context between calls), bounded by Timeout, and
// judges what came back. blackMeans, when set, is why a black frame is the
// expected picture rather than a failed one.
func Take(grab func() (image.Image, error), blackMeans string) Taken {
	time.Sleep(Settle)
	got := make(chan Taken, 1)
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		img, err := grab()
		got <- Taken{Img: img, Err: err}
	}()
	var r Taken
	select {
	case r = <-got:
	case <-time.After(Timeout):
		return Taken{Err: fmt.Errorf("the capture did not return within %s", Timeout)}
	}
	if r.Err != nil {
		return r
	}
	if why := Blank(r.Img); why != "" {
		if blackMeans == "" || !strings.HasPrefix(why, "black") {
			return Taken{Err: fmt.Errorf("the picture is %s — a failed capture, not published", why)}
		}
		r.Note = blackMeans
	}
	return r
}

// Record writes r's picture to dir/rel, shrunk to MaxWidth, and logs the line
// glazecheck reads; or, when there is no picture, logs why.
func Record(t testing.TB, dir, rel string, r Taken) {
	t.Helper()
	if r.Err != nil {
		t.Logf("%s%v", None, r.Err)
		return
	}
	if err := WritePNG(filepath.Join(dir, filepath.FromSlash(rel)), Shrink(r.Img, MaxWidth)); err != nil {
		t.Logf("%swriting it: %v", None, err)
		return
	}
	if r.Note != "" {
		t.Logf("%s%s (%s)", OK, rel, r.Note)
		return
	}
	t.Logf("%s%s", OK, rel)
}

// Name is the file name a test's picture is given: its name, subtests
// included, with the slashes taken out.
func Name(t testing.TB) string { return strings.ReplaceAll(t.Name(), "/", "_") + ".png" }

// Blank says why img is not a picture — black, or one colour throughout — or
// "" when it is one. Sampled on a grid: a title bar's text or a page's words
// are enough to break uniformity, and a capture that saw nothing has none.
func Blank(img image.Image) string {
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

// Shrink halves img until it is at most maxW wide, averaging each 2x2 block —
// enough for a screenshot, and no dependency outside the standard library.
func Shrink(img image.Image, maxW int) image.Image {
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

// WritePNG writes img to path, creating its directory, and removes a file
// left half-written by a failed encode.
func WritePNG(path string, img image.Image) error {
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
