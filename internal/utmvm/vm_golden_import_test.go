package utmvm

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// The pull-and-import vm-create does on a machine with no golden image is
// proven live against the real bucket and UTM (docs/findings.md). These run it with
// fakes for UTM, the bucket and the lock, for the decisions it makes on the way.

// fakeImport records what pullGolden asked of the outside world.
type fakeImport struct {
	locked, released bool
	pulled           []GoldenPullOptions
	registered       []string
}

// importerFor is a goldenImporter whose pull leaves a bundle and a golden.json
// in dir, as GoldenPull does, and whose UTM knows no golden image.
func (f *fakeImport) importerFor(t *testing.T, dir string) goldenImporter {
	t.Helper()
	return goldenImporter{
		lock: func() (func(), error) {
			f.locked = true
			return func() { f.released = true }, nil
		},
		exists: func() (bool, error) { return false, nil },
		pull: func(_ context.Context, o GoldenPullOptions, _ func(string, ...any)) (GoldenPullResult, error) {
			f.pulled = append(f.pulled, o)
			b := filepath.Join(o.Dir, GoldenVMName+bundleExt)
			if err := os.MkdirAll(b, 0o755); err != nil {
				return GoldenPullResult{}, err
			}
			g := filepath.Join(o.Dir, goldenJSONName)
			if err := os.WriteFile(g, []byte(`{"source":"g1","windows":"26100.4349"}`), 0o644); err != nil {
				return GoldenPullResult{}, err
			}
			return GoldenPullResult{ID: "abc", Bundle: b, Golden: g}, nil
		},
		name: func(string) (string, error) { return GoldenVMName, nil },
		register: func(bundle, name string) error {
			f.registered = append(f.registered, bundle+" as "+name)
			return nil
		},
		dir: dir,
	}
}

func say(t *testing.T) func(string, ...any) {
	return func(f string, a ...any) { t.Logf(f, a...) }
}

// The happy path: under the machine lock, pulled into the pull directory,
// imported from where it was pulled, and its record put where doctor reads it.
//
// Negative control, run by hand: drop the golden.json copy from pullGolden and
// the manifest check fails.
func TestPullGoldenPullsImportsAndRecords(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := filepath.Join(t.TempDir(), "golden-pull")
	var f fakeImport
	if err := f.importerFor(t, dir).pullGolden(context.Background(), R2Config{}, say(t)); err != nil {
		t.Fatal(err)
	}
	if !f.locked || !f.released {
		t.Errorf("machine lock taken %v, released %v; want both", f.locked, f.released)
	}
	if len(f.pulled) != 1 || f.pulled[0].Dir != dir {
		t.Fatalf("pulled %+v, want once into %s", f.pulled, dir)
	}
	want := filepath.Join(dir, GoldenVMName+bundleExt) + " as " + GoldenVMName
	if len(f.registered) != 1 || f.registered[0] != want {
		t.Errorf("registered %v, want [%s]", f.registered, want)
	}
	m, err := readGoldenManifest(windowsGuest)
	if err != nil || m.Windows != "26100.4349" {
		t.Errorf("golden.json at %s: %+v, %v; want the pulled record", windowsGuest.manifestPath(), m, err)
	}
}

// An empty dir means the real pull directory, under HOME at call time rather
// than when the package was loaded.
func TestPullGoldenDefaultsToThePullDir(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	var f fakeImport
	g := f.importerFor(t, "")
	if err := g.pullGolden(context.Background(), R2Config{}, say(t)); err != nil {
		t.Fatal(err)
	}
	if len(f.pulled) != 1 || f.pulled[0].Dir != GoldenPullDir() {
		t.Errorf("pulled into %+v, want %s", f.pulled, GoldenPullDir())
	}
}

// Each way it can stop: nothing is imported after a step that failed, and a
// golden image that appeared meanwhile is used rather than pulled over.
//
// Negative control, run by hand: skip the name check in pullGolden and the
// "named for another VM" case registers.
func TestPullGoldenStopsWhereItShould(t *testing.T) {
	busy := fmt.Errorf("%w: machine", ErrMutationInProgress)
	for _, c := range []struct {
		name       string
		change     func(*goldenImporter)
		wantErr    string // "" is success
		wantPull   bool
		wantImport bool
	}{
		{"lock busy", func(g *goldenImporter) {
			g.lock = func() (func(), error) { return nil, busy }
		}, "another mutation", false, false},
		{"appeared meanwhile", func(g *goldenImporter) {
			g.exists = func() (bool, error) { return true, nil }
		}, "", false, false},
		{"UTM cannot say", func(g *goldenImporter) {
			g.exists = func() (bool, error) { return false, errors.New("utmctl did not answer") }
		}, "utmctl did not answer", false, false},
		{"pull fails", func(g *goldenImporter) {
			g.pull = func(context.Context, GoldenPullOptions, func(string, ...any)) (GoldenPullResult, error) {
				return GoldenPullResult{}, errors.New("HTTP 401")
			}
		}, "pulling the golden image: HTTP 401", true, false},
		{"named for another VM", func(g *goldenImporter) {
			g.name = func(string) (string, error) { return "g1", nil }
		}, `named "g1"`, true, false},
		{"name unreadable", func(g *goldenImporter) {
			g.name = func(string) (string, error) { return "", errors.New("no config.plist") }
		}, "no config.plist", true, false},
		{"import refused", func(g *goldenImporter) {
			g.register = func(string, string) error { return errors.New("cannot import this VM") }
		}, "cannot import this VM", true, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			var f fakeImport
			g := f.importerFor(t, filepath.Join(t.TempDir(), "pull"))
			c.change(&g)
			pulled, imported := false, false
			pull, register := g.pull, g.register
			g.pull = func(ctx context.Context, o GoldenPullOptions, s func(string, ...any)) (GoldenPullResult, error) {
				pulled = true
				return pull(ctx, o, s)
			}
			g.register = func(b, n string) error { imported = true; return register(b, n) }

			err := g.pullGolden(context.Background(), R2Config{}, say(t))
			switch {
			case c.wantErr == "" && err != nil:
				t.Fatalf("got %v, want success", err)
			case c.wantErr != "" && (err == nil || !strings.Contains(err.Error(), c.wantErr)):
				t.Fatalf("got %v, want an error containing %q", err, c.wantErr)
			}
			if pulled != c.wantPull {
				t.Errorf("pulled %v, want %v", pulled, c.wantPull)
			}
			if imported != c.wantImport {
				t.Errorf("imported %v, want %v", imported, c.wantImport)
			}
			if c.wantErr != "" {
				if _, sErr := os.Stat(windowsGuest.manifestPath()); sErr == nil {
					t.Errorf("a failed import left %s, which doctor would report as a golden image", windowsGuest.manifestPath())
				}
			}
			if c.name != "lock busy" && f.locked && !f.released {
				t.Error("the machine lock was not released")
			}
		})
	}
}

// Configured means set: nothing set is no cache and no error, the Worker's
// pair is a cache, and half of it is an error naming the rest, so vm-create
// does not quietly spend 45 minutes installing for someone who meant to pull.
//
// Negative control, run by hand: return (c, false, nil) on an error in
// GoldenCacheFromEnv and the "URL only" case fails.
func TestGoldenCacheFromEnv(t *testing.T) {
	for _, c := range []struct {
		name    string
		env     map[string]string
		cached  bool
		missing string // "" is no error
	}{
		{"nothing set", nil, false, ""},
		{"the Worker", map[string]string{"IRGO_GOLDEN_URL": "https://x.example.workers.dev", "IRGO_GOLDEN_TOKEN": "r"}, true, ""},
		{"URL only", map[string]string{"IRGO_GOLDEN_URL": "https://x.example.workers.dev"}, false, "IRGO_GOLDEN_TOKEN"},
		{"token only", map[string]string{"IRGO_GOLDEN_TOKEN": "r"}, false, "IRGO_GOLDEN_URL"},
	} {
		t.Run(c.name, func(t *testing.T) {
			clearGoldenEnv(t)
			for k, v := range c.env {
				t.Setenv(k, v)
			}
			_, cached, err := GoldenCacheFromEnv()
			if cached != c.cached {
				t.Errorf("cached %v, want %v", cached, c.cached)
			}
			if c.missing == "" {
				if err != nil {
					t.Errorf("got %v, want no error", err)
				}
				return
			}
			if !errors.Is(err, ErrR2NotConfigured) || !strings.Contains(err.Error(), c.missing) {
				t.Errorf("got %v, want ErrR2NotConfigured naming %s", err, c.missing)
			}
			if strings.Contains(err.Error(), "docs/") {
				t.Errorf("a release user has no docs/ directory to read: %v", err)
			}
		})
	}
}

// bundleVMName reads the name UTM will register, from the plist this package
// writes. plutil is macOS's.
func TestBundleVMNameReadsTheConfig(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("plutil is macOS's")
	}
	if _, err := exec.LookPath("plutil"); err != nil {
		t.Skip("no plutil")
	}
	b := filepath.Join(t.TempDir(), "x.utm")
	if err := os.MkdirAll(b, 0o755); err != nil {
		t.Fatal(err)
	}
	plist := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
  <key>Information</key><dict><key>Name</key><string>irgo-golden</string></dict>
</dict></plist>`
	if err := os.WriteFile(filepath.Join(b, "config.plist"), []byte(plist), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := bundleVMName(b)
	if err != nil || got != GoldenVMName {
		t.Errorf("got %q, %v; want %s", got, err, GoldenVMName)
	}
	if _, err := bundleVMName(t.TempDir()); err == nil {
		t.Error("a bundle with no config.plist was given a name")
	}
}
