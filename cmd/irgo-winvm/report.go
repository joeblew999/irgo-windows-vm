package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/joeblew999/irgo-windows-vm/internal/command"
	"github.com/joeblew999/irgo-windows-vm/internal/glazecheck"
	"github.com/joeblew999/irgo-windows-vm/internal/utmvm"
)

func reportFlags() *flag.FlagSet {
	fs := flag.NewFlagSet("report", flag.ContinueOnError)
	fs.Int64("lines", 40, "log lines to include, ending a few lines after the last error (or at the end of the log when nothing failed)")
	fs.String("issue", "", "print a whole issue body of this kind instead, the report inside it, for gh issue create --body-file: "+strings.Join(issueKindNames(), ", "))
	return fs
}

const reportAbout = `  Prints a markdown block to paste into an issue: this binary's version, the
  macOS version and hardware, UTM, the golden image, the last commands and
  how they exited, the log around the last error, glaze-status, and
  doctor -json. Home directories become ~, and tokens, secrets and every
  value in .env.r2 are replaced with [redacted:...]. Read it before posting.

  With -issue it prints the body of an issue of that kind, with the same
  headings as the web form and the gh command that files it at the top.
`

// reportMarker opens every report, so a triager or a workflow can tell an
// issue that carries one from an issue that does not.
const reportMarker = "<!-- irgo-winvm report v1 -->"

// reportExits is how many of the last commands the report names.
const reportExits = 5

// runReport prints the diagnostic block an issue needs, redacted. It changes
// nothing, and every part it cannot gather says so in place rather than
// failing the whole report: a report is what gets run when things are broken.
func runReport(v values, _ []string) error {
	lines := int(v.Int64("lines"))
	if lines < 1 {
		return fmt.Errorf("%w: -lines must be at least 1", errUsage)
	}
	r := newRedactor(os.Environ(), envFiles())
	kind := v.String("issue")
	if kind == "" {
		_, err := fmt.Fprint(utmvm.Out, r.redact(buildReport(lines)))
		return err
	}
	k, ok := issueKinds[kind]
	if !ok {
		return fmt.Errorf("%w: -issue %q: the kinds are %s", errUsage, kind, strings.Join(issueKindNames(), ", "))
	}
	var report string
	if k.withReport {
		report = buildReport(lines)
	}
	_, err := fmt.Fprint(utmvm.Out, r.redact(issueBody(kind, k, report)))
	return err
}

// issueRepo is where issues are filed.
const issueRepo = "joeblew999/irgo-windows-vm"

// issueKind is one of the issue forms in .github/ISSUE_TEMPLATE, for an agent
// that files with gh or the API, which cannot fill a form. Its headings are
// the form's field labels, which is how GitHub writes a form's answers, so an
// issue reads the same whichever way it was filed. issue_test.go holds the two
// to each other.
type issueKind struct {
	form, title string
	labels      []string // the form's labels, then agent-filed
	withReport  bool
	sections    []issueSection
}

type issueSection struct {
	heading, hint string
	// fence is the language of the code block the answer goes in, as the
	// form's render: does it; "" for prose.
	fence string
	// body, when set, is the answer already: the report, the checks, the
	// version.
	body func(report string) string
}

var reporterSection = issueSection{heading: "Reporting repo and agent",
	hint: "owner/repo you were working in, and the agent (e.g. Claude Code) or person who hit this"}

var checksSection = issueSection{heading: "Checks", body: func(string) string {
	return "- [ ] It is not already in docs/UPSTREAM.md or an open issue.\n" +
		"- [ ] I read the report and it holds no secret, token or personal path."
}}

var reportSection = issueSection{heading: "Diagnostic report", body: func(r string) string { return r }}

var issueKinds = map[string]issueKind{
	"bug": {form: "bug.yml", title: "[bug] ", labels: []string{"bug", "needs-triage", "agent-filed"}, withReport: true,
		sections: []issueSection{
			reporterSection,
			{heading: "Exact command", hint: "the command as run, every flag; over MCP the tool name and its args", fence: "sh"},
			{heading: "Full output", hint: "everything it printed, stdout and stderr, and the exit code; not a summary", fence: "text"},
			{heading: "Expected", hint: "what should have happened"},
			{heading: "Actual", hint: "what happened instead"},
			{heading: "Steps to reproduce", hint: "from a known state, if you have them; otherwise write: none"},
			reportSection,
			checksSection,
		}},
	"feature": {form: "feature.yml", title: "[feature] ", labels: []string{"feature", "needs-triage", "agent-filed"},
		sections: []issueSection{
			reporterSection,
			{heading: "What the calling repo is trying to do", hint: "the outcome, in a sentence or two"},
			{heading: "The workflow today", hint: "the commands or MCP calls you run now, in order, and where it stops working"},
			{heading: "Acceptance criteria", hint: "checkable statements, one per line, as - [ ] items"},
			{heading: "Proposed interface", hint: "a command, flag or MCP tool shape, if you have one; otherwise write: none"},
			{heading: "irgo-winvm version", body: func(string) string { return version }},
		}},
	"upstream": {form: "upstream.yml", title: "[upstream] ", labels: []string{"needs-triage", "agent-filed"}, withReport: true,
		sections: []issueSection{
			{heading: "Project", hint: "one of: glaze, native, UTM, not sure"},
			{heading: "UPSTREAM.md entry", hint: "the closest heading in docs/UPSTREAM.md, or none"},
			reporterSection,
			{heading: "Why it is upstream", hint: "what the project's documentation promises, and what it does instead"},
			{heading: "Minimal reproduction", hint: "the smallest program or command sequence that shows it, and on which OS"},
			reportSection,
			checksSection,
		}},
}

func issueKindNames() []string {
	names := make([]string, 0, len(issueKinds))
	for n := range issueKinds {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// issueBody is the body of an issue of kind k: the gh command that files it,
// then one heading per form field, each with its hint or its answer.
func issueBody(name string, k issueKind, report string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<!-- irgo-winvm issue body: %s (the %s form). Keep every ### heading, in order;\n", name, k.form)
	b.WriteString("     replace each _italic_ line with the answer, and tick the checks. File it with:\n")
	fmt.Fprintf(&b, "     gh issue create --repo %s --title %q --label %s --body-file <this file>\n",
		issueRepo, k.title+"<one line: what is wrong or wanted>", strings.Join(k.labels, ","))
	b.WriteString("     If gh says a label is not found, nobody has synced the labels yet: drop --label. -->\n")
	for _, s := range k.sections {
		fmt.Fprintf(&b, "\n### %s\n\n", s.heading)
		switch {
		case s.body != nil:
			b.WriteString(strings.TrimRight(s.body(report), "\n") + "\n")
		case s.fence != "":
			b.WriteString(fence(s.fence, "_"+s.hint+"_"))
		default:
			b.WriteString("_" + s.hint + "_\n")
		}
	}
	return b.String()
}

// buildReport is the report before redaction.
func buildReport(lines int) string {
	var b strings.Builder
	rows := doctorRows()
	row := func(what string) doctorRow {
		for _, r := range rows {
			if r.What == what {
				return r
			}
		}
		return doctorRow{State: "cannot tell"}
	}

	b.WriteString(reportMarker + "\n")
	b.WriteString("#### irgo-winvm report\n\n")
	b.WriteString("| | |\n|---|---|\n")
	cell := func(k, v string) { fmt.Fprintf(&b, "| %s | %s |\n", k, tableCell(v, 300)) }
	cell("irgo-winvm", fmt.Sprintf("`%s` (%s, %s/%s)", version, runtime.Version(), runtime.GOOS, runtime.GOARCH))
	cell("OS", hostOS())
	cell("hardware", hardware())
	cell("free disk", freeDisk())
	utm := row("UTM version").State
	if u := row(utmUpdateRow); u.State != "" {
		utm += "; update: " + u.State
	}
	cell("UTM", utm)
	cell("golden image", row("golden image").State+"; sealed: "+row("golden sealed").State)
	cell("generated", time.Now().Format("2006-01-02 15:04 MST"))

	logLines, logErr := readLogTail(utmvm.LogPath(), 2<<20)

	b.WriteString("\n##### Last commands\n\n")
	exits := lastExits(logLines, reportExits)
	switch {
	case logErr != nil:
		fmt.Fprintf(&b, "cannot tell: %v\n", logErr)
	case len(exits) == 0:
		b.WriteString("none recorded in the log (versions before `report` existed did not record exits)\n")
	default:
		b.WriteString("| when | command | exit | outcome | error |\n|---|---|---|---|---|\n")
		for _, e := range exits {
			fmt.Fprintf(&b, "| %s | %s | %s | %s | %s |\n", tableCell(e.when, 40),
				tableCell("`"+strings.TrimSpace(e.cmd+" "+e.args)+"`", 200), e.code, e.outcome, tableCell(e.err, 300))
		}
	}

	excerpt, where := logExcerpt(logLines, lines)
	fmt.Fprintf(&b, "\n##### Log, %s\n\n", where)
	if logErr != nil {
		fmt.Fprintf(&b, "cannot tell: %v\n", logErr)
	} else {
		b.WriteString(fence("text", strings.Join(excerpt, "\n")))
	}

	b.WriteString("\n##### glaze-status\n\n")
	b.WriteString(fence("text", glazeSummary()))

	b.WriteString("\n<details><summary>doctor -json</summary>\n\n")
	j, err := json.MarshalIndent(rows, "", "  ")
	if err != nil {
		j = []byte(err.Error())
	}
	b.WriteString(fence("json", string(j)))
	b.WriteString("\n</details>\n")
	return b.String()
}

// fence wraps body in a code fence longer than any run of backticks inside it,
// so a log line holding ``` cannot close the block early.
func fence(lang, body string) string {
	longest, run := 0, 0
	for _, r := range body {
		if r == '`' {
			run++
			longest = max(longest, run)
		} else {
			run = 0
		}
	}
	f := strings.Repeat("`", max(3, longest+1))
	return f + lang + "\n" + strings.TrimRight(body, "\n") + "\n" + f + "\n"
}

// tableCell makes s safe inside a markdown table cell: one line, no bare
// pipe, at most n runes.
func tableCell(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	s = strings.ReplaceAll(s, "|", `\|`)
	if r := []rune(s); len(r) > n {
		s = string(r[:n]) + "…"
	}
	return s
}

// hostOS names the operating system and its version.
func hostOS() string {
	if runtime.GOOS != "darwin" {
		return runtime.GOOS + " (irgo-winvm needs macOS on Apple Silicon to drive a VM)"
	}
	v, vErr := output("sw_vers", "-productVersion")
	build, _ := output("sw_vers", "-buildVersion")
	if vErr != nil {
		return "macOS, version cannot tell: " + vErr.Error()
	}
	return "macOS " + v + " (" + build + ")"
}

// hardware names the chip and memory, which decide whether a VM fits.
func hardware() string {
	if runtime.GOOS != "darwin" {
		return runtime.GOARCH
	}
	chip, err := output("sysctl", "-n", "machdep.cpu.brand_string")
	if err != nil {
		chip = "chip cannot tell"
	}
	if mem, mErr := output("sysctl", "-n", "hw.memsize"); mErr == nil {
		if n, pErr := strconv.ParseInt(mem, 10, 64); pErr == nil {
			chip += ", " + utmvm.HumanBytes(n) + " memory"
		}
	}
	// A darwin/amd64 build under Rosetta says amd64 above; this says what the
	// machine is.
	if arm, aErr := output("sysctl", "-n", "hw.optional.arm64"); aErr == nil && arm == "1" {
		chip += ", Apple Silicon"
	}
	return chip
}

// freeDisk is the space left where the media, the VMs' staging and the
// golden-image downloads go.
func freeDisk() string {
	n, err := utmvm.FreeBytes(utmvm.Root())
	if err != nil {
		return "cannot tell: " + err.Error()
	}
	return utmvm.HumanBytes(n) + " at " + utmvm.Root()
}

func output(name string, args ...string) (string, error) {
	out, err := exec.Command(name, args...).Output()
	return strings.TrimSpace(string(out)), err
}

// glazeSummary is glaze-status's verdict lines, or why there are none.
func glazeSummary() string {
	root, err := glazecheck.FindRepo()
	if err != nil {
		return "not run: glaze-status works only in a checkout of irgo-windows-vm, and this is not one"
	}
	body, err := glazecheck.Read(root)
	if err != nil {
		return "cannot tell: " + err.Error()
	}
	return strings.Join(glazecheck.Freshness(root, body), "\n")
}

// recordExits turns logExit on in runTool. Tests turn it off: they run every
// command against the real log, and their exits would read as the user's.
var recordExits = true

// logExit records how a command ended, for report. It is called for every
// command an agent can run, with this process's own logger.
func logExit(log *slog.Logger, name string, args []string, err error) {
	if errors.Is(err, flag.ErrHelp) {
		return
	}
	code := exitCode(err)
	o, _ := command.Classify(code)
	attrs := []any{"cmd", name, "code", int(code), "outcome", o.Name, "args", logArgs(args)}
	if err != nil {
		log.Error("exit", append(attrs, "err", err.Error())...)
		return
	}
	log.Info("exit", attrs...)
}

// logArgs is args for the log, each cut short: app-upload's -data is up to
// 2 MiB of base64 per call.
func logArgs(args []string) string {
	const most = 80
	out := make([]string, len(args))
	for i, a := range args {
		if r := []rune(a); len(r) > most {
			a = string(r[:most]) + "…"
		}
		if strings.ContainsAny(a, " \t\"") {
			a = strconv.Quote(a)
		}
		out[i] = a
	}
	return strings.Join(out, " ")
}

// readLogTail returns the last up to n bytes of the log, as whole lines.
func readLogTail(path string, n int64) ([]string, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	partial := false
	if fi.Size() > n {
		if _, err := f.Seek(fi.Size()-n, io.SeekStart); err != nil {
			return nil, err
		}
		partial = true
	}
	var lines []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for sc.Scan() {
		if partial {
			partial = false // the first line read starts mid-line
			continue
		}
		lines = append(lines, sc.Text())
	}
	return lines, sc.Err()
}

// exitRecord is one exit line logExit wrote.
type exitRecord struct{ when, cmd, args, code, outcome, err string }

// lastExits returns the last n exits in lines, oldest first, leaving out
// report's own.
func lastExits(lines []string, n int) []exitRecord {
	var out []exitRecord
	for _, l := range lines {
		kv := parseLogfmt(l)
		if kv["msg"] != "exit" || kv["cmd"] == "" || kv["cmd"] == "report" {
			continue
		}
		when := kv["time"]
		if t, err := time.Parse(time.RFC3339Nano, when); err == nil {
			when = t.Format("2006-01-02 15:04:05 MST")
		}
		out = append(out, exitRecord{when, kv["cmd"], kv["args"], kv["code"], kv["outcome"], kv["err"]})
	}
	if len(out) > n {
		out = out[len(out)-n:]
	}
	return out
}

// logExcerpt returns up to n lines ending a few after the last error, or the
// last n lines when nothing failed, and says which.
func logExcerpt(lines []string, n int) ([]string, string) {
	const after = 5
	where := utmvm.LogPath()
	last := -1
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.Contains(lines[i], " level=ERROR ") {
			last = i
			break
		}
	}
	if last < 0 {
		start := max(0, len(lines)-n)
		return lines[start:], fmt.Sprintf("the last %d lines (no error in the last %d), %s", len(lines)-start, len(lines), where)
	}
	end := min(len(lines), last+after+1)
	start := max(0, end-n)
	return lines[start:end], fmt.Sprintf("%d lines around the last error, %s", end-start, where)
}

// parseLogfmt reads one line of slog's text handler: key=value pairs, a value
// quoted Go-style when it holds spaces or quotes.
func parseLogfmt(line string) map[string]string {
	kv := map[string]string{}
	for line != "" {
		line = strings.TrimLeft(line, " ")
		eq := strings.IndexByte(line, '=')
		if eq <= 0 || strings.ContainsAny(line[:eq], " \"") {
			break
		}
		key := line[:eq]
		line = line[eq+1:]
		var val string
		if strings.HasPrefix(line, `"`) {
			q, err := strconv.QuotedPrefix(line)
			if err != nil {
				break
			}
			val, _ = strconv.Unquote(q)
			line = line[len(q):]
		} else if sp := strings.IndexByte(line, ' '); sp >= 0 {
			val, line = line[:sp], line[sp:]
		} else {
			val, line = line, ""
		}
		kv[key] = val
	}
	return kv
}

// envFiles is where credentials files may be, whose values the report never
// prints: .env.r2 at the checkout's root (which mise loads) and in the
// working directory. A variable so a test can plant one.
var envFiles = func() []string {
	var files []string
	if root, err := glazecheck.FindRepo(); err == nil {
		files = append(files, filepath.Join(root, ".env.r2"))
	}
	if wd, err := os.Getwd(); err == nil {
		files = append(files, filepath.Join(wd, ".env.r2"))
	}
	return files
}

// secretName matches an environment variable whose value is a credential.
// IRGO_ is the tool's own prefix: every one of them is a token, a key, or the
// private Worker's URL.
var secretName = regexp.MustCompile(`(?i)(^IRGO_|TOKEN|SECRET|PASSW|CREDENTIAL|AUTH|API_?KEY|ACCESS_?KEY|PRIVATE_?KEY|_KEY$)`)

// publicValues are values that look like settings but are the tool's own
// names, printed everywhere: the bucket is conventionally called after the
// golden image. Redacting them would turn every golden-image path into
// [redacted:IRGO_R2_BUCKET] and hide nothing.
var publicValues = map[string]bool{utmvm.GoldenVMName: true, utmvm.GoldenLinuxVMName: true, utmvm.DefaultVMName: true}

// secretPatterns catch credentials the report was not told about: in a log
// line, an error message, a URL.
var secretPatterns = []struct {
	re   *regexp.Regexp
	with string
}{
	{regexp.MustCompile(`(?i)(authorization:\s*)\S+(\s+[A-Za-z0-9._~+/=-]+)?`), "${1}[redacted]"},
	{regexp.MustCompile(`(?i)\b(bearer|basic)\s+[A-Za-z0-9._~+/=-]{8,}`), "${1} [redacted]"},
	{regexp.MustCompile(`\b(gh[pousr]_[A-Za-z0-9]{20,}|github_pat_[A-Za-z0-9_]{20,})`), "[redacted:github-token]"},
	{regexp.MustCompile(`\b(AKIA|ASIA)[A-Z0-9]{16}\b`), "[redacted:aws-key-id]"},
	{regexp.MustCompile(`(?i)([?&](X-Amz-[A-Za-z-]+|token|access_token|sig|signature|key)=)[^&\s"']+`), "${1}[redacted]"},
	{regexp.MustCompile(`(?i)\b([A-Z0-9_]*(TOKEN|SECRET|PASSWORD|PASSWD|API_KEY|ACCESS_KEY)[A-Z0-9_]*\s*[=:]\s*)("[^"]*"|\S+)`), "${1}[redacted]"},
	{regexp.MustCompile(`\b[A-Za-z0-9._%+-]+@[A-Za-z0-9-]+(\.[A-Za-z0-9-]+)*\.[A-Za-z]{2,}\b`), "[redacted:email]"},
}

// usersDir matches another account's home directory, after this one's has
// become ~.
var usersDir = regexp.MustCompile(`/Users/([^/\s"'` + "`" + `]+)`)

// redactor removes what must not leave the machine.
type redactor struct {
	values []secretValue // longest first, so a value holding another goes whole
	home   string
}

type secretValue struct{ name, value string }

// newRedactor collects the values to remove: every environment variable
// secretName matches and every value in files.
func newRedactor(env []string, files []string) redactor {
	seen := map[string]bool{}
	var r redactor
	add := func(name, value string, minLen int) {
		value = strings.TrimSpace(value)
		if len(value) < minLen || publicValues[value] || seen[value] {
			return
		}
		seen[value] = true
		r.values = append(r.values, secretValue{name, value})
	}
	for _, e := range env {
		name, value, ok := strings.Cut(e, "=")
		if ok && secretName.MatchString(name) {
			add(name, value, 6)
		}
	}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		for _, l := range strings.Split(string(b), "\n") {
			l = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(l), "export "))
			name, value, ok := strings.Cut(l, "=")
			if !ok || strings.HasPrefix(name, "#") {
				continue
			}
			if i := strings.Index(value, " #"); i >= 0 {
				value = value[:i]
			}
			add(strings.TrimSpace(name), strings.Trim(strings.TrimSpace(value), `"'`), 4)
		}
	}
	sort.Slice(r.values, func(i, j int) bool { return len(r.values[i].value) > len(r.values[j].value) })
	if h, err := os.UserHomeDir(); err == nil && len(h) > 1 {
		r.home = filepath.Clean(h)
	}
	return r
}

// redact returns s with every known secret value, every credential-shaped
// string, and every home directory replaced.
func (r redactor) redact(s string) string {
	for _, v := range r.values {
		s = strings.ReplaceAll(s, v.value, "[redacted:"+v.name+"]")
	}
	for _, p := range secretPatterns {
		s = p.re.ReplaceAllString(s, p.with)
	}
	if r.home != "" {
		// Only where the home directory is a whole path component, so
		// /Users/ann does not eat the front of /Users/anna.
		home := regexp.MustCompile(regexp.QuoteMeta(r.home) + `($|[/\s"'` + "`" + `),:;\]])`)
		s = home.ReplaceAllString(s, "~$1")
	}
	return usersDir.ReplaceAllStringFunc(s, func(m string) string {
		if m == "/Users/Shared" {
			return m
		}
		return "/Users/<user>"
	})
}
