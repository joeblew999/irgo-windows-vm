package main

// The ledger: a durable, cross-machine record of which agent used which VM, on
// which machine, doing what (docs/DEVELOPMENT.md, "The ledger"). The tool's
// local lock files stay the authority; this is the record that survives a
// machine and can be read from anywhere.
//
//	POST /api/ledger/events   a batch of events                       LEDGER_TOKEN
//	GET  /api/ledger/events   history, filtered, newest first        LEDGER_READ_TOKEN
//	GET  /api/ledger/vms      now: machines, VMs, open work, stale    LEDGER_READ_TOKEN
//	GET  /api/ledger/         the same, as a page                     LEDGER_READ_TOKEN
//
// Stored in D1 (migrations/0001_ledger.sql), reached as a database/sql DB: on
// Workers through workers-go's d1 driver, on the host through SQLite, so the
// tests run the real queries.

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	varLedgerToken     = "LEDGER_TOKEN"      // secret: who may post events
	varLedgerReadToken = "LEDGER_READ_TOKEN" // secret: who may read them
)

// Limits. A batch is at most maxBatch events because each is one statement,
// and a Worker on the Free plan may make 50 D1 queries per invocation.
const (
	maxBatch      = 25
	maxBodyBytes  = 256 << 10
	maxField      = 200
	maxDetail     = 500
	defaultLimit  = 200
	maxLimit      = 1000
	defaultWindow = 14 * 24 * time.Hour // how far back /vms looks
	defaultStale  = 3 * time.Hour       // open work older than this, with no expiry, is stale
	maxViewRows   = 5000
)

// The event types. An op pairs the ones that open work with the ones that
// close it: start with end, lease-acquire with lease-release; a reap closes
// the op it names too.
var eventTypes = map[string]bool{
	"start": true, "end": true,
	"lease-acquire": true, "lease-release": true,
	"vm-create": true, "vm-delete": true, "reap": true,
}

var opens = map[string]bool{"start": true, "lease-acquire": true}
var closes = map[string]bool{"end": true, "lease-release": true, "reap": true}

// Event is one row, as posted and as read back. The client's definition is
// internal/ledger.Event in the tool's module; the JSON names are the contract.
type Event struct {
	ID         string `json:"id"`
	TS         int64  `json:"ts"`
	Received   int64  `json:"received,omitempty"`
	Type       string `json:"type"`
	Op         string `json:"op,omitempty"`
	Machine    string `json:"machine"`
	Host       string `json:"host,omitempty"`
	Owner      string `json:"owner,omitempty"`
	Client     string `json:"client,omitempty"`
	Repo       string `json:"repo,omitempty"`
	VM         string `json:"vm,omitempty"`
	Command    string `json:"command,omitempty"`
	Exit       *int64 `json:"exit,omitempty"`
	DurationMS *int64 `json:"duration_ms,omitempty"`
	Expires    *int64 `json:"expires,omitempty"`
	Version    string `json:"version,omitempty"`
	Detail     string `json:"detail,omitempty"`
}

const eventColumns = "id, ts, received, type, op, machine, host, owner, client, repo, vm, command, exit, duration_ms, expires, version, detail"

func (e *Event) scan(rows *sql.Rows) error {
	var exit, dur, exp sql.NullInt64
	err := rows.Scan(&e.ID, &e.TS, &e.Received, &e.Type, &e.Op, &e.Machine, &e.Host, &e.Owner,
		&e.Client, &e.Repo, &e.VM, &e.Command, &exit, &dur, &exp, &e.Version, &e.Detail)
	e.Exit, e.DurationMS, e.Expires = nullable(exit), nullable(dur), nullable(exp)
	return err
}

func nullable(n sql.NullInt64) *int64 {
	if !n.Valid {
		return nil
	}
	return &n.Int64
}

// isID is an event or op id: 8 to 64 of [A-Za-z0-9_-].
func isID(s string) bool {
	return len(s) >= 8 && len(s) <= 64 && allBytes(s, func(c byte) bool {
		return 'A' <= c && c <= 'Z' || 'a' <= c && c <= 'z' || '0' <= c && c <= '9' || c == '_' || c == '-'
	})
}

// validate says what is wrong with e, or "" when it may be stored. The
// client redacts; the Worker only bounds what it keeps.
func (e *Event) validate(now time.Time) string {
	switch {
	case !isID(e.ID):
		return "id must be 8-64 of [A-Za-z0-9_-]"
	case !eventTypes[e.Type]:
		return fmt.Sprintf("unknown type %q", e.Type)
	case e.Op != "" && !isID(e.Op):
		return "op must be 8-64 of [A-Za-z0-9_-]"
	case e.Machine == "" || len(e.Machine) > 64:
		return "machine must be 1-64 bytes"
	case e.TS < time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC).UnixMilli() || e.TS > now.Add(24*time.Hour).UnixMilli():
		return "ts is not a plausible time in Unix milliseconds"
	case len(e.Detail) > maxDetail:
		return fmt.Sprintf("detail is over %d bytes", maxDetail)
	}
	for _, f := range []string{e.Host, e.Owner, e.Client, e.Repo, e.VM, e.Command, e.Version} {
		if len(f) > maxField {
			return fmt.Sprintf("a field is over %d bytes", maxField)
		}
	}
	return ""
}

func (env Env) ledger(w http.ResponseWriter, r *http.Request, rest string) {
	switch {
	case r.Method == http.MethodPost && rest == "events":
		if env.authorized(w, r, varLedgerToken) {
			env.ledgerPost(w, r)
		}
	case r.Method == http.MethodGet && rest == "events":
		if env.authorized(w, r, varLedgerReadToken) {
			env.ledgerEvents(w, r)
		}
	case r.Method == http.MethodGet && rest == "vms":
		if env.authorized(w, r, varLedgerReadToken) {
			env.ledgerVMs(w, r)
		}
	case r.Method == http.MethodGet && rest == "":
		if env.authorizedPage(w, r, varLedgerReadToken) {
			env.ledgerPage(w, r)
		}
	default:
		// The token is checked first, so a caller without one learns nothing
		// about which paths exist.
		if env.authorized(w, r, varLedgerReadToken) {
			fail(w, http.StatusNotFound, "no such endpoint: %s %s", r.Method, r.URL.Path)
		}
	}
}

type rejected struct {
	ID    string `json:"id"`
	Error string `json:"error"`
}

func (env Env) ledgerPost(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Events []Event `json:"events"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	if err := dec.Decode(&in); err != nil {
		fail(w, http.StatusBadRequest, "the body is not {\"events\": [...]}: %v", err)
		return
	}
	if len(in.Events) == 0 || len(in.Events) > maxBatch {
		fail(w, http.StatusBadRequest, "send 1 to %d events, not %d", maxBatch, len(in.Events))
		return
	}
	db, err := env.Ledger()
	if err != nil {
		fail(w, http.StatusInternalServerError, "the LEDGER database: %v", err)
		return
	}
	now := env.Now()
	accepted, dup := 0, 0
	rej := []rejected{}
	for i := range in.Events {
		e := &in.Events[i]
		if why := e.validate(now); why != "" {
			rej = append(rej, rejected{e.ID, why})
			continue
		}
		res, err := db.Exec("INSERT OR IGNORE INTO events ("+eventColumns+") VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
			e.ID, e.TS, now.UnixMilli(), e.Type, e.Op, e.Machine, e.Host, e.Owner, e.Client, e.Repo,
			e.VM, e.Command, nullArg(e.Exit), nullArg(e.DurationMS), nullArg(e.Expires), e.Version, e.Detail)
		if err != nil {
			// Not the event's fault: answer 502 so the client keeps the batch
			// and sends it again. What was stored stays stored; the ids make
			// the retry harmless.
			fail(w, http.StatusBadGateway, "storing %s: %v", e.ID, err)
			return
		}
		if n, _ := res.RowsAffected(); n == 0 {
			dup++
		} else {
			accepted++
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"accepted": accepted, "duplicates": dup, "rejected": rej})
}

// nullArg is a nullable column's argument. A nil *int64 must reach the driver
// as nil, not as a typed nil pointer.
func nullArg(p *int64) any {
	if p == nil {
		return nil
	}
	return *p
}

// since reads ?since=: RFC 3339, or a duration back from now such as 24h.
func since(q string, now time.Time, def time.Duration) (time.Time, error) {
	if q == "" {
		return now.Add(-def), nil
	}
	if d, err := time.ParseDuration(q); err == nil {
		return now.Add(-d), nil
	}
	t, err := time.Parse(time.RFC3339, q)
	if err != nil {
		return time.Time{}, fmt.Errorf("since: %q is neither RFC 3339 nor a duration such as 24h", q)
	}
	return t, nil
}

func (env Env) ledgerEvents(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	from, err := since(q.Get("since"), env.Now(), 7*24*time.Hour)
	if err != nil {
		fail(w, http.StatusBadRequest, "%v", err)
		return
	}
	limit := defaultLimit
	if s := q.Get("limit"); s != "" {
		if limit, err = strconv.Atoi(s); err != nil || limit < 1 || limit > maxLimit {
			fail(w, http.StatusBadRequest, "limit must be 1 to %d", maxLimit)
			return
		}
	}
	where, args := []string{"ts >= ?"}, []any{from.UnixMilli()}
	for _, f := range []string{"owner", "vm", "machine", "host", "type", "op", "repo", "client"} {
		if v := q.Get(f); v != "" {
			where = append(where, f+" = ?")
			args = append(args, v)
		}
	}
	db, err := env.Ledger()
	if err != nil {
		fail(w, http.StatusInternalServerError, "the LEDGER database: %v", err)
		return
	}
	evs, err := queryEvents(db, "SELECT "+eventColumns+" FROM events WHERE "+strings.Join(where, " AND ")+
		" ORDER BY ts DESC, id LIMIT "+strconv.Itoa(limit), args...)
	if err != nil {
		fail(w, http.StatusBadGateway, "reading events: %v", err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{"since": from.UTC(), "events": evs})
}

func queryEvents(db *sql.DB, query string, args ...any) ([]Event, error) {
	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []Event{}
	for rows.Next() {
		var e Event
		if err := e.scan(rows); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// The view: what is true now, worked out from the events in the window.

type machineView struct {
	Machine     string `json:"machine"`
	Host        string `json:"host"`
	LastSeen    int64  `json:"last_seen"`
	LastOwner   string `json:"last_owner"`
	LastCommand string `json:"last_command"`
	Version     string `json:"version"`
}

type vmView struct {
	Machine      string `json:"machine"`
	Host         string `json:"host"`
	VM           string `json:"vm"`
	State        string `json:"state"` // in-use, stale, idle or deleted
	Owner        string `json:"owner"` // of the last event
	Client       string `json:"client"`
	Repo         string `json:"repo"`
	LastCommand  string `json:"last_command"`
	LastType     string `json:"last_type"`
	LastActivity int64  `json:"last_activity"`
	Created      int64  `json:"created,omitempty"`
	Deleted      int64  `json:"deleted,omitempty"`
	Open         int    `json:"open"`
}

type openView struct {
	Event
	AgeSeconds int64  `json:"age_s"`
	Stale      bool   `json:"stale"`
	Why        string `json:"why,omitempty"`
}

type ledgerView struct {
	Now        int64         `json:"now"`
	Since      int64         `json:"since"`
	StaleAfter string        `json:"stale_after"`
	Truncated  bool          `json:"truncated"`
	Machines   []machineView `json:"machines"`
	VMs        []vmView      `json:"vms"`
	Open       []openView    `json:"open"`
	Recent     []Event       `json:"recent"`
}

func (env Env) view(r *http.Request) (ledgerView, int, error) {
	q := r.URL.Query()
	now := env.Now()
	from, err := since(q.Get("since"), now, defaultWindow)
	if err != nil {
		return ledgerView{}, http.StatusBadRequest, err
	}
	stale := defaultStale
	if s := q.Get("stale"); s != "" {
		if stale, err = time.ParseDuration(s); err != nil || stale <= 0 {
			return ledgerView{}, http.StatusBadRequest, fmt.Errorf("stale: %q is not a positive duration such as 90m", s)
		}
	}
	db, err := env.Ledger()
	if err != nil {
		return ledgerView{}, http.StatusInternalServerError, fmt.Errorf("the LEDGER database: %v", err)
	}
	evs, err := queryEvents(db, "SELECT "+eventColumns+" FROM events WHERE ts >= ? ORDER BY ts, id LIMIT "+strconv.Itoa(maxViewRows+1), from.UnixMilli())
	if err != nil {
		return ledgerView{}, http.StatusBadGateway, fmt.Errorf("reading events: %v", err)
	}
	v := buildView(evs, now, stale)
	v.Since = from.UnixMilli()
	if len(evs) > maxViewRows {
		v.Truncated = true
	}
	return v, http.StatusOK, nil
}

// buildView works out the current state from events in time order.
func buildView(evs []Event, now time.Time, stale time.Duration) ledgerView {
	if len(evs) > maxViewRows {
		evs = evs[:maxViewRows]
	}
	v := ledgerView{Now: now.UnixMilli(), StaleAfter: stale.String(),
		Machines: []machineView{}, VMs: []vmView{}, Open: []openView{}, Recent: []Event{}}
	machines := map[string]*machineView{}
	vms := map[[2]string]*vmView{}
	// Which ops were closed, worked out first: a command that ends in the
	// millisecond it started has a start and an end with the same ts, and
	// sorted by id the end can come first (measured live, 1 Oct 2026: an
	// app-create refused in 0 ms showed as in use).
	// And within one millisecond, opening before anything else before
	// closing, so the last event of a VM is its end, not its start.
	rank := func(t string) int {
		switch {
		case opens[t]:
			return 0
		case closes[t]:
			return 2
		}
		return 1
	}
	sort.SliceStable(evs, func(i, j int) bool {
		if evs[i].TS != evs[j].TS {
			return evs[i].TS < evs[j].TS
		}
		return rank(evs[i].Type) < rank(evs[j].Type)
	})
	closed := map[string]bool{}
	for _, e := range evs {
		if e.Op != "" && closes[e.Type] {
			closed[e.Op] = true
		}
	}
	open := map[string]Event{} // op -> the event that opened it
	for _, e := range evs {
		m := machines[e.Machine]
		if m == nil {
			m = &machineView{Machine: e.Machine}
			machines[e.Machine] = m
		}
		m.LastSeen = e.TS
		// An event that leaves a field empty does not erase what is known.
		setIf(&m.Host, e.Host)
		setIf(&m.LastOwner, e.Owner)
		setIf(&m.Version, e.Version)
		setIf(&m.LastCommand, e.Command)
		if e.Op != "" && opens[e.Type] && !closed[e.Op] {
			open[e.Op] = e
		}
		if e.VM == "" {
			continue
		}
		k := [2]string{e.Machine, e.VM}
		vm := vms[k]
		if vm == nil {
			vm = &vmView{Machine: e.Machine, VM: e.VM}
			vms[k] = vm
		}
		vm.LastType, vm.LastActivity = e.Type, e.TS
		setIf(&vm.Host, e.Host)
		setIf(&vm.Owner, e.Owner)
		setIf(&vm.Client, e.Client)
		setIf(&vm.Repo, e.Repo)
		setIf(&vm.LastCommand, e.Command)
		switch e.Type {
		case "vm-create":
			vm.Created, vm.Deleted = e.TS, 0
		case "vm-delete", "reap":
			vm.Deleted = e.TS
		}
	}
	for _, e := range open {
		o := openView{Event: e, AgeSeconds: (now.UnixMilli() - e.TS) / 1000}
		switch {
		case e.Expires != nil && *e.Expires < now.UnixMilli():
			o.Stale, o.Why = true, "lease expired, never released"
		case e.Expires == nil && now.Sub(time.UnixMilli(e.TS)) > stale:
			o.Stale = true
			if e.Type == "start" {
				o.Why = "started, never ended"
			} else {
				o.Why = "acquired, never released"
			}
		}
		v.Open = append(v.Open, o)
		if vm := vms[[2]string{e.Machine, e.VM}]; e.VM != "" && vm != nil {
			vm.Open++
			if !o.Stale {
				vm.State = "in-use"
			} else if vm.State == "" {
				vm.State = "stale"
			}
		}
	}
	for _, vm := range vms {
		switch {
		case vm.State != "":
		case vm.Deleted != 0:
			vm.State = "deleted"
		default:
			vm.State = "idle"
		}
		v.VMs = append(v.VMs, *vm)
	}
	for _, m := range machines {
		v.Machines = append(v.Machines, *m)
	}
	sort.Slice(v.Machines, func(i, j int) bool { return v.Machines[i].LastSeen > v.Machines[j].LastSeen })
	sort.Slice(v.VMs, func(i, j int) bool { return v.VMs[i].LastActivity > v.VMs[j].LastActivity })
	sort.Slice(v.Open, func(i, j int) bool { return v.Open[i].TS < v.Open[j].TS })
	for i := len(evs) - 1; i >= 0 && len(v.Recent) < 50; i-- {
		v.Recent = append(v.Recent, evs[i])
	}
	return v
}

func setIf(dst *string, v string) {
	if v != "" {
		*dst = v
	}
}

func (env Env) ledgerVMs(w http.ResponseWriter, r *http.Request) {
	v, code, err := env.view(r)
	if err != nil {
		fail(w, code, "%v", err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, v)
}
