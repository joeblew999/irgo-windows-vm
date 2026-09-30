package glazecheck

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

// Screenshots.
//
// Every windowed test of examples/conformance photographs its window when the
// binary is given -conformance.shots=<dir>, and logs one line saying where the
// picture is or why there is none (parseEvents reads it into Result.Shot or
// Result.NoShot). Check gives the binary a directory, copies each picture a
// test named back from it — out of the guest with utmvm.Pull for the VM run —
// into ShotsDir/<target>, and writes a manifest beside them. Record draws the
// Screenshots table in docs/GLAZE-STATUS.md from both targets' manifests, so
// the Mac and Windows pictures of one test sit side by side whichever ran
// last. The site publishes docs/screens, so the same files are the pictures
// on the Glaze status page.
//
// A target's directory is emptied before each run's pictures go in: a picture
// a test did not take this time must not be shown beside this run's result.

// ShotsDir is where the pictures are kept, relative to the checkout: one
// directory per target, named as the targets are.
const ShotsDir = "docs/screens/conformance"

// manifestName is the file in a target's directory listing its pictures.
const manifestName = "shots.json"

// ShotsFlag is the conformance suite's flag naming the directory its tests
// write their pictures into (examples/conformance/shots_test.go).
const ShotsFlag = "-conformance.shots="

// Manifest is one run's pictures, as recorded beside them.
type Manifest struct {
	Target   string
	When     time.Time
	Platform string
	Commit   string
	RunURL   string `json:",omitempty"`
	Verdict  string
	Tests    []ShotRow
}

// ShotRow is one test that tried to take a picture.
type ShotRow struct {
	Test    string
	Result  string // as the section's table words it
	Picture string `json:",omitempty"` // a file name in the target's directory
	Note    string `json:",omitempty"` // what the test said about the picture
	Missing string `json:",omitempty"` // why there is no picture
}

// collectShots copies every picture the run's tests named into the target's
// directory, with fetch reading one by the name the test logged, and writes
// the manifest. A picture that cannot be copied back is recorded as missing,
// with the reason, rather than failing the check: it is evidence about the
// run, not part of the verdict.
func collectShots(root string, sec *Section, fetch func(rel string) ([]byte, error), say func(string, ...any)) error {
	dir := filepath.Join(root, filepath.FromSlash(ShotsDir), sec.Target)
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	m := Manifest{
		Target: sec.Target, When: sec.When, Platform: sec.Platform,
		Commit: sec.Tree.Commit, RunURL: sec.RunURL, Verdict: sec.Verdict(),
	}
	taken := 0
	for i := range sec.Results {
		r := &sec.Results[i]
		if r.Shot == "" && r.NoShot == "" {
			continue
		}
		if r.Shot != "" {
			if err := copyShot(dir, sec.Target, r.Shot, fetch); err != nil {
				r.Shot, r.ShotNote, r.NoShot = "", "", "taken in the run, and not copied back: "+err.Error()
			}
		}
		row := ShotRow{Test: r.Name, Result: r.label(), Note: r.ShotNote, Missing: r.NoShot}
		if r.Shot != "" {
			row.Picture = path.Base(r.Shot)
			taken++
		}
		m.Tests = append(m.Tests, row)
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	if err := writeFile(filepath.Join(dir, manifestName), append(b, '\n')); err != nil {
		return err
	}
	say("screenshots: %d of %d in %s", taken, len(m.Tests), filepath.Join(ShotsDir, sec.Target))
	return nil
}

// pngMagic is the first eight bytes of every PNG.
var pngMagic = []byte("\x89PNG\r\n\x1a\n")

// copyShot fetches one picture and writes it into dir, checking it is a PNG
// the test named for this target and not something else that arrived.
func copyShot(dir, target, rel string, fetch func(string) ([]byte, error)) error {
	name, ok := strings.CutPrefix(rel, target+"/")
	if !ok || name == "" || strings.ContainsAny(name, `/\`) {
		return fmt.Errorf("%s is not a picture of %s", rel, target)
	}
	b, err := fetch(rel)
	if err != nil {
		return err
	}
	if !bytes.HasPrefix(b, pngMagic) {
		return fmt.Errorf("%s arrived as %d bytes that are not a PNG", rel, len(b))
	}
	return writeFile(filepath.Join(dir, name), b)
}

// readManifest reads a target's manifest under root; ok is false when there
// is none.
func readManifest(root, target string) (Manifest, bool, error) {
	return readManifestIn(filepath.Join(root, filepath.FromSlash(ShotsDir), target))
}

// readManifestIn reads the manifest in a target's directory.
func readManifestIn(dir string) (m Manifest, ok bool, err error) {
	b, err := os.ReadFile(filepath.Join(dir, manifestName))
	if errors.Is(err, fs.ErrNotExist) {
		return m, false, nil
	}
	if err != nil {
		return m, false, err
	}
	if err := json.Unmarshal(b, &m); err != nil {
		return m, false, fmt.Errorf("reading %s: %w", filepath.Join(dir, manifestName), err)
	}
	return m, true, nil
}

// thumbWidth is how wide a picture is drawn in the table: two side by side
// fit the site's text column without scrolling. The file itself is up to 800
// pixels wide, and the picture links to it.
const thumbWidth = 280

// gallery is the Screenshots section: one row per test that took a picture on
// either target, the Mac and Windows side by side, each with its result.
// Empty when neither target has a manifest with anything in it.
//
// HTML images rather than markdown ones, so they can be drawn smaller than
// the file and link to it; the paths are relative to docs/, where the status
// file is, and the site publishes docs/screens at the same place relative to
// its pages, so one path works on GitHub and on the site.
func gallery(root string) (string, error) {
	var ms []Manifest
	var order []string
	seen := map[string]bool{}
	for _, t := range targets {
		m, ok, err := readManifest(root, t)
		if err != nil {
			return "", err
		}
		if !ok {
			m = Manifest{Target: t}
		}
		ms = append(ms, m)
		for _, r := range m.Tests {
			if !seen[r.Test] {
				seen[r.Test] = true
				order = append(order, r.Test)
			}
		}
	}
	if len(order) == 0 {
		return "", nil
	}

	var b strings.Builder
	b.WriteString("## Screenshots\n\n")
	b.WriteString("Every test that opens a window photographs it at the moment that shows what it checked — the page loaded, the tray up, " +
		"the menu installed, the dialog open — and the picture is recorded with the run it came from. A capture that failed says why " +
		"instead of showing a picture; a black or one-colour frame counts as failed. How each is taken is in " +
		"`examples/conformance/shots_test.go`.\n\n")
	for _, m := range ms {
		switch {
		case len(m.Tests) == 0 && m.When.IsZero():
			fmt.Fprintf(&b, "- %s: no pictures recorded yet\n", titleShort(m.Target))
		default:
			from := ""
			if m.RunURL != "" {
				from = fmt.Sprintf(", [CI run](%s)", m.RunURL)
			}
			fmt.Fprintf(&b, "- %s: %s, commit `%s`, %s%s\n", titleShort(m.Target), m.When.Format("2006-01-02 15:04 -0700"), short(m.Commit), m.Platform, from)
		}
	}
	b.WriteString("\n| test | " + titleShort(TargetMac) + " | " + titleShort(TargetWindows) + " |\n|---|---|---|\n")
	for _, name := range order {
		fmt.Fprintf(&b, "| %s |", name)
		for _, m := range ms {
			b.WriteString(" " + shotCell(m, name) + " |")
		}
		b.WriteString("\n")
	}
	return b.String(), nil
}

// shotCell is one test's cell for one target: its result, and its picture or
// why there is none.
func shotCell(m Manifest, test string) string {
	for _, r := range m.Tests {
		if r.Test != test {
			continue
		}
		if r.Picture == "" {
			return r.Result + "<br>not captured: " + htmlText(r.Missing)
		}
		src := "screens/conformance/" + m.Target + "/" + r.Picture
		cell := fmt.Sprintf(`%s<br><a href="%s"><img src="%s" width="%d" alt="%s on %s"></a>`,
			r.Result, src, src, thumbWidth, htmlText(test), titleShort(m.Target))
		if r.Note != "" {
			cell += "<br><sub>" + htmlText(r.Note) + "</sub>"
		}
		return cell
	}
	if len(m.Tests) == 0 {
		return "—"
	}
	return "no picture: the test ended before it took one"
}

func titleShort(target string) string {
	if target == TargetMac {
		return "Mac"
	}
	return "Windows"
}

// htmlText makes a message safe as text inside HTML inside a table cell.
func htmlText(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "|", "&#124;", "\n", " ").Replace(s)
}

// Import records runs made elsewhere — the conformance CI job's artifacts,
// downloaded — as if they had been made here: for each target an artifact
// carries pictures for, its section of GLAZE-STATUS.md replaces this
// checkout's, and its pictures replace this checkout's. The pages workflow
// runs it before building the site, so the Glaze status page shows the
// latest CI run; without it the site shows what is committed.
//
// An artifact is found by its manifest: <dir>/…/screens/conformance/<target>/
// shots.json, with the GLAZE-STATUS.md glaze-check wrote beside screens/.
// Finding none is an error, not a quiet no-op: the caller asked for an import.
func Import(root, dir string, say func(string, ...any)) error {
	var found []string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, wErr error) error {
		if wErr != nil {
			return wErr
		}
		if d.IsDir() || d.Name() != manifestName {
			return nil
		}
		tdir := filepath.Dir(p)
		if filepath.Base(filepath.Dir(tdir)) == "conformance" && filepath.Base(filepath.Dir(filepath.Dir(tdir))) == "screens" {
			found = append(found, tdir)
		}
		return nil
	})
	if err != nil {
		return err
	}
	if len(found) == 0 {
		return fmt.Errorf("no conformance run under %s: looked for screens/conformance/<target>/%s", dir, manifestName)
	}
	done := map[string]string{}
	for _, tdir := range found {
		target := filepath.Base(tdir)
		if !isTarget(target) {
			return fmt.Errorf("%s: %q is not a target (%s)", tdir, target, strings.Join(targets, ", "))
		}
		if prev, dup := done[target]; dup {
			return fmt.Errorf("two runs for %s under %s: %s and %s", target, dir, prev, tdir)
		}
		art := filepath.Dir(filepath.Dir(filepath.Dir(tdir)))
		status, err := os.ReadFile(filepath.Join(art, filepath.Base(StatusFile)))
		if err != nil {
			return fmt.Errorf("%s has pictures and no status file beside them: %w", art, err)
		}
		sec, ok := sections(string(status))[target]
		if !ok {
			return fmt.Errorf("%s has no %s section", filepath.Join(art, filepath.Base(StatusFile)), target)
		}
		m, _, err := readManifestIn(tdir)
		if err != nil {
			return err
		}
		if m.Target != target {
			return fmt.Errorf("%s says it is %q, in the directory for %s", filepath.Join(tdir, manifestName), m.Target, target)
		}
		if err := replaceDir(tdir, filepath.Join(root, filepath.FromSlash(ShotsDir), target)); err != nil {
			return err
		}
		if _, err := record(root, target, sec); err != nil {
			return err
		}
		done[target] = tdir
		say("imported %s: %s, commit %s, %d pictures — %s", target, m.When.Format("2006-01-02 15:04 -0700"), short(m.Commit), countPictures(m), m.Verdict)
	}
	return nil
}

func isTarget(t string) bool {
	for _, x := range targets {
		if x == t {
			return true
		}
	}
	return false
}

func countPictures(m Manifest) int {
	n := 0
	for _, r := range m.Tests {
		if r.Picture != "" {
			n++
		}
	}
	return n
}

// replaceDir makes dst a copy of the files in src, and nothing else.
func replaceDir(src, dst string) error {
	if err := os.RemoveAll(dst); err != nil {
		return err
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		b, err := os.ReadFile(filepath.Join(src, e.Name()))
		if err != nil {
			return err
		}
		if err := writeFile(filepath.Join(dst, e.Name()), b); err != nil {
			return err
		}
	}
	return nil
}
