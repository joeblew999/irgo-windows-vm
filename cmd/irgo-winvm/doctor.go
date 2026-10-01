package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/joeblew999/irgo-windows-vm/internal/job"
	"github.com/joeblew999/irgo-windows-vm/internal/utmvm"
)

func doctorFlags() *flag.FlagSet {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	fs.Bool("json", false, "print the rows as JSON, for scripts: what, state, absolute path, present, and url and note on the UTM release rows")
	return fs
}

// doctorRow is one line of doctor's table, and one object of `doctor -json`.
//
// Path is absolute in the JSON and ~-abbreviated in the table. Present is its
// own field so a script need not know which State strings mean absent. The UTM
// release rows have no path: Present says whether the answer is known, URL is
// the release page, and Note says it in a sentence.
type doctorRow struct {
	What    string `json:"what"`
	State   string `json:"state"`
	Path    string `json:"path"`
	Present bool   `json:"present"`
	URL     string `json:"url,omitempty"`
	Note    string `json:"note,omitempty"`
}

// checkUTMReleases asks doctor's cache, or GitHub, for UTM's latest releases.
// A variable so tests never touch the network. Three seconds, because doctor
// is what gets run when something is wrong, and the network may be it.
var checkUTMReleases = func() utmvm.UTMReleaseCheck { return utmvm.CheckUTMReleases(3 * time.Second) }

// utmUpdateRow is the row whose Note answers "should I update UTM"; the table
// prints it again underneath.
const utmUpdateRow = "UTM update"

// runDoctor prints what is installed, what is missing, and where the log,
// screenshots and jobs are, as one table or, with -json, for scripts.
func runDoctor(v values, _ []string) error {
	rows := doctorRows()

	// Through utmvm.Out, not os.Stdout, so MCP captures it like any output.
	if v.Bool("json") {
		enc := json.NewEncoder(utmvm.Out)
		enc.SetIndent("", "  ")
		return enc.Encode(rows)
	}

	out := utmvm.Reporter("doctor")
	out("%-22s %-12s %s", "WHAT", "STATE", "WHERE")
	var update, room string
	for _, r := range rows {
		where := utmvm.Home(r.Path)
		if where == "" {
			where = r.URL
		}
		out("%-22s %-12s %s", r.What, r.State, where)
		switch r.What {
		case utmUpdateRow:
			update = r.Note
		case "capacity":
			room = "Capacity " + r.Note
		}
	}
	out("")
	out("%s", update)
	out("%s", room)
	out("")
	for _, line := range nextSteps(measureSetup()) {
		out("%s", line)
	}
	return nil
}

// setup is what the next steps depend on, measured by measureSetup and kept
// apart from it so nextSteps can be tested without a Mac set up each way.
type setup struct {
	utm        string // installed version, "" when UTM is not installed
	media      bool   // the Windows installer is built
	isoTools   bool   // wimlib and a masterer are installed
	brew       bool   // Homebrew is here to install them
	vm         bool   // the default VM's disk exists
	golden     bool   // a golden image is registered
	cache      string // the private cache's location, "" when not configured
	cacheError error  // the cache half configured
}

func measureSetup() setup {
	var s setup
	if in, err := utmvm.DetectUTM(); err == nil {
		s.utm = in.Version
	}
	for _, e := range utmvm.Externals() {
		if e.Name == "Windows 11 ARM64 ISO" {
			s.media = e.Present
		}
	}
	s.isoTools = true
	for _, t := range utmvm.ISOTools() {
		s.isoTools = s.isoTools && t.Found()
	}
	if _, err := exec.LookPath("brew"); err == nil {
		s.brew = true
	} else if _, err := os.Stat("/opt/homebrew/bin/brew"); err == nil {
		s.brew = true
	}
	if b, err := utmvm.BundlePath(utmvm.DefaultVMName); err == nil {
		_, sErr := os.Stat(utmvm.DiskPath(b))
		s.vm = sErr == nil
	}
	s.golden = utmvm.Golden().Present
	r, cached, err := utmvm.GoldenCacheFromEnv()
	if cached {
		s.cache = r.Where()
	}
	s.cacheError = err
	return s
}

// nextSteps is what a newcomer does next, in order, each step marked done,
// next (the first not done) or later. Every line names the command to run,
// and nothing assumes a checkout of the repository.
func nextSteps(s setup) []string {
	var lines []string
	first := true
	step := func(done bool, title string, how ...string) {
		mark := "   "
		switch {
		case done:
			mark = " ✓ "
		case first:
			mark = " → "
			first = false
		}
		lines = append(lines, mark+title)
		if !done {
			for _, h := range how {
				lines = append(lines, "      "+h)
			}
		}
	}
	lines = append(lines, "Next, in order:")

	step(s.utm != "", "UTM, the hypervisor"+ifElse(s.utm != "", " ("+s.utm+")", ""),
		"vm-create installs it from UTM's signed .dmg on GitHub, or: brew install --cask utm")

	// The installer is only needed when the VM will be installed from it.
	if !s.vm && !s.golden && s.cache == "" {
		how := []string{"irgo-winvm iso-create -fetch",
			"downloads Windows 11 ARM64 from Microsoft (4.2 GB) and builds the installer from it"}
		if !s.isoTools && !s.brew {
			how = append(how, "it installs wimlib and xorriso with Homebrew, which is not here: https://brew.sh")
		}
		step(s.media, "the Windows installer", how...)
	}

	vm := []string{"irgo-winvm vm-create -install",
		"installs Windows unattended, about 45 minutes; you click nothing"}
	switch {
	case s.golden:
		vm = []string{"irgo-winvm vm-create", "clones the golden image and boots it: seconds"}
	case s.cache != "":
		vm = []string{"irgo-winvm vm-create -install",
			"pulls the golden image from your private cache (" + s.cache + "), minutes, then clones it"}
	}
	vm = append(vm, "macOS asks once whether your terminal may control UTM: click OK",
		"(if you clicked Don't Allow: System Settings > Privacy & Security > Automation)")
	step(s.vm, "a Windows VM", vm...)

	step(false, "your program, on Windows",
		"GOOS=windows GOARCH=arm64 CGO_ENABLED=0 go build -o app.exe .",
		"irgo-winvm app-create app.exe        (-gui for anything with a window)")

	if s.cacheError != nil {
		lines = append(lines, "", "The private golden-image cache is half configured:", "  "+s.cacheError.Error())
	}
	lines = append(lines, "",
		"For an AI agent: claude mcp add irgo-winvm -- irgo-winvm mcp",
		"Guide: "+utmvm.SiteURL+"mcp.html")
	return lines
}

func ifElse(c bool, a, b string) string {
	if c {
		return a
	}
	return b
}

// doctorRows is everything doctor reports, measured now.
func doctorRows() []doctorRow {
	// The binary first: a pasted report must say what produced it, and "dev"
	// versus a tag is the difference between a local build and a release.
	self, err := os.Executable()
	if err != nil {
		self = "(this binary)"
	}
	rows := []doctorRow{{What: "irgo-winvm", State: version, Path: self, Present: true}}

	for _, e := range utmvm.Externals() {
		rows = append(rows, externalRow(e))
		if e.Name == "UTM.app" {
			in, dErr := utmvm.DetectUTM()
			rows = append(rows, utmReleaseRows(in, dErr == nil, checkUTMReleases())...)
		}
	}
	for _, t := range utmvm.ISOTools() {
		state := "not yet" // iso-create installs it
		if t.Found() {
			state = "ok"
		}
		rows = append(rows, doctorRow{What: t.Name, State: state, Path: t.Where(), Present: t.Found()})
	}
	for _, r := range utmvm.Records() {
		rows = append(rows, recordRow(r))
	}
	rows = append(rows, jobsRow(), vmRecordsRow())
	rows = append(rows, goldenRows()...)
	return append(rows, capacitySummary(utmvm.Capacity()))
}

// utmReleaseRows reports the installed UTM, the latest stable release and the
// latest pre-release on GitHub, and whether an update is available: yes, no,
// or cannot tell.
func utmReleaseRows(in utmvm.Install, present bool, c utmvm.UTMReleaseCheck) []doctorRow {
	installed := doctorRow{What: "UTM version", State: in.Version, Path: utmvm.AppPath, Present: present}
	if !present {
		installed.State = "MISSING"
	}

	var asOf string
	if c.Stale {
		asOf = fmt.Sprintf(" (as of %s; GitHub did not answer)", c.Releases.Checked.Format("2 Jan 2006"))
	}
	stable := doctorRow{What: "UTM latest stable", State: "cannot tell", Note: c.Err}
	if r := c.Releases.Stable; c.Known && r != nil {
		stable = doctorRow{What: stable.What, State: r.Version, Present: true, URL: r.Page,
			Note: "released " + day(r.Published) + "; what vm-create installs" + asOf}
	}
	pre := doctorRow{What: "UTM latest beta", State: "cannot tell", Note: c.Err}
	if r := c.Releases.Pre; c.Known && r != nil {
		pre = doctorRow{What: pre.What, State: r.Version, Present: true, URL: r.Page,
			Note: "pre-release of " + day(r.Published) + "; vm-create never installs one" + asOf}
	} else if c.Known {
		pre = doctorRow{What: pre.What, State: "none", Present: true,
			Note: "no pre-release newer than the latest stable" + asOf}
	}

	verdict := doctorRow{What: utmUpdateRow, State: "cannot tell", Note: utmUpdateNote(in, present, c)}
	// A missing UTM has version "", which UTMUpdateFor cannot compare.
	switch utmvm.UTMUpdateFor(in.Version, c) {
	case utmvm.UTMUpdateAvailable:
		verdict.State, verdict.Present, verdict.URL = "available", true, c.Releases.Stable.Page
	case utmvm.UTMUpToDate:
		verdict.State, verdict.Present = "none", true
	case utmvm.UTMUpdateCannotTell:
	}
	return []doctorRow{installed, stable, pre, verdict}
}

// utmUpdateNote says in a sentence or two whether to update UTM, and names
// the latest pre-release, for the line under doctor's table.
func utmUpdateNote(in utmvm.Install, present bool, c utmvm.UTMReleaseCheck) string {
	var b strings.Builder
	st := c.Releases.Stable
	known := c.Known && st != nil
	why := c.Err
	if why == "" {
		why = "GitHub lists no stable release with a UTM.dmg"
	}
	switch {
	case !present && known:
		fmt.Fprintf(&b, "UTM is not installed; vm-create installs the latest stable release, %s.", st.Version)
	case !present:
		fmt.Fprintf(&b, "UTM is not installed, and the latest stable release cannot be told: %s.", why)
	default:
		switch utmvm.UTMUpdateFor(in.Version, c) {
		case utmvm.UTMUpdateAvailable:
			fmt.Fprintf(&b, "UTM update available: %s, released %s; installed %s. %s",
				st.Version, day(st.Published), in.Version, st.Page)
		case utmvm.UTMUpToDate:
			if in.Version == st.Version {
				fmt.Fprintf(&b, "UTM %s is the latest stable release; no update.", in.Version)
			} else {
				fmt.Fprintf(&b, "UTM %s is newer than the latest stable release, %s: a pre-release.", in.Version, st.Version)
			}
		case utmvm.UTMUpdateCannotTell:
			if known {
				fmt.Fprintf(&b, "Cannot tell whether UTM is up to date: the installed version %q is not a version number.", in.Version)
			} else {
				fmt.Fprintf(&b, "Cannot tell whether UTM %s is up to date: %s.", in.Version, why)
			}
		}
	}
	if c.Known && c.Releases.Pre != nil {
		fmt.Fprintf(&b, " %s (%s) is a pre-release; vm-create never installs one.",
			c.Releases.Pre.Version, day(c.Releases.Pre.Published))
	}
	if present && !in.Compatible {
		fmt.Fprintf(&b, " The VM config is verified against UTM %s, a different major version.", utmvm.VerifiedVersion)
	}
	if c.Stale {
		fmt.Fprintf(&b, " Release data from %s; GitHub did not answer: %s.",
			c.Releases.Checked.Format("2 Jan 2006 15:04 MST"), c.Err)
	}
	return b.String()
}

func day(t time.Time) string { return t.Format("2 Jan 2006") }

// notYet is the state of a row whose absence is not a problem: optional, or
// fetched by the command that needs it.
var notYet = map[string]string{
	"Go toolchain":        "optional", // for building your .exe, and glaze-check
	"UTM guest tools ISO": "not yet",  // vm-create downloads it
	"the VM itself":       "not yet",  // UTM makes it on first use
}

func externalRow(e utmvm.External) doctorRow {
	state := "MISSING"
	if s, ok := notYet[e.Name]; ok {
		state = s
	}
	if e.Present {
		state = "ok"
		if e.Bytes > 0 {
			state = utmvm.HumanBytes(e.Bytes)
		}
	}
	return doctorRow{What: e.Name, State: state, Path: e.Path, Present: e.Present}
}

func recordRow(r utmvm.External) doctorRow {
	fi, err := os.Stat(r.Path)
	switch {
	case err != nil:
		return doctorRow{What: r.Name, State: "not yet", Path: r.Path}
	case r.Dir:
		return doctorRow{What: r.Name, State: "written", Path: r.Path, Present: true}
	default:
		return doctorRow{What: r.Name, State: utmvm.HumanBytes(fi.Size()), Path: r.Path, Present: true}
	}
}

// jobsRow reports the jobs directory, which grows by a record and a log per
// detached run. It is here rather than in utmvm.Records because package job
// imports utmvm. The size is what is kept after pruning.
// vmRecordsRow is how many VMs have a recorded owner; status lists them.
func vmRecordsRow() doctorRow {
	r := doctorRow{What: "VM owners", State: "none yet", Path: utmvm.RecordsDir(),
		Note: "irgo-winvm status lists each VM with its owner and last use"}
	if _, err := os.Stat(r.Path); err != nil {
		return r
	}
	r.Present = true
	records, bad, err := utmvm.VMRecords()
	switch {
	case err != nil:
		r.State = "unreadable"
	case len(bad) > 0:
		r.State = fmt.Sprintf("%d, %d unreadable", len(records), len(bad))
	default:
		r.State = fmt.Sprintf("%d recorded", len(records))
	}
	return r
}

func jobsRow() doctorRow {
	all, err := job.All()
	if err != nil || len(all) == 0 {
		return doctorRow{What: "jobs", State: "none yet", Path: job.Dir()}
	}
	running := 0
	for _, j := range all {
		if j.Alive {
			running++
		}
	}
	state := utmvm.HumanBytes(job.Size())
	if running > 0 {
		state = fmt.Sprintf("%d running", running)
	}
	return doctorRow{What: "jobs", State: state, Path: job.Dir(), Present: true}
}
