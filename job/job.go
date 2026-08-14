package job

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/joeblew999/irgo-windows-vm/utmvm"
)

// dirName is where job records live, under the one application-support root.
const dirName = "jobs"

// Dir is where job records are written.
func Dir() string { return filepath.Join(utmvm.Root(), dirName) }

// State is what is known about a job.
type State struct {
	ID      string    `json:"id"`
	Command string    `json:"command"`
	Args    []string  `json:"args"`
	PID     int       `json:"pid"`
	Started time.Time `json:"started"`

	// Alive is measured, not stored. It is the whole reason this package
	// exists: a record that says "running" because nothing checked is worse
	// than no record, since an agent waits on it forever.
	Alive bool `json:"-"`

	// Elapsed since the job started, which is what a caller actually asks.
	Elapsed time.Duration `json:"-"`
}

// path is the record for an id.
func path(id string) string { return filepath.Join(Dir(), id+".json") }

// executable is which binary Start re-runs.
//
// A seam, so tests can start a real long-lived process instead of the test
// binary — which exits immediately on an argument it does not recognise, and
// would make every liveness assertion pass or fail for the wrong reason.
var executable = os.Executable

// Start launches a command detached and returns its id.
//
// It re-executes this tool's own binary rather than running the work in a
// goroutine. The work has to outlive the process that started it: an MCP client
// disconnecting kills the server, and a 45-minute install must not die with the
// conversation that asked for it.
//
// A job already running the same command with the same arguments is returned as
// it is, rather than started twice. Two concurrent installs of one VM is the
// failure this prevents, and it is cheap to hit — a client that timed out will
// simply ask again.
func Start(command string, args []string) (State, error) {
	if existing, ok := findRunning(command, args); ok {
		return existing, nil
	}

	self, err := executable()
	if err != nil {
		return State{}, fmt.Errorf("finding this binary to re-run it: %w", err)
	}
	if err := os.MkdirAll(Dir(), 0o755); err != nil {
		return State{}, err
	}

	started := time.Now()
	id := uniqueID(command, started)

	// Output goes to the log, not to a pipe nobody reads: a detached process
	// whose stdout fills a pipe buffer blocks forever, which would look exactly
	// like the hung install this is meant to report on.
	logFile, err := os.OpenFile(filepath.Join(Dir(), id+".log"),
		os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return State{}, err
	}
	defer func() { _ = logFile.Close() }()

	cmd := exec.Command(self, append([]string{command}, args...)...)
	cmd.Stdout, cmd.Stderr = logFile, logFile
	// Its own process group, so it is not killed when the shell or the client
	// that spawned the server goes away. Platform-specific — see detach_unix.go.
	detach(cmd)
	if err := cmd.Start(); err != nil {
		return State{}, fmt.Errorf("starting %s: %w", command, err)
	}

	s := State{ID: id, Command: command, Args: args, PID: cmd.Process.Pid, Started: started}
	if err := write(s); err != nil {
		return State{}, err
	}
	// Released deliberately: this process does not wait for it, and must not
	// leave a zombie when it exits first.
	_ = cmd.Process.Release()
	return s, nil
}

// uniqueID names a job so it cannot overwrite another.
//
// The timestamp alone is not enough, and a test caught it: two jobs started in
// the same second — the same command with different arguments, which is exactly
// `iso-create -fetch` and `iso-create` — produced one id, and the second record
// replaced the first. The first job then existed, ran, and could never be asked
// about.
//
// A counter rather than a random suffix, so the name stays readable and two runs
// of the same thing sort in the order they happened.
func uniqueID(command string, started time.Time) string {
	base := fmt.Sprintf("%s-%s", command, started.Format("20060102-150405"))
	id := base
	for n := 2; ; n++ {
		if _, err := os.Stat(path(id)); os.IsNotExist(err) {
			return id
		}
		id = fmt.Sprintf("%s-%d", base, n)
	}
}

func write(s State) error {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	// Written whole, then renamed: a reader that catches a half-written file
	// would report a job that does not exist.
	tmp := path(s.ID) + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path(s.ID))
}

// Status reports a job, measuring whether its process is actually alive.
func Status(id string) (State, error) {
	b, err := os.ReadFile(path(id))
	if err != nil {
		return State{}, fmt.Errorf("no job %q: %w", id, err)
	}
	var s State
	if err := json.Unmarshal(b, &s); err != nil {
		return State{}, fmt.Errorf("job %q is unreadable: %w", id, err)
	}
	s.Alive = alive(s.PID)
	s.Elapsed = time.Since(s.Started).Round(time.Second)
	return s, nil
}

// All returns every job, newest first.
func All() ([]State, error) {
	entries, err := os.ReadDir(Dir())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // no jobs is not an error
		}
		return nil, err
	}
	var out []State
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		s, err := Status(strings.TrimSuffix(e.Name(), ".json"))
		if err != nil {
			continue // a corrupt record must not hide the others
		}
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Started.After(out[j].Started) })
	return out, nil
}

// findRunning returns a live job for the same command and arguments.
func findRunning(command string, args []string) (State, bool) {
	all, err := All()
	if err != nil {
		return State{}, false
	}
	for _, s := range all {
		if s.Alive && s.Command == command && strings.Join(s.Args, "\x00") == strings.Join(args, "\x00") {
			return s, true
		}
	}
	return State{}, false
}

// alive reports whether a process exists.
//
// Signal 0 performs the permission and existence checks and delivers nothing,
// which is the portable way to ask. os.FindProcess is not: on Unix it succeeds
// for any pid, alive or not, so a check written with it always says yes — which
// is the exact failure this package exists to avoid.
//
// A recycled pid would report a stranger's process as this job. Accepted: the
// window is a whole pid space wide, and the alternative is a supervisor that
// owns durability semantics this tool should not delegate.
func alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return p.Signal(syscall.Signal(0)) == nil
}
