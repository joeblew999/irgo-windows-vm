package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const (
	writeTok = "ledger-write"
	readTok  = "ledger-read"
)

// ledgerEnv is an Env with a fresh SQLite ledger holding the D1 migration.
func ledgerEnv(t *testing.T, now *time.Time) Env {
	t.Helper()
	db, err := openMemLedger()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	vars := map[string]string{varLedgerToken: writeTok, varLedgerReadToken: readTok}
	return Env{
		Var:    func(n string) string { return vars[n] },
		Ledger: func() (*sql.DB, error) { return db, nil },
		Now:    func() time.Time { return *now },
	}
}

func ms(t time.Time) int64 { return t.UnixMilli() }

func ev(id, typ, op, vm string, at time.Time) Event {
	return Event{ID: id, Type: typ, Op: op, VM: vm, TS: ms(at), Machine: "m0000001", Host: "mac-a",
		Owner: "alice", Client: "claude-code", Repo: "joeblew999/x", Command: "app-create", Version: "dev"}
}

func postEvents(h http.Handler, token string, evs ...Event) *httptest.ResponseRecorder {
	b, _ := json.Marshal(map[string]any{"events": evs})
	return do(h, http.MethodPost, "/api/ledger/events", token, bytes.NewBuffer(b), "application/json")
}

// Negative control (by hand, 1 Oct 2026): making ledger() skip authorized for
// the read paths fails the "want 401" cases below; restored.
func TestLedgerRefusals(t *testing.T) {
	now := testNow
	h := Handler(ledgerEnv(t, &now))
	for _, c := range []struct {
		method, path, token string
		want                int
	}{
		{"POST", "/api/ledger/events", "", 401},
		{"POST", "/api/ledger/events", readTok, 401}, // the read token does not write
		{"GET", "/api/ledger/events", "", 401},
		{"GET", "/api/ledger/events", writeTok, 401}, // the write token does not read
		{"GET", "/api/ledger/vms", "", 401},
		{"GET", "/api/ledger/vms", writeTok, 401},
		{"GET", "/api/ledger/", "", 401},
		{"GET", "/api/ledger", "", 401},
		{"GET", "/api/ledger/nothing", "", 401}, // no hint which paths exist
		{"GET", "/api/ledger/nothing", readTok, 404},
		{"GET", "/api/ledger", readTok, 302},
	} {
		w := do(h, c.method, c.path, c.token, nil, "")
		if w.Code != c.want {
			t.Errorf("%s %s token=%q: %d %s, want %d", c.method, c.path, c.token, w.Code, w.Body, c.want)
		}
	}
	// The page asks a browser for Basic, and takes the read token as its password.
	w := do(h, "GET", "/api/ledger/", "", nil, "")
	if !strings.HasPrefix(w.Header().Get("WWW-Authenticate"), "Basic ") {
		t.Errorf("page refusal asks for %q, want Basic", w.Header().Get("WWW-Authenticate"))
	}
	for pw, want := range map[string]int{readTok: 200, writeTok: 401, "": 401} {
		r := httptest.NewRequest("GET", "/api/ledger/", nil)
		r.SetBasicAuth("anyone", pw)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		if rec.Code != want {
			t.Errorf("page with Basic password %q: %d, want %d", pw, rec.Code, want)
		}
	}
}

func TestLedgerUnconfiguredRefuses(t *testing.T) {
	now := testNow
	env := ledgerEnv(t, &now)
	env.Var = func(string) string { return "" }
	for _, p := range []string{"/api/ledger/events", "/api/ledger/vms", "/api/ledger/"} {
		if w := do(Handler(env), "GET", p, "anything", nil, ""); w.Code != 503 {
			t.Errorf("GET %s with no secrets set: %d, want 503", p, w.Code)
		}
	}
}

func TestLedgerIngestIdempotentAndValidated(t *testing.T) {
	now := testNow
	h := Handler(ledgerEnv(t, &now))
	a := ev("evt-00000001", "start", "op-0000001", "win11", now.Add(-time.Minute))
	exit := int64(3)
	b := ev("evt-00000002", "end", "op-0000001", "win11", now)
	b.Exit = &exit
	bad := ev("short", "start", "", "", now)
	weird := ev("evt-00000003", "explode", "", "", now)

	w := postEvents(h, writeTok, a, b, bad, weird)
	var got struct {
		Accepted, Duplicates int
		Rejected             []rejected
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &got) != nil || got.Accepted != 2 || len(got.Rejected) != 2 {
		t.Fatalf("first post: %d %s", w.Code, w.Body)
	}
	// The same batch again stores nothing new: a retry after a timeout is harmless.
	w = postEvents(h, writeTok, a, b)
	if json.Unmarshal(w.Body.Bytes(), &got) != nil || got.Accepted != 0 || got.Duplicates != 2 {
		t.Fatalf("second post: %d %s", w.Code, w.Body)
	}

	w = do(h, "GET", "/api/ledger/events?vm=win11", readTok, nil, "")
	var list struct{ Events []Event }
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &list) != nil || len(list.Events) != 2 {
		t.Fatalf("history: %d %s", w.Code, w.Body)
	}
	if list.Events[0].ID != b.ID || list.Events[0].Exit == nil || *list.Events[0].Exit != 3 || list.Events[1].Exit != nil {
		t.Errorf("history not newest first with exit codes kept: %+v", list.Events)
	}
	if list.Events[0].Received != ms(now) {
		t.Errorf("received = %d, want the Worker's clock %d", list.Events[0].Received, ms(now))
	}
	for q, n := range map[string]int{"owner=alice": 2, "owner=bob": 0, "machine=m0000001&type=end": 1, "since=30s": 1, "vm=other": 0} {
		w = do(h, "GET", "/api/ledger/events?"+q, readTok, nil, "")
		if json.Unmarshal(w.Body.Bytes(), &list) != nil || len(list.Events) != n {
			t.Errorf("?%s: %d events, want %d (%s)", q, len(list.Events), n, w.Body)
		}
	}

	// Batch bounds and a body that is not the shape.
	many := make([]Event, maxBatch+1)
	for i := range many {
		many[i] = ev(fmt.Sprintf("evt-many-%04d", i), "start", "", "", now)
	}
	if w := postEvents(h, writeTok, many...); w.Code != 400 {
		t.Errorf("%d events: %d, want 400", len(many), w.Code)
	}
	if w := do(h, "POST", "/api/ledger/events", writeTok, bytes.NewBufferString("nope"), ""); w.Code != 400 {
		t.Errorf("garbage body: %d, want 400", w.Code)
	}
}

func TestLedgerView(t *testing.T) {
	now := testNow
	h := Handler(ledgerEnv(t, &now))
	hourAgo, fourAgo := now.Add(-time.Hour), now.Add(-4*time.Hour)
	expired := ms(now.Add(-time.Minute))
	future := ms(now.Add(time.Hour))

	running := ev("evt-run-0001", "start", "op-run-001", "busy", hourAgo)
	orphan := ev("evt-orp-0001", "start", "op-orp-001", "orphan", fourAgo) // never ended, older than 3h
	leaseOK := ev("evt-lse-0001", "lease-acquire", "op-lse-001", "leased", fourAgo)
	leaseOK.Expires = &future // old, but its lease has not run out
	leaseGone := ev("evt-lse-0002", "lease-acquire", "op-lse-002", "lapsed", hourAgo)
	leaseGone.Expires = &expired
	done1 := ev("evt-don-0001", "start", "op-don-001", "done", fourAgo)
	done2 := ev("evt-don-0002", "end", "op-don-001", "done", fourAgo.Add(time.Minute))
	created := ev("evt-cre-0001", "vm-create", "", "gone", fourAgo)
	deleted := ev("evt-del-0001", "vm-delete", "", "gone", hourAgo)
	doctor := ev("evt-doc-0001", "start", "op-doc-001", "", now) // no VM: machine-level
	doctor.Command = "doctor"
	if w := postEvents(h, writeTok, running, orphan, leaseOK, leaseGone, done1, done2, created, deleted, doctor); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}

	w := do(h, "GET", "/api/ledger/vms", readTok, nil, "")
	var v ledgerView
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &v) != nil {
		t.Fatal(w.Code, w.Body)
	}
	state := map[string]string{}
	for _, vm := range v.VMs {
		state[vm.VM] = vm.State
	}
	want := map[string]string{"busy": "in-use", "orphan": "stale", "leased": "in-use", "lapsed": "stale", "done": "idle", "gone": "deleted"}
	for k, s := range want {
		if state[k] != s {
			t.Errorf("VM %s: state %q, want %q (all: %v)", k, state[k], s, state)
		}
	}
	why := map[string]string{}
	for _, o := range v.Open {
		why[o.Op] = o.Why
	}
	if len(v.Open) != 5 || why["op-orp-001"] != "started, never ended" || why["op-lse-002"] != "lease expired, never released" || why["op-run-001"] != "" {
		t.Errorf("open: %+v", v.Open)
	}
	if len(v.Machines) != 1 || v.Machines[0].LastCommand != "doctor" || v.Machines[0].Host != "mac-a" {
		t.Errorf("machines: %+v", v.Machines)
	}

	// A tighter threshold makes the hour-old run stale too.
	w = do(h, "GET", "/api/ledger/vms?stale=30m", readTok, nil, "")
	if json.Unmarshal(w.Body.Bytes(), &v) != nil {
		t.Fatal(w.Body)
	}
	for _, vm := range v.VMs {
		if vm.VM == "busy" && vm.State != "stale" {
			t.Errorf("busy with stale=30m: %q, want stale", vm.State)
		}
	}

	// The page shows the same, and escapes what it shows.
	evil := ev("evt-evil-001", "end", "", "busy", now)
	evil.Detail = `<script>alert(1)</script>`
	postEvents(h, writeTok, evil)
	w = do(h, "GET", "/api/ledger/", readTok, nil, "")
	body := w.Body.String()
	if w.Code != 200 || !strings.Contains(body, "orphan") || !strings.Contains(body, "started, never ended") {
		t.Errorf("page: %d\n%s", w.Code, body)
	}
	if strings.Contains(body, "<script>alert") || !strings.Contains(body, "&lt;script&gt;") {
		t.Error("page did not escape an event's detail")
	}
}
