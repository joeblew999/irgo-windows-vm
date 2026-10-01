package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
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
	var missing int
	var update string
	for _, r := range rows {
		where := utmvm.Home(r.Path)
		if where == "" {
			where = r.URL
		}
		out("%-22s %-12s %s", r.What, r.State, where)
		if r.State == "MISSING" {
			missing++
		}
		if r.What == utmUpdateRow {
			update = r.Note
		}
	}
	out("")
	out("%s", update)
	if missing == 0 {
		out("nothing missing.")
		return nil
	}
	out("%d missing. In order: irgo-winvm iso-create -fetch, then vm-create -install.", missing)
	return nil
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
		state := "MISSING"
		if t.Found() {
			state = "ok"
		}
		rows = append(rows, doctorRow{What: t.Name, State: state, Path: t.Where(), Present: t.Found()})
	}
	for _, r := range utmvm.Records() {
		rows = append(rows, recordRow(r))
	}
	rows = append(rows, jobsRow())
	return append(rows, goldenRows()...)
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

func externalRow(e utmvm.External) doctorRow {
	state := "MISSING"
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
