// Package keeper is the loop `irgo-winvm keeper` runs on a Mac, under
// pitchfork: each pass it keeps the Mac awake while a VM runs, starts again a
// VM marked keep-running that has stopped, and reports the Mac and its VMs to
// fleet-api (docs/concepts/architecture.md, "The keeper").
//
// It reaches UTM only through the UTM interface the CLI supplies, which has
// no way to quit UTM or to stop, delete or restart a VM: the keeper runs
// unattended beside VMs other people own, and the one thing it may do to a VM
// is start one that is marked and stopped. So it builds and is tested on
// every OS, against fakes.
package keeper

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	fleet "github.com/joeblew999/fleet-api/sdk/go"

	"github.com/joeblew999/irgo-windows-vm/internal/device"
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

// Config is a keeper's parts. Zero durations take the defaults.
type Config struct {
	UTM   UTM
	Awake Awake
	// Read reads the machine (device.Read with the data directory).
	Read func() device.Snapshot
	// Reports sends reports to fleet-api; nil is reporting off.
	Reports *Reporter
	// ID is this machine's id; Version the tool's.
	ID, Version string

	Every       time.Duration // between passes: 15 s
	ReportEvery time.Duration // between reports when nothing changes, the report's next_s: 5 min
	Now         func() time.Time
	Say         func(string, ...any)
}

// Defaults.
const (
	DefaultEvery       = 15 * time.Second
	DefaultReportEvery = 5 * time.Minute
	// changeGap is the least time between two reports sent for a change, so
	// a flapping state sends one report a half-minute, not one a pass.
	changeGap = 30 * time.Second
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

	sentAny  bool
	lastSent time.Time
	lastFP   string
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
	if cfg.ReportEvery == 0 {
		cfg.ReportEvery = DefaultReportEvery
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
// again and sends a stop report.
func (k *Keeper) Run(ctx context.Context) {
	t := time.NewTicker(k.cfg.Every)
	defer t.Stop()
	for {
		// A pass runs to its end: a stop signal mid-send would cancel the
		// report and leave it to the next run.
		k.Pass(context.WithoutCancel(ctx))
		select {
		case <-ctx.Done():
			stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			k.Stop(stopCtx)
			cancel()
			return
		case <-t.C:
		}
	}
}

// Pass is one turn of the loop: the VMs, the hold, the report. It never
// fails; what goes wrong is said, and tried again on a later pass.
func (k *Keeper) Pass(ctx context.Context) {
	now := k.cfg.Now()
	snap := k.cfg.Read()
	vms := k.keep(now, true)
	k.hold(snap.Power, vms)
	k.report(ctx, now, snap, vms)
}

// Once reads the Mac and its VMs and reports them once, with reason once,
// changing nothing: it opens no UTM, starts no VM and holds nothing
// (`keeper -once`).
func (k *Keeper) Once(ctx context.Context) *fleet.DeviceReport {
	now := k.cfg.Now()
	snap := k.cfg.Read()
	vms := k.keep(now, false)
	r := k.build(now, snap, vms, fleet.DeviceReportReasonOnce, 0)
	r.Keeper = &fleet.DeviceKeeper{Status: fleet.DeviceKeeperStatusUnknown, Why: fleet.String("read by keeper -once, which holds nothing; the running keeper reports its own")}
	k.cfg.Reports.Send(ctx, r, k.cfg.Say)
	return r
}

// Stop lets the Mac sleep again and sends a stop report, which says the
// keeper is going quiet on purpose.
func (k *Keeper) Stop(ctx context.Context) {
	if k.cfg.Awake.Held() {
		if err := k.cfg.Awake.Release(); err != nil {
			k.cfg.Say("letting the Mac sleep again: %v", err)
		} else {
			k.cfg.Say("the Mac may sleep again: the keeper is stopping")
		}
	}
	now := k.cfg.Now()
	r := k.build(now, k.cfg.Read(), k.lastVMs(), fleet.DeviceReportReasonStop, 0)
	r.Keeper = &fleet.DeviceKeeper{Status: fleet.DeviceKeeperStatusOk, Running: fleet.Bool(false), Idle: fleet.Bool(false), Lid: fleet.Bool(false)}
	k.cfg.Reports.Last(ctx, r, k.cfg.Say)
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
// report.
type vmView struct {
	Section VMSection
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
		return k.view(VMSection{Status: "none"}, nil, recs)
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
			return k.view(VMSection{Status: "ok", AppRunning: false}, nil, recs)
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
	return k.view(VMSection{Status: "ok", AppRunning: true}, list, recs)
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
	unknown := vms.Section.Status == "unknown"
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

// lastVMs is the VM section without asking UTM anything: for the stop
// report, sent as the keeper goes.
func (k *Keeper) lastVMs() vmView {
	return vmView{Section: unknownVMs("not read: the keeper is stopping")}
}

// VMSection is the report's vms: the VMs on this Mac and whose they are.
// fleet-api's schema 1 has no section for VMs; the report carries it as a
// field the Worker stores as posted (every object allows unknown fields).
type VMSection struct {
	Status string `json:"status"` // ok, none (no UTM), unknown (why)
	Why    string `json:"why,omitempty"`
	// AppRunning is whether UTM is running. Every VM is stopped when it is
	// not, and the keeper does not open it to ask.
	AppRunning bool     `json:"app_running"`
	List       []VMInfo `json:"list,omitempty"`
}

// VMInfo is one VM.
type VMInfo struct {
	Name        string `json:"name"`
	State       string `json:"state"`        // utmctl's: started, stopped, paused...
	OS          string `json:"os,omitempty"` // windows, linux; absent with no record
	Owner       string `json:"owner,omitempty"`
	KeepRunning bool   `json:"keep_running,omitempty"`
	// KeeperStarts is how many times this keeper has started it since it
	// began: a VM that keeps stopping shows here, though each start is
	// quick enough that no report sees it stopped.
	KeeperStarts int `json:"keeper_starts,omitempty"`
}

// maxVMs bounds the list, as fleet-api bounds every list.
const maxVMs = 32

func unknownVMs(why string) VMSection {
	return VMSection{Status: "unknown", Why: clip(why)}
}

// view joins UTM's list with the records. With UTM closed the list is the
// records, each stopped.
func (k *Keeper) view(s VMSection, list []VM, recs []Record) vmView {
	if s.Status != "ok" {
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
		os := r.OS
		if os == "" && r.Name != "" {
			os = "windows" // a record with no os is Windows: every one made before the field
		}
		starts := 0
		if st := k.vms[strings.ToLower(name)]; st != nil {
			starts = st.starts
		}
		s.List = append(s.List, VMInfo{Name: clip(name), State: state, OS: os, Owner: PublicOwner(r.Owner), KeepRunning: r.KeepRunning, KeeperStarts: starts})
	}
	if s.AppRunning {
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

// report sends a report when one is due: the first (start), one for a change
// at most every changeGap, and one every ReportEvery (interval). Between
// them it resends what is spooled.
func (k *Keeper) report(ctx context.Context, now time.Time, snap device.Snapshot, vms vmView) {
	if k.cfg.Reports == nil {
		return
	}
	r := k.build(now, snap, vms, "", int64(k.cfg.ReportEvery/time.Second))
	fp := fingerprint(r, vms.Section)
	var reason fleet.DeviceReportReason
	switch {
	case !k.sentAny:
		reason = fleet.DeviceReportReasonStart
	case fp != k.lastFP && now.Sub(k.lastSent) >= changeGap:
		reason = fleet.DeviceReportReasonChange
	case now.Sub(k.lastSent) >= k.cfg.ReportEvery:
		reason = fleet.DeviceReportReasonInterval
	default:
		k.cfg.Reports.Flush(ctx, k.cfg.Say)
		return
	}
	r.Reason = reason
	k.sentAny, k.lastSent, k.lastFP = true, now, fp
	k.cfg.Reports.Send(ctx, r, k.cfg.Say)
}

// build is the report for this moment.
func (k *Keeper) build(now time.Time, snap device.Snapshot, vms vmView, reason fleet.DeviceReportReason, nextS int64) *fleet.DeviceReport {
	held := k.cfg.Awake.Held()
	r := &fleet.DeviceReport{
		Schema: 1, ID: k.cfg.ID, Ts: now.UnixMilli(), Reason: reason, NextS: nextS,
		Tool:    &fleet.DeviceTool{Name: fleet.String("irgo-winvm"), Version: k.cfg.Version, Command: "keeper"},
		Host:    snap.Host,
		CPU:     snap.CPU,
		Memory:  snap.Memory,
		Disks:   snap.Disks,
		Power:   snap.Power,
		Battery: snap.Battery,
		Lid:     snap.Lid,
		Sleep:   snap.Sleep,
		// lid: false, always. Keeping the Mac up with the lid closed needs
		// root (pmset disablesleep) and has not been measured here.
		Keeper:          &fleet.DeviceKeeper{Status: fleet.DeviceKeeperStatusOk, Running: fleet.Bool(true), Idle: fleet.Bool(held), Lid: fleet.Bool(false)},
		ExtraProperties: map[string]any{"vms": vms.Section},
	}
	return r
}

// fingerprint is what, changed, is worth a report of its own: the power
// source, the battery's state, the lid, what closing it does, the keeper's
// hold, and each VM's state. Not memory, load or charge, which change every
// pass.
func fingerprint(r *fleet.DeviceReport, vms VMSection) string {
	var b strings.Builder
	if r.Power != nil && r.Power.Source != nil {
		fmt.Fprintf(&b, "power=%s;", *r.Power.Source)
	}
	if r.Battery != nil && r.Battery.State != nil {
		fmt.Fprintf(&b, "battery=%s;", *r.Battery.State)
	}
	if r.Lid != nil && r.Lid.Closed != nil {
		fmt.Fprintf(&b, "lid=%v;", *r.Lid.Closed)
	}
	if r.Sleep != nil && r.Sleep.LidAction != nil {
		fmt.Fprintf(&b, "lid_action=%s;", *r.Sleep.LidAction)
	}
	if r.Keeper != nil && r.Keeper.Idle != nil {
		fmt.Fprintf(&b, "held=%v;", *r.Keeper.Idle)
	}
	fmt.Fprintf(&b, "vms=%s/%v;", vms.Status, vms.AppRunning)
	for _, v := range vms.List {
		fmt.Fprintf(&b, "%s=%s/%v/%d;", v.Name, v.State, v.KeepRunning, v.KeeperStarts)
	}
	return b.String()
}

func clip(s string) string {
	if len(s) > 200 {
		return s[:197] + "..."
	}
	return s
}

// ErrRefused is a report fleet-api refused for what it says (400, 413,
// 422): sending it again cannot work, so it is not kept.
var ErrRefused = errors.New("fleet-api refused the report")
