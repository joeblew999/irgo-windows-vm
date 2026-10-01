package shots

import (
	"errors"
	"fmt"
	"image"
	"image/color"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func filled(w, h int, c color.RGBA) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for i := 0; i < len(img.Pix); i += 4 {
		img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = c.R, c.G, c.B, c.A
	}
	return img
}

// TestBlank: black and one-colour frames are failed captures; a frame with
// anything drawn on it is a picture.
//
// Negative control, run by hand: raising the uniformity threshold in Blank
// from 0.995 to 1.01 calls the black frame a picture.
func TestBlank(t *testing.T) {
	black := filled(200, 100, color.RGBA{0, 0, 0, 255})
	white := filled(200, 100, color.RGBA{255, 255, 255, 255})
	drawn := filled(200, 100, color.RGBA{255, 255, 255, 255})
	for y := 20; y < 60; y++ {
		for x := 20; x < 120; x++ {
			drawn.SetRGBA(x, y, color.RGBA{0, 0, 200, 255})
		}
	}
	if got := Blank(black); got != "black" {
		t.Errorf("black frame: %q", got)
	}
	if got := Blank(white); !strings.HasPrefix(got, "one colour (#ffffff)") {
		t.Errorf("white frame: %q", got)
	}
	if got := Blank(drawn); got != "" {
		t.Errorf("a frame with a box drawn on it is %q", got)
	}
}

// TestRecord: a picture is written shrunk and logged as taken; a failed one
// is logged as not captured, and nothing is written.
func TestRecord(t *testing.T) {
	dir := t.TempDir()
	img := filled(1700, 100, color.RGBA{1, 2, 3, 255})
	img.SetRGBA(5, 5, color.RGBA{200, 0, 0, 255})
	rec := &logged{TB: t}
	Record(rec, dir, "vm/TestX.png", Taken{Img: img, Note: "a note"})
	Record(rec, dir, "vm/TestY.png", Taken{Err: errors.New("no window")})
	if strings.Join(rec.lines, "\n") != "screenshot: vm/TestX.png (a note)\nscreenshot not captured: no window" {
		t.Errorf("logged %q", rec.lines)
	}
	f, err := os.Open(filepath.Join(dir, "vm", "TestX.png"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	cfg, _, err := image.DecodeConfig(f)
	if err != nil || cfg.Width > MaxWidth {
		t.Errorf("written picture: %+v, %v", cfg, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "vm", "TestY.png")); err == nil {
		t.Error("a failed capture was written")
	}
}

type logged struct {
	testing.TB
	lines []string
}

func (l *logged) Logf(format string, args ...any) {
	l.lines = append(l.lines, strings.TrimSpace(fmt.Sprintf(format, args...)))
}
