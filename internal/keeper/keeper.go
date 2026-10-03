// Package keeper is the loop `irgo-winvm keeper` runs on a Mac, under
// pitchfork: each pass it keeps the Mac awake while a VM runs, starts again a
// VM marked keep-running that has stopped, and writes the VMs down for
// claude-rig, whose report to fleet-api carries them
// (docs/concepts/architecture.md, "The keeper"). It sends nothing itself.
//
// It reaches UTM only through the UTM interface the CLI supplies, which has
// no way to quit UTM or to stop, delete or restart a VM: the keeper runs
// unattended beside VMs other people own, and the one thing it may do to a VM
// is start one that is marked and stopped. So it builds and is tested on
// every OS, against fakes.
package keeper

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	fleet "github.com/joeblew999/fleet-api/sdk/go"
)

// VM is one row of UTM's list.
type VM struct {
	Name, Status string
	Running      bool // the state runs the VM (not stopped, not paused)
}

// Record is what the tool knows of a VM it made.
type Record struct {
	Name, Owner, OS string
	KeepRunning     bool
}

// UTM is everything the keeper may ask of UTM. Nothing here quits it, or
// stops, deletes or restarts a VM.
type UTM interface {
	Installed() bool
	// Running asks the operating system, never UTM, so it never opens UTM.
	Running() (bool, error)
	// Open opens UTM, and sends it nothing for two seconds, if it is not
	// running.
	Open() error
	// List is UTM's VMs; called only once UTM is running.
	List() ([]VM, error)
	// Start starts a stopped VM, and never restarts UTM to do it.
	Start(name string) error
	// Lock takes the VM's mutation lock without waiting, so a VM a command
	// is using is not started under it.
	Lock(name string) (release func(), err error)
	// Records is every VM record.
	Records() ([]Record, error)
}

// Awake holds the Mac awake, and lets it go.
type Awake interface {
	Hold() error    // returns once the hold is checked to be in place
	Release() error // returns once it is checked to be gone
	Held() bool
	// Holder is what holds it, for a person: "caffeinate pid 123".
	Holder() string
}

// Config is a keeper's parts. A zero Every takes the default.
type Config struct {
	UTM   UTM
	Awake Awake
	// Power reads the power source (device.Power): the Mac is held awake on
	// AC power only.
	Power func() *fleet.DevicePower
	// Write keeps the VMs for claude-rig's report (WriteVMs, to its file);
	// nil writes nothing.
	Write func(VMsFile) error

	Every time.Duration // between passes: 15 s
	Now   func() time.Time
	Say   func(string, ...any)
}

// Defaults.
const (
	DefaultEvery = 15 * time.Second
	// Restart backoff: the first start of a stopped VM is at once, then 30 s,
	// doubling to at most 30 min between tries. A start counts as having
	// worked once the VM has run for stableAfter; one that stops sooner
	// counts as failed, so a VM that crashes on boot is tried less and less
	// often and never in a tight loop.
	backoffBase = 30 * time.Second
	backoffMax  = 30 * time.Minute
	stableAfter = 5 * time.Minute
)

// Keeper is one running keeper.
type Keeper struct {
	cfg   Config
	vms   map[string]*vmState // by lower-case name
	saidT map[string]string   // the last thing said on each topic, so a pass repeats nothing
}

// vmState is what the keeper remembers of a keep-running VM between passes.
type vmState struct {
	starts      int       // starts UTM took since the keeper began
	attempts    int       // starts since it last ran for stableAfter
	lastAttempt time.Time // zero: never tried
	upSince     time.Time // zero: not seen running
}

// New is a keeper with its defaults filled in.
func New(cfg Config) *Keeper {
	if cfg.Every == 0 {
		cfg.Every = DefaultEvery
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Say == nil {
		cfg.Say = func(string, ...any) {}
	}
	return &Keeper{cfg: cfg, vms: map[string]*vmState{}, saidT: map[string]string{}}
}

// Run passes every cfg.Every until ctx ends, then stops: lets the Mac sleep
// again and writes that the keeper stopped.
func (k *Keeper) Run(ctx context.Context) {
	t := time.NewTicker(k.cfg.Every)
	defer t.Stop()
	for {
		k.Pass()
		select {
		case <-ctx.Done():
			k.Stop()
			return
		case <-t.C:
		}
	}
}

// Pass is one turn of the loop: the VMs, the hold, the VMs written down. It
// never fails; what goes wrong is said, and tried again on a later pass.
func (k *Keeper) Pass() {
	now := k.cfg.Now()
	power := k.cfg.Power()
	vms := k.keep(now, true)
	k.hold(power, vms)
	k.write(VMsFile{TS: now.UnixMilli(), VMs: vms.Section})
}

// Once reads the VMs and returns what the keeper would write, changing
// nothing: it opens no UTM, starts no VM, holds nothing and writes nothing
// (`keeper -once`).
func (k *Keeper) Once() VMsFile {
	now := k.cfg.Now()
	return VMsFile{TS: now.UnixMilli(), VMs: k.keep(now, false).Section}
}

// Stop lets the Mac sleep again and writes that the keeper stopped, so the
// report says the VMs are not known rather than showing the last list.
func (k *Keeper) Stop() {
	if k.cfg.Awake.Held() {
		if err := k.cfg.Awake.Release(); err != nil {
			k.cfg.Say("letting the Mac sleep again: %v", err)
		} else {
			k.cfg.Say("the Mac may sleep again: the keeper is stopping")
		}
	}
	now := k.cfg.Now()
	k.write(VMsFile{TS: now.UnixMilli(), VMs: unknownVMs("the VM keeper stopped at " + now.Format("2006-01-02 15:04"))})
}

// VMsFile is what the keeper writes for claude-rig's report: when it read
// the VMs (Unix milliseconds), and the report's vms section, in fleet-api's
// shape (its Go SDK's type).
type VMsFile struct {
	TS  int64            `json:"ts"`
	VMs *fleet.DeviceVMs `json:"vms"`
}

// WriteVMs writes f to path through a temporary file beside it and a
// rename, so a reader never sees half of it, and makes the folder if it is
// missing.
func WriteVMs(path string, f VMsFile) error {
	b, err := json.Marshal(f)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+"-*")
	if err != nil {
		return err
	}
	_, wErr := tmp.Write(b)
	cErr := tmp.Close()
	if err := errors.Join(wErr, cErr); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	return nil
}

// write writes f, saying a failure once until it changes.
func (k *Keeper) write(f VMsFile) {
	if k.cfg.Write == nil {
		return
	}
	if err := k.cfg.Write(f); err != nil {
		k.say("write", "writing the VMs down for claude-rig's report: %v; trying again next pass", err)
		return
	}
	k.say("write", "")
}

// say says msg on topic unless the last thing said on it was msg, so a state
// that lasts is said once, not every pass.
func (k *Keeper) say(topic, format string, a ...any) {
	msg := fmt.Sprintf(format, a...)
	if k.saidT[topic] == msg {
		return
	}
	k.saidT[topic] = msg
	if msg != "" { // "" only forgets what was said, so it is said again when it recurs
		k.cfg.Say("%s", msg)
	}
}

// vmView is the VMs as the keeper last saw them, for the hold and the
// file.
type vmView struct {
	Section *fleet.DeviceVMs
	List    []VM
}

// keep is the keep-running part of a pass: open UTM if a marked VM needs it,
// start what is marked and stopped, and return what UTM lists afterwards.
// With act false it only looks: UTM is listed if it is running, and nothing
// is opened or started.
func (k *Keeper) keep(now time.Time, act bool) vmView {
	u := k.cfg.UTM
	recs, rErr := u.Records()
	if rErr != nil {
		k.say("records", "reading the VM records: %v; no VM is started until they can be read", rErr)
	} else {
		k.say("records", "")
	}
	var keep []string
	for _, r := range recs {
		if r.KeepRunning {
			keep = append(keep, r.Name)
		}
	}
	if !u.Installed() {
		k.say("utm", "UTM is not installed; there are no VMs to keep")
		return k.view(&fleet.DeviceVMs{Status: fleet.DeviceVMsStatusNone}, nil, recs)
	}
	up, err := u.Running()
	if err != nil {
		k.say("utm", "whether UTM is running is not known (%v); nothing is sent to it this pass", err)
		return k.view(unknownVMs("whether UTM is running is not known: "+err.Error()), nil, recs)
	}
	if !up {
		if len(keep) == 0 || !act {
			if act {
				k.say("utm", "UTM is not running, and no VM is marked keep-running: leaving it closed")
			}
			return k.view(okVMs(false), nil, recs)
		}
		k.cfg.Say("UTM is not running, and %d VMs must keep running (%s): opening it", len(keep), strings.Join(keep, ", "))
		if err := u.Open(); err != nil {
			k.say("utm", "opening UTM: %v; trying again next pass", err)
			return k.view(unknownVMs("UTM is not running and could not be opened: "+err.Error()), nil, recs)
		}
	}
	list, err := u.List()
	if err != nil {
		k.say("utm", "listing UTM's VMs: %v; nothing is started this pass", err)
		return k.view(unknownVMs("listing UTM's VMs: "+err.Error()), nil, recs)
	}
	k.say("utm", "")
	if act && rErr == nil && k.startStopped(now, keep, list) {
		if again, err := u.List(); err == nil {
			list = again
		}
	}
	return k.view(okVMs(true), list, recs)
}

// startStopped starts each marked VM UTM lists stopped whose backoff has
// passed, and reports whether it tried any.
func (k *Keeper) startStopped(now time.Time, keep []string, list []VM) (tried bool) {
	for _, name := range keep {
		st := k.state(name)
		i := slices.IndexFunc(list, func(v VM) bool { return strings.EqualFold(v.Name, name) })
		if i < 0 {
			k.say("vm:"+name, "%s is marked keep-running, and UTM has no VM of that name; nothing to start", name)
			continue
		}
		v := list[i]
		switch {
		case v.Running:
			if st.upSince.IsZero() {
				st.upSince = now
			}
			if now.Sub(st.upSince) >= stableAfter {
				st.attempts = 0
			}
			k.say("vm:"+name, "%s is %s", name, v.Status)
			continue
		case !strings.EqualFold(v.Status, "stopped"):
			// paused, or on its way somewhere: left alone.
			st.upSince = time.Time{}
			k.say("vm:"+name, "%s is %s; only a stopped VM is started", name, v.Status)
			continue
		}
		st.upSince = time.Time{}
		if wait := backoff(st.attempts); !st.lastAttempt.IsZero() && now.Sub(st.lastAttempt) < wait {
			k.say("vm:"+name, "%s is stopped; next try at %s (%d tries so far)", name, st.lastAttempt.Add(wait).Format("15:04:05"), st.attempts)
			continue
		}
		tried = true
		st.lastAttempt = now
		release, err := k.cfg.UTM.Lock(name)
		if err != nil {
			k.say("vm:"+name, "%s is stopped, and its lock is held (%v): a command is using it; trying again later", name, err)
			continue
		}
		st.attempts++
		k.cfg.Say("%s is stopped and marked keep-running: starting it (try %d)", name, st.attempts)
		began := k.cfg.Now()
		err = k.cfg.UTM.Start(name)
		release()
		if err != nil {
			k.cfg.Say("starting %s: %v; next try in %s", name, err, backoff(st.attempts))
			k.saidT["vm:"+name] = ""
			continue
		}
		st.starts++
		k.cfg.Say("%s: UTM took the start in %s", name, k.cfg.Now().Sub(began).Round(100*time.Millisecond))
		k.saidT["vm:"+name] = ""
	}
	return tried
}

// backoff is how long after a try the next may be: none before the first,
// then 30 s doubling to 30 min.
func backoff(attempts int) time.Duration {
	if attempts <= 0 {
		return 0
	}
	d := backoffBase << (attempts - 1)
	if d > backoffMax || d <= 0 {
		return backoffMax
	}
	return d
}

func (k *Keeper) state(name string) *vmState {
	key := strings.ToLower(name)
	st := k.vms[key]
	if st == nil {
		st = &vmState{}
		k.vms[key] = st
	}
	return st
}

// hold keeps the Mac awake while a VM runs on AC power, and lets it sleep
// otherwise. On battery it holds nothing, and when the power source cannot
// be told it holds nothing and says so. When UTM's VMs cannot be listed,
// whether one runs cannot be told, and on AC that holds: letting VMs sleep
// is the harm, and holding costs only power.
func (k *Keeper) hold(power *fleet.DevicePower, vms vmView) {
	var running []string
	for _, v := range vms.List {
		if v.Running {
			running = append(running, v.Name)
		}
	}
	unknown := vms.Section.Status == fleet.DeviceVMsStatusUnknown
	if unknown {
		running = []string{"whatever UTM runs (its VMs cannot be listed)"}
	}
	ac := power != nil && power.Status == fleet.DevicePowerStatusOk && power.Source != nil && *power.Source == fleet.DevicePowerSourceAc
	want := len(running) > 0 && ac
	var why string
	switch {
	case len(running) == 0:
		why = "no VM is running"
	case power == nil || power.Status != fleet.DevicePowerStatusOk || power.Source == nil:
		why = fmt.Sprintf("%d VMs are running, and the power source cannot be told, so nothing is held", len(running))
	case !ac:
		why = fmt.Sprintf("%d VMs are running on %s power, and the Mac is kept awake on AC power only", len(running), *power.Source)
	}
	a := k.cfg.Awake
	switch {
	case want && !a.Held():
		if err := a.Hold(); err != nil {
			k.say("awake", "keeping the Mac awake for %s: %v; trying again next pass", strings.Join(running, ", "), err)
			return
		}
		k.say("awake", "keeping the Mac awake while %s run on AC power (%s, checked in pmset -g assertions)", strings.Join(running, ", "), a.Holder())
	case !want && a.Held():
		if err := a.Release(); err != nil {
			k.say("awake", "letting the Mac sleep again (%s): %v; trying again next pass", why, err)
			return
		}
		k.say("awake", "the Mac may sleep again: %s", why)
	case !want && why != "":
		k.say("awake", "not holding the Mac awake: %s", why)
	}
}

// utmUp is whether the section says UTM is running.
func utmUp(s *fleet.DeviceVMs) bool { return s.ManagerRunning != nil && *s.ManagerRunning }

// okVMs is an ok section: UTM, running or not.
func okVMs(running bool) *fleet.DeviceVMs {
	return &fleet.DeviceVMs{Status: fleet.DeviceVMsStatusOk, Manager: fleet.String("utm"), ManagerRunning: fleet.Bool(running)}
}

// maxVMs bounds the list, as fleet-api bounds every list.
const maxVMs = 32

func unknownVMs(why string) *fleet.DeviceVMs {
	return &fleet.DeviceVMs{Status: fleet.DeviceVMsStatusUnknown, Why: fleet.String(clip(why))}
}

// view joins UTM's list with the records. With UTM closed the list is the
// records, each stopped.
func (k *Keeper) view(s *fleet.DeviceVMs, list []VM, recs []Record) vmView {
	if s.Status != fleet.DeviceVMsStatusOk {
		return vmView{Section: s, List: list}
	}
	byName := map[string]Record{}
	for _, r := range recs {
		byName[strings.ToLower(r.Name)] = r
	}
	add := func(name, state string) {
		if len(s.List) >= maxVMs {
			return
		}
		r := byName[strings.ToLower(name)]
		vm := &fleet.DeviceVM{Name: clip(name), State: state}
		system := r.OS
		if system == "" && r.Name != "" {
			system = "windows" // a record with no os is Windows: every one made before the field
		}
		if system != "" {
			vm.Os = fleet.DeviceVMOs(system).Ptr()
		}
		if owner := PublicOwner(r.Owner); owner != "" {
			vm.Owner = fleet.String(owner)
		}
		if r.KeepRunning {
			vm.KeepRunning = fleet.Bool(true)
		}
		// How many times this keeper has started it since it began: a VM
		// that keeps stopping shows here, though each start is quick enough
		// that no report sees it stopped.
		if st := k.vms[strings.ToLower(name)]; st != nil && st.starts > 0 {
			vm.KeeperStarts = fleet.Int(st.starts)
		}
		s.List = append(s.List, vm)
	}
	if utmUp(s) {
		for _, v := range list {
			add(v.Name, v.Status)
		}
	} else {
		for _, r := range recs {
			add(r.Name, "stopped")
		}
	}
	return vmView{Section: s, List: list}
}

// PublicOwner is a VM's owner as a report may carry it: a person's login
// and machine name (user@host, as the tool's default owner is
// user@host:repo) are removed, leaving the repository or agent.
func PublicOwner(owner string) string {
	o := strings.Trim(personAt.ReplaceAllString(owner, ""), ":/ ")
	return clip(o)
}

// personAt is "name@host:" or "name@host".
var personAt = regexp.MustCompile(`[^\s/@:]+@[^\s/:]+:?`)

func clip(s string) string {
	if len(s) > 200 {
		return s[:197] + "..."
	}
	return s
}
