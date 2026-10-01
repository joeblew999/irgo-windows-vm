package main

import (
	"bytes"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/joeblew999/irgo-windows-vm/internal/utmvm"
)

// Planted secrets, each shaped like the real thing and each reaching the
// report by a different road: the environment, .env.r2, or only a log line.
const (
	plantedEnvToken   = "planted-golden-token-7f3a9c1e5b"
	plantedGitHub     = "ghp_PlantedGitHubToken0123456789abcdef"
	plantedFileSecret = "planted-r2-secret-access-key-41d8cd98f00b"
	plantedFileURL    = "https://planted-subdomain.example.workers.dev"
	plantedLogOnly    = "ghp_OnlyInTheLog9876543210zyxwvutsrqpo"
	plantedBearer     = "eyJplantedBearerTokenValue.abc.def"
	plantedAWSKey     = "AKIAPLANTED000000000"
	plantedEmail      = "planted.person@example.com"
	plantedSigned     = "plantedSignature0123456789"
)

// TestReportNeverPrintsAPlantedSecret runs the report command end to end,
// in-process as the CLI and MCP both do, against a home, an environment, a
// .env.r2 and a log full of planted credentials, and checks none comes out.
//
// Negative control (run by hand, 1 Oct 2026): making redact return s
// unchanged fails this for every planted value and for the home directory;
// dropping the .env.r2 reading fails it for plantedFileSecret and
// plantedFileURL (and their missing [redacted:...] markers) only, which shows
// each road is checked on its own.
func TestReportNeverPrintsAPlantedSecret(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("IRGO_GOLDEN_TOKEN", plantedEnvToken)
	t.Setenv("GITHUB_TOKEN", plantedGitHub)
	// The bucket name is the golden image's name, so it stays: see publicValues.
	t.Setenv("IRGO_R2_BUCKET", utmvm.GoldenVMName)

	envFile := filepath.Join(t.TempDir(), ".env.r2")
	writeFile(t, envFile, "# the private cache\n"+
		"IRGO_R2_SECRET_ACCESS_KEY="+plantedFileSecret+"\n"+
		"export IRGO_GOLDEN_URL=\""+plantedFileURL+"\"\n")
	prev := envFiles
	envFiles = func() []string { return []string{envFile} }
	t.Cleanup(func() { envFiles = prev })

	logLines := []string{
		`time=2026-10-01T09:00:00.000+07:00 level=INFO msg=started cmd=vm-golden-pull`,
		`time=2026-10-01T09:00:01.000+07:00 level=INFO msg="pull from ` + plantedFileURL + ` with ` + plantedEnvToken + `" cmd=vm-golden-pull`,
		`time=2026-10-01T09:00:02.000+07:00 level=INFO msg="Authorization: Bearer ` + plantedBearer + `" cmd=vm-golden-pull`,
		`time=2026-10-01T09:00:03.000+07:00 level=INFO msg="key ` + plantedAWSKey + ` secret ` + plantedFileSecret + `" cmd=vm-golden-pull`,
		`time=2026-10-01T09:00:04.000+07:00 level=INFO msg="GET https://bucket.example/x?X-Amz-Signature=` + plantedSigned + `&a=1" cmd=vm-golden-pull`,
		`time=2026-10-01T09:00:05.000+07:00 level=INFO msg="pushed by ` + plantedEmail + ` using ` + plantedLogOnly + ` and ` + plantedGitHub + `" cmd=vm-golden-pull`,
		`time=2026-10-01T09:00:06.000+07:00 level=INFO msg="bundle: ` + home + `/Library/irgo-golden.utm, from /Users/someoneelse/x" cmd=vm-golden-pull`,
		`time=2026-10-01T09:00:07.000+07:00 level=ERROR msg=exit cmd=vm-golden-pull code=1 outcome=failed args=-parallel=4 err="chunk 3: sha256 mismatch"`,
	}
	writeFile(t, utmvm.LogPath(), strings.Join(logLines, "\n")+"\n")

	got, err := runReportForTest(t)
	if err != nil {
		t.Fatalf("report failed: %v\n%s", err, got)
	}

	for _, s := range []string{plantedEnvToken, plantedGitHub, plantedFileSecret, plantedFileURL,
		plantedLogOnly, plantedBearer, plantedAWSKey, plantedEmail, plantedSigned, "someoneelse"} {
		if strings.Contains(got, s) {
			t.Errorf("the report contains the planted %q", s)
		}
	}
	if strings.Contains(got, home) {
		t.Errorf("the report contains the home directory %s", home)
	}
	// And it is still a report: the redaction did not eat what triage needs.
	for _, want := range []string{
		reportMarker,
		"~/Library/irgo-golden.utm",
		"[redacted:IRGO_GOLDEN_TOKEN]",
		"[redacted:IRGO_R2_SECRET_ACCESS_KEY]",
		"[redacted:IRGO_GOLDEN_URL]",
		"| `vm-golden-pull -parallel=4` | 1 | failed | chunk 3: sha256 mismatch |",
		"lines around the last error",
		"##### glaze-status",
		"doctor -json",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the report does not contain %q", want)
		}
	}
	if t.Failed() {
		t.Logf("report:\n%s", got)
	}
}

// TestRedactLeavesWhatTriageNeeds: redaction that removes too much is a
// report nobody can act on. Each of these must survive unchanged.
//
// Negative control: adding a pattern for 64 hex digits (the obvious "catch
// every hash" rule) fails the manifest id case.
func TestRedactLeavesWhatTriageNeeds(t *testing.T) {
	r := newRedactor(nil, nil)
	for _, s := range []string{
		"pulled 6817f1b6b87aa2037c2daac597cae690b1bff71a01a4b8ea296f13275112bd2d: 64.0 GB of files",
		"native was v0.1.15 and is now github.com/joeblew999/native@v0.1.16-0.20261001015633-2fbbf2d09e65",
		"golden: irgo-golden (/Users/Shared/irgo-golden.utm)",
		"error: usage: irgo-winvm vm-golden-create -vm <installed disposable VM>",
		"https://github.com/utmapp/UTM/releases/tag/v4.7.5",
		"keyboard layout en-US; monkey=1",
	} {
		if got := r.redact(s); got != s {
			t.Errorf("redact changed a line triage needs:\n  in:  %s\n  out: %s", s, got)
		}
	}
}

// TestRedactHomeIsAWholeComponent: the home directory becomes ~ only where it
// is the whole directory, never the front of a longer name.
//
// Negative control: replacing the home regexp with strings.ReplaceAll turns
// /Users/ann-other into ~-other and fails this.
func TestRedactHomeIsAWholeComponent(t *testing.T) {
	t.Setenv("HOME", "/Users/ann")
	r := newRedactor(nil, nil)
	for in, want := range map[string]string{
		"/Users/ann/Library/x":    "~/Library/x",
		"cd /Users/ann":           "cd ~",
		`path="/Users/ann"`:       `path="~"`,
		"/Users/ann-other/thing":  "/Users/<user>/thing",
		"/Users/anna/Library":     "/Users/<user>/Library",
		"/Users/Shared/irgo.utm":  "/Users/Shared/irgo.utm",
		"C:\\Users\\dev\\out.txt": "C:\\Users\\dev\\out.txt",
	} {
		if got := r.redact(in); got != want {
			t.Errorf("redact(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestExitIsRecordedAndReadBack: what logExit writes is what lastExits reads,
// multi-line errors and quoted arguments included, and help is not an exit.
//
// Negative control: renaming the "err" attribute in logExit empties the error
// column and fails this; so does logging -h.
func TestExitIsRecordedAndReadBack(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))

	logExit(log, "vm-create", []string{"-vm", "a b"}, fmt.Errorf("%w: vm-create\n  make one with: x", errUsage))
	logExit(log, "app-create", []string{"-h"}, fmt.Errorf("wrapped: %w", flag.ErrHelp))
	logExit(log, "report", nil, nil)
	logExit(log, "app-upload", []string{"-data", strings.Repeat("A", 4000)}, nil)

	exits := lastExits(strings.Split(buf.String(), "\n"), 5)
	if len(exits) != 2 {
		t.Fatalf("read back %d exits, want 2 (help and report's own are not listed): %+v\n%s", len(exits), exits, buf.String())
	}
	e := exits[0]
	if e.cmd != "vm-create" || e.code != "2" || e.outcome != "usage" || e.args != `-vm "a b"` ||
		e.err != "usage: vm-create\n  make one with: x" {
		t.Errorf("vm-create read back as %+v", e)
	}
	if !strings.Contains(buf.String(), "level=ERROR") {
		t.Errorf("a failure was not logged at ERROR, so the excerpt cannot find it:\n%s", buf.String())
	}
	if u := exits[1]; u.code != "0" || len(u.args) > 100 {
		t.Errorf("app-upload read back as code %s with %d bytes of args; want 0 and its -data cut short", u.code, len(u.args))
	}
}

// TestFenceOutlastsBackticksInside: a log line holding ``` must not end the
// block it is quoted in.
func TestFenceOutlastsBackticksInside(t *testing.T) {
	got := fence("text", "a ``` b ```` c")
	if !strings.HasPrefix(got, "`````text\n") || !strings.HasSuffix(got, "\n`````\n") {
		t.Errorf("fence = %q, want five backticks each side", got)
	}
}

func runReportForTest(t *testing.T) (string, error) {
	t.Helper()
	return utmvm.Capture(func() error { return runTool("report", nil) })
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}
