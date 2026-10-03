package keeper

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	fleet "github.com/joeblew999/fleet-api/sdk/go"
)

// fakeUTM is UTM as a table. Every call is logged, so a test can say what
// was never asked.
type fakeUTM struct {
	installed bool
	up        bool
	vms       []VM
	recs      []Record
	startErr  error
	busy      map[string]bool
	// stays: a started VM stays started; false makes every start a VM
	// that UTM lists stopped again at the next pass (a crash on boot).
	stays bool
	calls []string
}

func (f *fakeUTM) log(s string) { f.calls = append(f.calls, s) }

func (f *fakeUTM) Installed() bool { return f.installed }
func (f *fakeUTM) Running() (bool, error) {
	f.log("running")
	return f.up, nil
}
func (f *fakeUTM) Open() error {
	f.log("open")
	f.up = true
	return nil
}
func (f *fakeUTM) List() ([]VM, error) {
	f.log("list")
	if !f.up {
		return nil, errors.New("listed while UTM is closed")
	}
	return slices.Clone(f.vms), nil
}
func (f *fakeUTM) Start(name string) error {
	f.log("start " + name)
	if f.startErr != nil {
		return f.startErr
	}
	for i := range f.vms {
		if f.vms[i].Name == name && f.stays {
			f.vms[i].Status, f.vms[i].Running = "started", true
		}
	}
	return nil
}
func (f *fakeUTM) Lock(name string) (func(), error) {
	if f.busy[name] {
		return nil, errors.New("busy")
	}
	return func() {}, nil
}
func (f *fakeUTM) Records() ([]Record, error) { return f.recs, nil }

func (f *fakeUTM) starts() []string {
	var s []string
	for _, c := range f.calls {
		if strings.HasPrefix(c, "start ") {
			s = append(s, strings.TrimPrefix(c, "start "))
		}
	}
	return s
}

// listFails is a UTM whose list fails, as utmctl refused by macOS does.
type listFails struct{ *fakeUTM }

func (listFails) List() ([]VM, error) { return nil, errors.New("OSStatus error -1743") }

type fakeAwake struct {
	held  bool
	holds int
}

func (a *fakeAwake) Hold() error    { a.held = true; a.holds++; return nil }
func (a *fakeAwake) Release() error { a.held = false; return nil }
func (a *fakeAwake) Held() bool     { return a.held }
func (a *fakeAwake) Holder() string { return "fake" }

// clock is a time a test moves by hand.
type clock struct{ t time.Time }

func (c *clock) now() time.Time      { return c.t }
func (c *clock) add(d time.Duration) { c.t = c.t.Add(d) }
func newClock() *clock               { return &clock{t: time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)} }
func stopped(name string) VM         { return VM{Name: name, Status: "stopped"} }
func started(name string) VM         { return VM{Name: name, Status: "started", Running: true} }
func kept(name string) Record        { return Record{Name: name, Owner: "claude-rig", KeepRunning: true} }
func power(src fleet.DevicePowerSource) func() *fleet.DevicePower {
	return func() *fleet.DevicePower {
		return &fleet.DevicePower{Status: fleet.DevicePowerStatusOk, Source: src.Ptr()}
	}
}

func newKeeper(u *fakeUTM, a *fakeAwake, c *clock, src fleet.DevicePowerSource) *Keeper {
	return New(Config{UTM: u, Awake: a, Now: c.now, Power: power(src)})
}

// TestStartsOnlyMarkedStoppedVMs: of three VMs, only the one marked and
// stopped is started; a stopped VM nobody marked, and a marked one already
// running, are left alone.
//
// Negative control, run by hand: start every stopped VM in startStopped
// (iterate list instead of keep) and "other" is started too.
func TestStartsOnlyMarkedStoppedVMs(t *testing.T) {
	u := &fakeUTM{installed: true, up: true, stays: true,
		vms:  []VM{stopped("keep-me"), stopped("other"), started("kept-up")},
		recs: []Record{kept("keep-me"), {Name: "other", Owner: "x"}, kept("kept-up")}}
	newKeeper(u, &fakeAwake{}, newClock(), fleet.DevicePowerSourceAc).Pass()
	if got := u.starts(); !slices.Equal(got, []string{"keep-me"}) {
		t.Fatalf("started %v, want only keep-me", got)
	}
}

// TestUTMClosedIsOpenedOnlyForAMarkedVM: with UTM closed and nothing marked,
// the keeper asks macOS and nothing else, so UTM stays closed; with a VM
// marked it opens UTM, by the two-second rule, before any request.
//
// Negative control, run by hand: drop the len(keep) == 0 case in keep and
// the first half opens UTM.
func TestUTMClosedIsOpenedOnlyForAMarkedVM(t *testing.T) {
	u := &fakeUTM{installed: true, up: false, vms: []VM{stopped("a")}, recs: []Record{{Name: "a"}}}
	newKeeper(u, &fakeAwake{}, newClock(), fleet.DevicePowerSourceAc).Pass()
	if !slices.Equal(u.calls, []string{"running"}) {
		t.Fatalf("with nothing marked the keeper asked %v; want only whether UTM runs", u.calls)
	}

	u = &fakeUTM{installed: true, up: false, stays: true, vms: []VM{stopped("a")}, recs: []Record{kept("a")}}
	newKeeper(u, &fakeAwake{}, newClock(), fleet.DevicePowerSourceAc).Pass()
	if len(u.calls) < 3 || u.calls[0] != "running" || u.calls[1] != "open" || u.calls[2] != "list" {
		t.Fatalf("calls %v: want running, open, list before anything else", u.calls)
	}
	if !slices.Equal(u.starts(), []string{"a"}) {
		t.Fatalf("started %v, want a", u.starts())
	}
}

// TestOnceChangesNothing: -once lists a running UTM and starts nothing, and
// never opens a closed one even for a marked VM.
//
// Negative control, run by hand: call keep(now, true) in Once and both
// halves fail.
func TestOnceChangesNothing(t *testing.T) {
	u := &fakeUTM{installed: true, up: true, vms: []VM{stopped("a")}, recs: []Record{kept("a")}}
	newKeeper(u, &fakeAwake{}, newClock(), fleet.DevicePowerSourceAc).Once()
	if len(u.starts()) > 0 {
		t.Fatalf("-once started %v", u.starts())
	}
	u = &fakeUTM{installed: true, up: false, recs: []Record{kept("a")}}
	newKeeper(u, &fakeAwake{}, newClock(), fleet.DevicePowerSourceAc).Once()
	if slices.Contains(u.calls, "open") {
		t.Fatalf("-once opened UTM: %v", u.calls)
	}
}

// TestBackoff: a start that fails is tried again at once, then after 30 s,
// 60 s, 120 s: never every pass.
//
// Negative control, run by hand: make backoff return 0 and a try lands on
// every 15 s pass.
func TestBackoff(t *testing.T) {
	u := &fakeUTM{installed: true, up: true, startErr: errors.New("AppleEvent timed out (-1712)"),
		vms: []VM{stopped("a")}, recs: []Record{kept("a")}}
	c := newClock()
	k := newKeeper(u, &fakeAwake{}, c, fleet.DevicePowerSourceAc)
	var at []time.Duration
	begin := c.now()
	for range 30 { // 7.5 minutes of passes
		before := len(u.starts())
		k.Pass()
		if len(u.starts()) > before {
			at = append(at, c.now().Sub(begin))
		}
		c.add(15 * time.Second)
	}
	want := []time.Duration{0, 30 * time.Second, 90 * time.Second, 210 * time.Second, 450 * time.Second}
	if !slices.Equal(at[:min(len(at), len(want))], want[:min(len(at), len(want))]) || len(at) < 4 {
		t.Fatalf("tries at %v, want %v", at, want)
	}
	if backoff(100) != backoffMax {
		t.Fatalf("backoff(100) = %s, want the cap %s", backoff(100), backoffMax)
	}
}

// TestCrashOnBootBacksOff: a start UTM takes, of a VM that is stopped again
// at the next pass, counts as a failure, so it backs off like one.
//
// Negative control, run by hand: reset attempts whenever the VM is seen
// running (not after stableAfter) and the tries come every 30 s.
func TestCrashOnBootBacksOff(t *testing.T) {
	u := &fakeUTM{installed: true, up: true, stays: true, vms: []VM{stopped("a")}, recs: []Record{kept("a")}}
	c := newClock()
	k := newKeeper(u, &fakeAwake{}, c, fleet.DevicePowerSourceAc)
	for c.now().Before(time.Date(2026, 10, 3, 12, 5, 0, 0, time.UTC)) {
		k.Pass()
		c.add(15 * time.Second)
		if u.vms[0].Running { // seen running for one pass, then it stops
			k.Pass()
			c.add(15 * time.Second)
			u.vms[0] = stopped("a")
		}
	}
	if n := len(u.starts()); n > 4 {
		t.Fatalf("%d starts in 5 minutes of a VM that never stays up; want at most 4", n)
	}
}

// TestBusyVMIsNotStarted: a VM whose lock a command holds is not started.
//
// Negative control, run by hand: ignore Lock's error and it is started.
func TestBusyVMIsNotStarted(t *testing.T) {
	u := &fakeUTM{installed: true, up: true, vms: []VM{stopped("a")}, recs: []Record{kept("a")}, busy: map[string]bool{"a": true}}
	newKeeper(u, &fakeAwake{}, newClock(), fleet.DevicePowerSourceAc).Pass()
	if len(u.starts()) > 0 {
		t.Fatalf("started %v under a command's lock", u.starts())
	}
}

// TestHoldsAwakeOnlyWhileAVMRunsOnAC: the table of what is held.
//
// Negative control, run by hand: drop "&& ac" from want and the battery
// case holds; drop the unknown case in hold and the unlisted case lets go.
func TestHoldsAwakeOnlyWhileAVMRunsOnAC(t *testing.T) {
	cases := []struct {
		name string
		vms  []VM
		src  fleet.DevicePowerSource
		want bool
	}{
		{"a VM running on AC", []VM{started("a")}, fleet.DevicePowerSourceAc, true},
		{"a VM running on battery", []VM{started("a")}, fleet.DevicePowerSourceBattery, false},
		{"no VM running on AC", []VM{stopped("a")}, fleet.DevicePowerSourceAc, false},
		{"a paused VM on AC", []VM{{Name: "a", Status: "paused"}}, fleet.DevicePowerSourceAc, false},
	}
	for _, c := range cases {
		u := &fakeUTM{installed: true, up: true, vms: c.vms}
		a := &fakeAwake{}
		newKeeper(u, a, newClock(), c.src).Pass()
		if a.held != c.want {
			t.Errorf("%s: held %v, want %v", c.name, a.held, c.want)
		}
	}
	// Power that cannot be told holds nothing.
	u := &fakeUTM{installed: true, up: true, vms: []VM{started("a")}}
	a := &fakeAwake{}
	k := New(Config{UTM: u, Awake: a, Now: newClock().now, Power: func() *fleet.DevicePower {
		return &fleet.DevicePower{Status: fleet.DevicePowerStatusUnknown, Why: fleet.String("x")}
	}})
	k.Pass()
	if a.held {
		t.Error("held with the power source unknown")
	}
	// VMs that cannot be listed may be running: on AC that holds.
	u = &fakeUTM{installed: true, up: true, vms: []VM{started("a")}}
	a = &fakeAwake{}
	k = New(Config{UTM: listFails{u}, Awake: a, Now: newClock().now, Power: power(fleet.DevicePowerSourceAc)})
	k.Pass()
	if !a.held {
		t.Error("let the Mac sleep while UTM's VMs could not be listed")
	}
	// And a hold is let go when the VM stops, and by Stop.
	u = &fakeUTM{installed: true, up: true, vms: []VM{started("a")}}
	a = &fakeAwake{}
	k = newKeeper(u, a, newClock(), fleet.DevicePowerSourceAc)
	k.Pass()
	u.vms = []VM{stopped("a")}
	k.Pass()
	if a.held {
		t.Error("still held after the only VM stopped")
	}
	u.vms = []VM{started("a")}
	k.Pass()
	k.Stop()
	if a.held {
		t.Error("still held after Stop")
	}
}

// TestWritesTheVMs: every pass writes the VMs, as UTM and the records say,
// with when; a person's login never reaches it; a VM the keeper started
// again shows it; Stop writes that the keeper stopped; a write that fails is
// said once and the loop goes on.
//
// Negative control, run by hand: drop the write from Pass and the first
// check fails; return owner unchanged in view and the owner check fails.
func TestWritesTheVMs(t *testing.T) {
	var got []VMsFile
	var failing error
	write := func(f VMsFile) error {
		if failing != nil {
			return failing
		}
		got = append(got, f)
		return nil
	}
	var said []string
	u := &fakeUTM{installed: true, up: true, vms: []VM{started("a")}, recs: []Record{{Name: "a", Owner: "apple@mac:repo", OS: "linux"}}}
	c := newClock()
	k := New(Config{UTM: u, Awake: &fakeAwake{}, Now: c.now, Power: power(fleet.DevicePowerSourceBattery), Write: write,
		Say: func(f string, a ...any) { said = append(said, fmt.Sprintf(f, a...)) }})
	k.Pass()
	if len(got) != 1 || got[0].TS != c.now().UnixMilli() {
		t.Fatalf("after one pass wrote %v, want one file at %v", got, c.now().UnixMilli())
	}
	vms := got[0].VMs
	if vms.Status != fleet.DeviceVMsStatusOk || !utmUp(vms) || len(vms.List) != 1 {
		t.Fatalf("vms %+v, want ok, UTM up, one VM", vms)
	}
	a := vms.List[0]
	if a.Name != "a" || a.State != "started" || a.Owner == nil || *a.Owner != "repo" || a.Os == nil || *a.Os != fleet.DeviceVMOsLinux {
		t.Fatalf("a: %+v, want started, linux, owner repo (the person's login and machine removed)", a)
	}

	// A marked VM found stopped is started, and the next file says so.
	u.recs = append(u.recs, kept("b"))
	u.vms = append(u.vms, stopped("b"))
	u.stays = true
	c.add(15 * time.Second)
	k.Pass()
	b := got[len(got)-1].VMs.List[1]
	if b.State != "started" || b.KeeperStarts == nil || *b.KeeperStarts != 1 || b.KeepRunning == nil || !*b.KeepRunning {
		t.Fatalf("b after the keeper started it: %+v, want started, keep_running, keeper_starts 1", b)
	}

	// A write that fails is said once, and the loop goes on.
	failing = errors.New("disk full")
	for range 3 {
		c.add(15 * time.Second)
		k.Pass()
	}
	n := 0
	for _, s := range said {
		if strings.Contains(s, "writing the VMs down") {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("a failing write was said %d times, want once", n)
	}
	failing = nil

	k.Stop()
	last := got[len(got)-1].VMs
	if last.Status != fleet.DeviceVMsStatusUnknown || last.Why == nil || !strings.Contains(*last.Why, "stopped") || last.List != nil {
		t.Fatalf("after Stop: %+v, want unknown, the keeper stopped", last)
	}
}

// TestWriteVMsFile: the file is {ts, vms} in fleet-api's JSON (snake_case,
// absent fields left out), written whole, and rewritten in place.
//
// Negative control, run by hand: write to path directly instead of through
// the temporary file and no temporary file is left to check; marshal a
// struct with Go's field names and the key check fails.
func TestWriteVMsFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "claude-rig", "vms.json")
	f := VMsFile{TS: 1790842406355, VMs: okVMs(false)}
	f.VMs.List = []*fleet.DeviceVM{{Name: "a", State: "stopped", KeepRunning: fleet.Bool(true)}}
	if err := WriteVMs(path, f); err != nil {
		t.Fatal(err)
	}
	if err := WriteVMs(path, f); err != nil {
		t.Fatal("rewriting:", err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"ts":1790842406355,"vms":{"list":[{"keep_running":true,"name":"a","state":"stopped"}],"manager":"utm","manager_running":false,"status":"ok"}}`
	var gotJSON, wantJSON any
	_ = json.Unmarshal(b, &gotJSON)
	_ = json.Unmarshal([]byte(want), &wantJSON)
	gotB, _ := json.Marshal(gotJSON)
	wantB, _ := json.Marshal(wantJSON)
	if string(gotB) != string(wantB) {
		t.Fatalf("file:\n%s\nwant:\n%s", b, want)
	}
	left, _ := filepath.Glob(filepath.Join(filepath.Dir(path), ".*"))
	if len(left) != 0 {
		t.Fatalf("temporary files left: %v", left)
	}
}

// TestPublicOwner: a person's login and machine never reach a report.
//
// Negative control, run by hand: return owner unchanged and the first two
// cases fail.
func TestPublicOwner(t *testing.T) {
	for in, want := range map[string]string{
		"apple@apples-macbook-pro:irgo-windows-vm":        "irgo-windows-vm",
		"claude-code/apple@apples-macbook-pro:claude-rig": "claude-code/claude-rig",
		"claude-rig":          "claude-rig",
		"remote:agent-x/0123": "remote:agent-x/0123",
		"":                    "",
	} {
		if got := PublicOwner(in); got != want || strings.Contains(got, "@") {
			t.Errorf("PublicOwner(%q) = %q, want %q", in, got, want)
		}
	}
}
