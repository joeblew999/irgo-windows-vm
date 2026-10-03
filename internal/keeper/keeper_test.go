package keeper

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	fleet "github.com/joeblew999/fleet-api/sdk/go"

	"github.com/joeblew999/irgo-windows-vm/internal/device"
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
func snapshot(src fleet.DevicePowerSource) device.Snapshot {
	return device.Snapshot{Power: &fleet.DevicePower{Status: fleet.DevicePowerStatusOk, Source: src.Ptr()}}
}

func newKeeper(u *fakeUTM, a *fakeAwake, c *clock, src fleet.DevicePowerSource) *Keeper {
	return New(Config{UTM: u, Awake: a, Now: c.now, Read: func() device.Snapshot { return snapshot(src) }, ID: "0123456789abcdef"})
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
	newKeeper(u, &fakeAwake{}, newClock(), fleet.DevicePowerSourceAc).Pass(context.Background())
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
	newKeeper(u, &fakeAwake{}, newClock(), fleet.DevicePowerSourceAc).Pass(context.Background())
	if !slices.Equal(u.calls, []string{"running"}) {
		t.Fatalf("with nothing marked the keeper asked %v; want only whether UTM runs", u.calls)
	}

	u = &fakeUTM{installed: true, up: false, stays: true, vms: []VM{stopped("a")}, recs: []Record{kept("a")}}
	newKeeper(u, &fakeAwake{}, newClock(), fleet.DevicePowerSourceAc).Pass(context.Background())
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
	newKeeper(u, &fakeAwake{}, newClock(), fleet.DevicePowerSourceAc).Once(context.Background())
	if len(u.starts()) > 0 {
		t.Fatalf("-once started %v", u.starts())
	}
	u = &fakeUTM{installed: true, up: false, recs: []Record{kept("a")}}
	newKeeper(u, &fakeAwake{}, newClock(), fleet.DevicePowerSourceAc).Once(context.Background())
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
		k.Pass(context.Background())
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
		k.Pass(context.Background())
		c.add(15 * time.Second)
		if u.vms[0].Running { // seen running for one pass, then it stops
			k.Pass(context.Background())
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
	newKeeper(u, &fakeAwake{}, newClock(), fleet.DevicePowerSourceAc).Pass(context.Background())
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
		newKeeper(u, a, newClock(), c.src).Pass(context.Background())
		if a.held != c.want {
			t.Errorf("%s: held %v, want %v", c.name, a.held, c.want)
		}
	}
	// Power that cannot be told holds nothing.
	u := &fakeUTM{installed: true, up: true, vms: []VM{started("a")}}
	a := &fakeAwake{}
	k := New(Config{UTM: u, Awake: a, Now: newClock().now, Read: func() device.Snapshot {
		return device.Snapshot{Power: &fleet.DevicePower{Status: fleet.DevicePowerStatusUnknown, Why: fleet.String("x")}}
	}})
	k.Pass(context.Background())
	if a.held {
		t.Error("held with the power source unknown")
	}
	// VMs that cannot be listed may be running: on AC that holds.
	u = &fakeUTM{installed: true, up: true, vms: []VM{started("a")}}
	a = &fakeAwake{}
	k = New(Config{UTM: listFails{u}, Awake: a, Now: newClock().now, Read: func() device.Snapshot { return snapshot(fleet.DevicePowerSourceAc) }})
	k.Pass(context.Background())
	if !a.held {
		t.Error("let the Mac sleep while UTM's VMs could not be listed")
	}
	// And a hold is let go when the VM stops, and by Stop.
	u = &fakeUTM{installed: true, up: true, vms: []VM{started("a")}}
	a = &fakeAwake{}
	k = newKeeper(u, a, newClock(), fleet.DevicePowerSourceAc)
	k.Pass(context.Background())
	u.vms = []VM{stopped("a")}
	k.Pass(context.Background())
	if a.held {
		t.Error("still held after the only VM stopped")
	}
	u.vms = []VM{started("a")}
	k.Pass(context.Background())
	k.Stop(context.Background())
	if a.held {
		t.Error("still held after Stop")
	}
}

// fleetFake is fleet-api's report route: it records each body, or answers
// with a status.
type fleetFake struct {
	mu     sync.Mutex
	status int
	bodies []map[string]any
}

func (f *fleetFake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	b, _ := io.ReadAll(r.Body)
	if f.status != 0 {
		w.Header().Set("content-type", "application/problem+json")
		w.WriteHeader(f.status)
		_, _ = w.Write([]byte(`{"title":"no"}`))
		return
	}
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	if !strings.HasSuffix(r.URL.Path, "/"+m["id"].(string)+"/reports") || r.Header.Get("Authorization") != "Bearer tok" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	f.bodies = append(f.bodies, m)
	w.Header().Set("content-type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_, _ = w.Write([]byte(`{"id":"0123456789abcdef","received":1,"duplicate":false,"conditions":[]}`))
}

func (f *fleetFake) reasons() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, b := range f.bodies {
		out = append(out, b["reason"].(string))
	}
	return out
}

// TestReports: start first; nothing between; change when a VM changes, no
// sooner than 30 s after the last; interval after five quiet minutes; stop
// at the end. Each carries next_s and the VMs.
//
// Negative control, run by hand: drop the VMs from fingerprint and the
// change report is not sent.
func TestReports(t *testing.T) {
	ff := &fleetFake{}
	srv := httptest.NewServer(ff)
	defer srv.Close()
	u := &fakeUTM{installed: true, up: true, vms: []VM{started("a")}, recs: []Record{{Name: "a", Owner: "apple@mac:repo"}}}
	c := newClock()
	// On battery, so nothing is held and only the VM's state changes.
	k := New(Config{UTM: u, Awake: &fakeAwake{}, Now: c.now, ID: "0123456789abcdef",
		Read:    func() device.Snapshot { return snapshot(fleet.DevicePowerSourceBattery) },
		Reports: &Reporter{Post: NewPost(srv.URL, "tok"), Dir: t.TempDir(), Now: c.now}})
	ctx := context.Background()
	k.Pass(ctx) // start
	c.add(15 * time.Second)
	k.Pass(ctx) // nothing
	u.vms = []VM{stopped("a")}
	k.Pass(ctx) // a change, but 15 s after the last: held back
	c.add(15 * time.Second)
	k.Pass(ctx) // change
	for range 21 {
		c.add(15 * time.Second)
		k.Pass(ctx)
	} // 5 min 15 s later: one interval
	// A marked VM that stops and is started again within one pass is never
	// seen stopped; its keeper_starts is the change.
	u.recs = append(u.recs, kept("b"))
	u.vms = append(u.vms, stopped("b"))
	u.stays = true
	c.add(time.Minute)
	k.Pass(ctx)
	k.Stop(ctx)
	want := []string{"start", "change", "interval", "change", "stop"}
	if got := ff.reasons(); !slices.Equal(got, want) {
		t.Fatalf("reasons %v, want %v", got, want)
	}
	first := ff.bodies[0]
	if first["next_s"].(float64) != 300 {
		t.Errorf("next_s %v, want 300", first["next_s"])
	}
	vms, _ := first["vms"].(map[string]any)
	list, _ := vms["list"].([]any)
	if len(list) != 1 || list[0].(map[string]any)["owner"] != "repo" {
		t.Errorf("vms %v: want a, owner repo (the person's login and machine removed)", vms)
	}
	if ff.bodies[4]["next_s"].(float64) != 0 {
		t.Errorf("stop report promises next_s %v", ff.bodies[4]["next_s"])
	}
	restarted := ff.bodies[3]["vms"].(map[string]any)["list"].([]any)[1].(map[string]any)
	if restarted["state"] != "started" || restarted["keeper_starts"] != float64(1) {
		t.Errorf("b after the keeper started it: %v, want started, keeper_starts 1", restarted)
	}
}

// TestUnsentReportsAreSpooledAndResentInOrder: with fleet-api failing, the
// loop goes on and reports stay in the spool; once it answers they go,
// oldest first, VMs and all. A report it refuses (422) is dropped.
//
// Negative control, run by hand: remove the spooled file when a send fails
// (flush's default case) and the spool is empty after the outage.
func TestUnsentReportsAreSpooledAndResentInOrder(t *testing.T) {
	ff := &fleetFake{status: http.StatusServiceUnavailable}
	srv := httptest.NewServer(ff)
	defer srv.Close()
	c := newClock()
	dir := t.TempDir()
	rep := &Reporter{Post: NewPost(srv.URL, "tok"), Dir: dir, Now: c.now}
	u := &fakeUTM{installed: true, up: true, vms: []VM{started("a")}}
	k := New(Config{UTM: u, Awake: &fakeAwake{}, Now: c.now, ID: "0123456789abcdef", ReportEvery: time.Minute,
		Read: func() device.Snapshot { return snapshot(fleet.DevicePowerSourceAc) }, Reports: rep})
	ctx := context.Background()
	for range 12 { // 3 minutes: a start and two intervals, none delivered
		k.Pass(ctx)
		c.add(15 * time.Second)
	}
	if n, _ := rep.spooled(); len(n) != 3 {
		t.Fatalf("spool holds %v, want 3 reports", n)
	}
	ff.mu.Lock()
	ff.status = 0
	ff.mu.Unlock()
	c.add(time.Minute)
	k.Pass(ctx) // the backoff has passed: an interval report, and the three before it
	got := ff.reasons()
	if !slices.Equal(got, []string{"start", "interval", "interval", "interval"}) {
		t.Fatalf("sent %v after the outage, want start then the intervals, in order", got)
	}
	for i := 1; i < len(ff.bodies); i++ {
		if ff.bodies[i]["ts"].(float64) <= ff.bodies[i-1]["ts"].(float64) {
			t.Fatalf("sent out of order: %v then %v", ff.bodies[i-1]["ts"], ff.bodies[i]["ts"])
		}
		if ff.bodies[i]["vms"] == nil {
			t.Fatal("a resent report lost its vms")
		}
	}
	if n, _ := rep.spooled(); len(n) != 0 {
		t.Fatalf("spool still holds %v", n)
	}

	ff.mu.Lock()
	ff.status = http.StatusUnprocessableEntity
	ff.mu.Unlock()
	c.add(2 * time.Minute)
	k.Pass(ctx)
	if n, _ := rep.spooled(); len(n) != 0 {
		t.Fatalf("a refused report was kept: %v", n)
	}
	if _, err := os.Stat(filepath.Join(dir)); err != nil {
		t.Fatal(err)
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
