package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/joeblew999/irgo-windows-vm/internal/job"
	"github.com/joeblew999/irgo-windows-vm/internal/utmvm"
)

func doctorFlags() *flag.FlagSet {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	fs.Bool("json", false, "print the rows as JSON, for scripts: what, state, absolute path, present")
	return fs
}

// doctorRow is one line of doctor's table, and one object of `doctor -json`.
//
// Path is absolute in the JSON and ~-abbreviated in the table. Present is its
// own field so a script need not know which State strings mean absent.
type doctorRow struct {
	What    string `json:"what"`
	State   string `json:"state"`
	Path    string `json:"path"`
	Present bool   `json:"present"`
}

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
	out("%-22s %-10s %s", "WHAT", "STATE", "WHERE")
	var missing int
	for _, r := range rows {
		out("%-22s %-10s %s", r.What, r.State, utmvm.Home(r.Path))
		if r.State == "MISSING" {
			missing++
		}
	}
	out("")
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
	rows := []doctorRow{{"irgo-winvm", version, self, true}}

	for _, e := range utmvm.Externals() {
		rows = append(rows, externalRow(e))
	}
	for _, t := range utmvm.ISOTools() {
		state := "MISSING"
		if t.Found() {
			state = "ok"
		}
		rows = append(rows, doctorRow{t.Name, state, t.Where(), t.Found()})
	}
	for _, r := range utmvm.Records() {
		rows = append(rows, recordRow(r))
	}
	return append(rows, jobsRow())
}

func externalRow(e utmvm.External) doctorRow {
	state := "MISSING"
	if e.Present {
		state = "ok"
		if e.Bytes > 0 {
			state = utmvm.HumanBytes(e.Bytes)
		}
	}
	return doctorRow{e.Name, state, e.Path, e.Present}
}

func recordRow(r utmvm.External) doctorRow {
	fi, err := os.Stat(r.Path)
	switch {
	case err != nil:
		return doctorRow{r.Name, "not yet", r.Path, false}
	case r.Dir:
		return doctorRow{r.Name, "written", r.Path, true}
	default:
		return doctorRow{r.Name, utmvm.HumanBytes(fi.Size()), r.Path, true}
	}
}

// jobsRow reports the jobs directory, which grows by a record and a log per
// detached run. It is here rather than in utmvm.Records because package job
// imports utmvm. The size is what is kept after pruning.
func jobsRow() doctorRow {
	all, err := job.All()
	if err != nil || len(all) == 0 {
		return doctorRow{"jobs", "none yet", job.Dir(), false}
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
	return doctorRow{"jobs", state, job.Dir(), true}
}
