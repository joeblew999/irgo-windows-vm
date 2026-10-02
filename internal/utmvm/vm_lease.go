package utmvm

// Whose VM is whose, and when it was last used.
//
// Every VM vm-create makes gets a record under vms/ in the runtime data: who
// made it (owner.go), when, and when a command last used it. status lists
// them, and vm-reap removes clones nobody has used for longer than a lease, so
// an agent that went away does not leave 8 GiB of RAM and a growing disk
// behind on a machine others share.
//
// A record is ours, not UTM's: the bundle is in UTM's container, which this
// process cannot write. A VM with no record (irgo-win11, the golden image, or
// anything made before records existed) has no known owner and is never
// reaped.

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// VMRecord is what is known about who made a VM.
type VMRecord struct {
	Name        string    `json:"name"`
	Owner       string    `json:"owner"`
	OwnerSource string    `json:"owner_source"`
	Created     time.Time `json:"created"`
	LastUsed    time.Time `json:"last_used"`

	// OS is the system inside the VM, a guestOS name (guest.go). Empty is
	// Windows: every record written before the field existed.
	OS string `json:"os,omitempty"`

	// CreatingPID is the vm-create that is making this VM, while it is. The
	// capacity check counts the memory of a VM still being made (see
	// BeginCreate), and a dead pid is a create that was killed.
	CreatingPID int `json:"creating_pid,omitempty"`
}

// recordsDirName holds one record per VM, named by the VM's key.
const recordsDirName = "vms"

// RecordsDir is where VM records are kept.
func RecordsDir() string { return filepath.Join(appRoot(), recordsDirName) }

func recordPath(name string) string {
	return filepath.Join(RecordsDir(), fileKey(name)+".json")
}

// readRecord returns name's record; ok is false when there is none.
func readRecord(name string) (VMRecord, bool, error) {
	b, err := os.ReadFile(recordPath(name))
	if errors.Is(err, os.ErrNotExist) {
		return VMRecord{}, false, nil
	}
	if err != nil {
		return VMRecord{}, false, err
	}
	var r VMRecord
	if err := json.Unmarshal(b, &r); err != nil {
		return VMRecord{}, false, fmt.Errorf("reading %s: %w", recordPath(name), err)
	}
	return r, true, nil
}

// writeRecord replaces a record whole. Through a temporary file of its own and
// a rename, so a reader never sees half a record and two writers (vm-screen
// touches without a lock) never interleave; the write's error is checked,
// because a full disk shows up there.
func writeRecord(r VMRecord) error {
	if err := os.MkdirAll(RecordsDir(), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(RecordsDir(), ".record-*")
	if err != nil {
		return err
	}
	_, wErr := f.Write(append(b, '\n'))
	cErr := f.Close()
	if err := errors.Join(wErr, cErr); err != nil {
		_ = os.Remove(f.Name())
		return fmt.Errorf("writing the record of %s: %w", r.Name, err)
	}
	if err := os.Rename(f.Name(), recordPath(r.Name)); err != nil {
		_ = os.Remove(f.Name())
		return err
	}
	return nil
}

// ForgetVM removes name's record. None is success, so vm-delete can run twice.
func ForgetVM(name string) error {
	if err := os.Remove(recordPath(name)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// VMRecords returns every record, by name. A record that cannot be read is
// reported in bad rather than dropped, so nothing it describes is treated as
// unowned.
func VMRecords() (records []VMRecord, bad []string, err error) {
	entries, err := os.ReadDir(RecordsDir())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		p := filepath.Join(RecordsDir(), e.Name())
		b, rErr := os.ReadFile(p)
		var r VMRecord
		if rErr == nil {
			rErr = json.Unmarshal(b, &r)
		}
		if rErr != nil || r.Name == "" {
			bad = append(bad, fmt.Sprintf("%s: %v", Home(p), rErr))
			continue
		}
		records = append(records, r)
	}
	sort.Slice(records, func(i, j int) bool { return records[i].Name < records[j].Name })
	return records, bad, nil
}

// TouchVM records that a command is using name now, so its lease starts
// again. A VM with no record is left without one: using the owner's VM, or one
// made before records, does not claim it.
func TouchVM(name string) error {
	if uuidRef.MatchString(name) {
		e, err := Find(name)
		if err != nil {
			return nil // the command itself reports a VM that is not there
		}
		name = e.Name
	}
	r, ok, err := readRecord(name)
	if err != nil || !ok {
		return err
	}
	r.LastUsed = time.Now().UTC()
	return writeRecord(r)
}

// lastUse is when r was last used, or made if it never was.
func (r VMRecord) lastUse() time.Time {
	if r.LastUsed.After(r.Created) {
		return r.LastUsed
	}
	return r.Created
}

// Idle is how long ago r was last used.
func (r VMRecord) Idle(now time.Time) time.Duration { return now.Sub(r.lastUse()) }

// protectedVM reports whether name is never reaped whatever its record says:
// the owner's VM, the golden image and the clone that verifies it.
func protectedVM(name string) bool {
	for _, p := range []string{DefaultVMName, GoldenVMName, goldenVerifyName} {
		if strings.EqualFold(name, p) {
			return true
		}
	}
	return false
}

// ReapAction is what vm-reap does about one record.
type ReapAction int

const (
	ReapKeep   ReapAction = iota // in lease, protected, in use, or cannot tell
	ReapDelete                   // lease expired and nothing is using it
	ReapForget                   // UTM has no such VM: only the record goes
)

// ReapDecision is the verdict on one record, and why.
type ReapDecision struct {
	Record VMRecord
	Action ReapAction
	Why    string
}

// reapFacts is what deciding needs to know about one record's VM, gathered by
// the caller so the decision can be tested without UTM.
type reapFacts struct {
	exists    bool  // UTM lists it
	findErr   error // UTM could not be asked: cannot tell
	lockBusy  bool  // a command holds its VM lock
	lockErr   error // the lock's state could not be read: cannot tell
	creatorUp bool  // the vm-create making it is still alive
}

// decideReap is the rule. It keeps anything it cannot be sure of: deleting a
// VM somebody is using is the failure, and keeping a stale one for another
// run of vm-reap costs only memory and disk until then.
func decideReap(r VMRecord, f reapFacts, lease time.Duration, now time.Time) ReapDecision {
	d := ReapDecision{Record: r, Action: ReapKeep}
	idle := r.Idle(now)
	switch {
	case protectedVM(r.Name):
		d.Why = "protected: never reaped"
	case f.creatorUp:
		d.Why = fmt.Sprintf("being made by vm-create (pid %d)", r.CreatingPID)
	case f.lockErr != nil:
		d.Why = "cannot tell whether it is in use: " + f.lockErr.Error()
	case f.lockBusy:
		d.Why = "in use: a command holds its lock"
	case f.findErr != nil:
		d.Why = "cannot tell whether it exists: " + f.findErr.Error()
	case !f.exists:
		d.Action, d.Why = ReapForget, "UTM has no such VM; the record is left over"
	case idle < lease:
		d.Why = fmt.Sprintf("in lease: last used %s ago, lease %s", roundIdle(idle), lease)
	default:
		d.Action, d.Why = ReapDelete, fmt.Sprintf("lease expired: last used %s ago, lease %s", roundIdle(idle), lease)
	}
	return d
}

func roundIdle(d time.Duration) time.Duration {
	if d > time.Hour {
		return d.Round(time.Minute)
	}
	return d.Round(time.Second)
}

// Reap decides about every record and, with force, carries the decisions
// out: deletes each expired VM through UTM and forgets each left-over
// record. Without force it changes nothing, so the decisions are a dry run.
//
// Each VM is decided and deleted under its own lock, taken without waiting:
// a VM a command is using is busy, and is kept. So vm-reap takes no lock of
// its own, and never holds up a VM it is not deleting.
func Reap(lease time.Duration, force bool, say func(string, ...any)) ([]ReapDecision, []string, error) {
	records, bad, err := VMRecords()
	if err != nil {
		return nil, nil, err
	}
	now := time.Now()
	var out []ReapDecision
	var failed []error
	for _, r := range records {
		d, release := decideOne(r, lease, now)
		if force && d.Action != ReapKeep {
			if err := carryOut(d, say); err != nil {
				failed = append(failed, fmt.Errorf("%s: %w", r.Name, err))
				d.Why += "; FAILED: " + err.Error()
			}
		}
		if release != nil {
			release()
		}
		out = append(out, d)
	}
	return out, bad, errors.Join(failed...)
}

// decideOne gathers the facts about r and decides. When the verdict is to act,
// it returns holding r's VM lock, so nothing can start using the VM between
// the decision and the delete.
func decideOne(r VMRecord, lease time.Duration, now time.Time) (ReapDecision, func()) {
	var f reapFacts
	if protectedVM(r.Name) {
		// Decided without its lock: taking even briefly the lock of a VM
		// that is never reaped could refuse its owner's command (exit 6).
		return decideReap(r, f, lease, now), nil
	}
	if r.CreatingPID != 0 && r.CreatingPID != os.Getpid() && processAlive(r.CreatingPID) {
		f.creatorUp = true
		return decideReap(r, f, lease, now), nil
	}
	release, err := Acquire(VMLock(r.Name))
	switch {
	case errors.Is(err, ErrMutationInProgress):
		f.lockBusy = true
	case err != nil:
		f.lockErr = err
	}
	if err == nil {
		_, fErr := Find(r.Name)
		f.exists = fErr == nil
		if fErr != nil && !errors.Is(fErr, ErrNoVM) {
			f.findErr = fErr
		}
	}
	d := decideReap(r, f, lease, now)
	if d.Action == ReapKeep && release != nil {
		release()
		release = nil
	}
	return d, release
}

func carryOut(d ReapDecision, say func(string, ...any)) error {
	if d.Action == ReapDelete {
		e, err := Find(d.Record.Name)
		if err != nil {
			return err
		}
		if _, err := Delete(e.UUID, true, say); err != nil {
			return err
		}
	}
	return ForgetVM(d.Record.Name)
}

// BeginCreate is called by vm-create before it makes or boots name. It
// decides whether there is room (CheckCapacity) and, for a VM that does not
// exist yet, records c as its owner with this process as its creator, all
// under CapacityLock, so a second vm-create starting at the same moment sees
// this one's memory as taken.
//
// overcommit skips the memory half of the check (CapacityPlan.Overcommit).
//
// finish must be called when vm-create is done, whether it worked or not: it
// clears the creator, and drops the record if no VM came of it.
func BeginCreate(name string, c Caller, noGolden, overcommit bool, say func(string, ...any)) (finish func(), err error) {
	release, err := acquireWithin(10*time.Second, CapacityLock)
	if err != nil {
		return nil, err
	}
	defer release()

	e, fErr := Find(name)
	exists := fErr == nil
	if fErr != nil && !errors.Is(fErr, ErrNoVM) {
		return nil, fmt.Errorf("%w: cannot tell whether %s exists: %v", ErrNoRoom, name, fErr)
	}
	if exists && vmUsesMemory(e.Status) {
		// Already running: nothing new is started, so there is nothing to
		// check. vm-create only waits for it to answer.
		return func() {}, TouchVM(name)
	}

	plan := CapacityPlan{VM: name, Exists: exists, Overcommit: overcommit, Owner: c.ID}
	if !exists {
		plan.Disk = diskForInstall
		if !noGolden {
			if _, ok, gErr := goldenEntry(); gErr != nil {
				return nil, fmt.Errorf("%w: cannot tell whether there is a golden image: %v", ErrNoRoom, gErr)
			} else if ok {
				plan.Disk = diskForClone
			}
		}
	}
	if err := CheckCapacity(plan, say); err != nil {
		return nil, err
	}

	if exists {
		return func() {}, TouchVM(name)
	}
	now := time.Now().UTC()
	r := VMRecord{Name: name, Owner: c.ID, OwnerSource: c.Source, Created: now, LastUsed: now, CreatingPID: os.Getpid()}
	if err := writeRecord(r); err != nil {
		return nil, err
	}
	say("owner:  %s, recorded in %s", c, Home(recordPath(name)))
	return func() { finishCreate(name) }, nil
}

// finishCreate clears the creator from name's record, or removes the record
// when UTM has no such VM after all (the create failed before making it).
// Best effort: the record is ours, and vm-reap forgets one whose VM is gone.
func finishCreate(name string) {
	if _, err := Find(name); errors.Is(err, ErrNoVM) {
		_ = ForgetVM(name)
		return
	}
	r, ok, err := readRecord(name)
	if err != nil || !ok {
		return
	}
	r.CreatingPID = 0
	_ = writeRecord(r)
}
