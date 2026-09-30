package glazecheck

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// shotSuite logs screenshot lines exactly as examples/conformance does
// (shots_test.go: shotOK, shotNone), from a real binary, so the framing and
// the file:line prefix are the toolchain's and not a guess.
const shotSuite = `package fake

import "testing"

func TestWindow(t *testing.T) { t.Log("screenshot: mac/TestWindow.png") }

func TestNoted(t *testing.T) {
	t.Run("sub", func(t *testing.T) {
		t.Log("screenshot: mac/TestNoted_sub.png (the window the tray was started beside; the status item (x) was not capturable)")
	})
}

func TestMissed(t *testing.T) {
	t.Log("screenshot not captured: the picture is black — a failed capture, not published")
	t.Fatal("the real reason it failed")
}

func TestHeadless(t *testing.T) {}
`

// TestShotLinesAreReadAndNotTakenForMessages: a screenshot line lands in the
// result's Shot, ShotNote or NoShot, and never becomes the test's first
// message — TestMissed's Detail is its t.Fatal, logged after the shot line.
//
// Negative control, run by hand: removing the shotLine call from parseEvents
// makes TestMissed's Detail the "screenshot not captured" line and leaves
// every Shot empty.
func TestShotLinesAreReadAndNotTakenForMessages(t *testing.T) {
	rs, err := parseEvents(TargetMac, runFake(t, shotSuite))
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]Result{}
	for _, r := range rs {
		got[r.Name] = r
	}
	if r := got["TestWindow"]; r.Shot != "mac/TestWindow.png" || r.ShotNote != "" || r.NoShot != "" {
		t.Errorf("TestWindow = %+v", r)
	}
	if r := got["TestNoted/sub"]; r.Shot != "mac/TestNoted_sub.png" ||
		r.ShotNote != "the window the tray was started beside; the status item (x) was not capturable" {
		t.Errorf("TestNoted/sub = %+v", r)
	}
	r := got["TestMissed"]
	if r.Shot != "" || r.NoShot != "the picture is black — a failed capture, not published" {
		t.Errorf("TestMissed's screenshot = %+v", r)
	}
	if !strings.HasSuffix(r.Detail, "the real reason it failed") {
		t.Errorf("TestMissed's first message is %q, want its t.Fatal", r.Detail)
	}
	if r := got["TestHeadless"]; r.Shot != "" || r.NoShot != "" {
		t.Errorf("TestHeadless took no picture and has %+v", r)
	}
}

var fakePNG = append([]byte(nil), append(pngMagic, "rest of a picture"...)...)

// TestShotsFlowToTheStatusFileAndBack is the path a picture takes: collected
// into the target's directory, drawn in the Screenshots table beside the
// other target's, and imported from a CI artifact into another checkout.
//
// Negative controls, run by hand: dropping the RemoveAll in collectShots
// leaves stale.png behind and fails the first check; making copyShot skip the
// PNG check lets notpng through as a picture.
func TestShotsFlowToTheStatusFileAndBack(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	macDir := filepath.Join(root, filepath.FromSlash(ShotsDir), TargetMac)
	if err := os.MkdirAll(macDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(macDir, "stale.png"), fakePNG, 0o644); err != nil {
		t.Fatal(err)
	}

	files := map[string][]byte{"mac/TestA.png": fakePNG, "mac/TestB.png": []byte("not a png")}
	fetch := func(rel string) ([]byte, error) {
		if b, ok := files[rel]; ok {
			return b, nil
		}
		return nil, errors.New("no such file")
	}
	mac := section(TargetMac,
		Result{Name: "TestA", Outcome: Pass, Shot: "mac/TestA.png", ShotNote: "a <note>"},
		Result{Name: "TestB", Outcome: Pass, Shot: "mac/TestB.png"},
		Result{Name: "TestC", Outcome: Fail, Detail: "x", NoShot: "no permission"},
		Result{Name: "TestHeadless", Outcome: Pass},
	)
	if err := collectShots(root, &mac, fetch, func(string, ...any) {}); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(macDir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if strings.Join(names, " ") != "TestA.png shots.json" {
		t.Errorf("the Mac's directory holds %v, want only TestA.png and the manifest", names)
	}
	if _, err := Record(root, mac); err != nil {
		t.Fatal(err)
	}

	body := read(t, filepath.Join(root, StatusFile))
	for _, want := range []string{
		"- screenshots: 1 of the 3 tests that open a window took one",
		"## Screenshots",
		`| TestA | PASS<br><a href="screens/conformance/mac/TestA.png"><img src="screens/conformance/mac/TestA.png" width="360" alt="TestA on Mac"></a><br><sub>a &lt;note&gt;</sub> | — |`,
		"| TestB | PASS<br>not captured: taken in the run, and not copied back: mac/TestB.png arrived as 9 bytes that are not a PNG | — |",
		"| TestC | **FAIL**<br>not captured: no permission | — |",
		"- Windows: no pictures recorded yet",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("status file lacks %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "| TestHeadless | PASS<br>") {
		t.Error("a test that opens no window has a row in the Screenshots table")
	}

	// A CI artifact for Windows, laid out as conformance.yml uploads it,
	// imported into this checkout: the Mac's pictures stay, Windows' arrive.
	art := filepath.Join(t.TempDir(), "conformance-windows-11-arm")
	winRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(winRoot, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	win := section(TargetWindows, Result{Name: "TestA", Outcome: Pass, Shot: "windows/TestA.png"})
	win.RunURL = "https://github.com/o/r/actions/runs/1"
	wfetch := func(string) ([]byte, error) { return fakePNG, nil }
	if err := collectShots(winRoot, &win, wfetch, func(string, ...any) {}); err != nil {
		t.Fatal(err)
	}
	if _, err := Record(winRoot, win); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(art, "screens", "conformance"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(winRoot, filepath.FromSlash(ShotsDir), TargetWindows), filepath.Join(art, "screens", "conformance", TargetWindows)); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(winRoot, StatusFile), filepath.Join(art, "GLAZE-STATUS.md")); err != nil {
		t.Fatal(err)
	}
	if err := Import(root, filepath.Dir(art), func(string, ...any) {}); err != nil {
		t.Fatal(err)
	}
	body = read(t, filepath.Join(root, StatusFile))
	for _, want := range []string{
		"## On the Mac — NO: failed: TestC",
		"## On Windows — YES: 1 passed, 0 skipped",
		"- CI run: https://github.com/o/r/actions/runs/1",
		`<img src="screens/conformance/mac/TestA.png"`,
		`<img src="screens/conformance/windows/TestA.png"`,
		"[CI run](https://github.com/o/r/actions/runs/1)",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("after the import the status file lacks %q:\n%s", want, body)
		}
	}
	if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(ShotsDir), TargetWindows, "TestA.png")); err != nil {
		t.Errorf("the imported picture is not in the checkout: %v", err)
	}

	if err := Import(root, t.TempDir(), func(string, ...any) {}); err == nil {
		t.Error("importing from a directory with no run in it succeeded")
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
