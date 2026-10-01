// Package ledger reports what the tool does to the Worker's ledger: which
// agent used which VM, on which machine, doing what (docs/ARCHITECTURE.md, "The
// ledger client"). The local lock files stay the authority; this is the durable,
// cross-machine record of them.
//
// It never decides anything and never fails a command. An event is appended
// to a spool file under the runtime directory first, which is instant and
// works offline, then sent in the background; whatever could not be sent
// stays spooled and goes with the next flush. Off unless IRGO_LEDGER_URL and
// IRGO_LEDGER_TOKEN are both set.
//
// From anywhere in the tool (the lease code in utmvm included):
//
//	ledger.Emit(ledger.Event{Type: ledger.LeaseAcquire, Op: leaseID, VM: name, Expires: &until})
//
// Emit is a no-op until main has called Configure, so a package can report
// without knowing whether the ledger is on.
package ledger

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Type is what happened. An Op pairs the types that open work with those
// that close it: Start with End, LeaseAcquire with LeaseRelease; Reap closes
// the op it names. The Worker flags open work it never sees closed.
type Type string

const (
	Start        Type = "start"
	End          Type = "end"
	LeaseAcquire Type = "lease-acquire"
	LeaseRelease Type = "lease-release"
	VMCreate     Type = "vm-create"
	VMDelete     Type = "vm-delete"
	Reap         Type = "reap"
)

// Event is one thing that happened. The JSON names are the wire contract with
// the Worker (worker/ledger.go, Event). Record fills ID, TS, Machine, Host,
// Owner, Repo, Version and Client when they are empty.
type Event struct {
	ID         string `json:"id"`
	TS         int64  `json:"ts"` // Unix milliseconds
	Type       Type   `json:"type"`
	Op         string `json:"op,omitempty"`
	Machine    string `json:"machine"`
	Host       string `json:"host,omitempty"`
	Owner      string `json:"owner,omitempty"`
	Client     string `json:"client,omitempty"`
	Repo       string `json:"repo,omitempty"`
	VM         string `json:"vm,omitempty"`
	Command    string `json:"command,omitempty"`
	Exit       *int   `json:"exit,omitempty"`
	DurationMS *int64 `json:"duration_ms,omitempty"`
	Expires    *int64 `json:"expires,omitempty"` // Unix ms; a lease past this is stale
	Version    string `json:"version,omitempty"`
	Detail     string `json:"detail,omitempty"`
}

// Limits the Worker enforces (worker/ledger.go); kept under them here so an
// event is never refused for its size.
const (
	maxBatch   = 25
	maxField   = 200
	maxDetail  = 500
	maxSpool   = 4 << 20 // past this, new events are dropped rather than filling the disk
	sendBudget = 2 * time.Second
)

// Config is how a Client is set up. FromEnv is the usual way.
type Config struct {
	URL, Token string
	Dir        string // where the spool and the machine id live
	Version    string
	HTTP       *http.Client     // nil: one with a 2 s timeout
	Now        func() time.Time // nil: time.Now
	// Backoff is how long after a failed send nothing is tried, so an
	// offline machine pays nothing per command. 0 means one minute.
	Backoff time.Duration
}

// Client spools and sends events. A nil *Client is valid and does nothing.
type Client struct {
	cfg      Config
	id       *identity
	inFlight atomic.Bool
	wg       sync.WaitGroup
}

// New is a Client, or nil when cfg has no URL or no token: the ledger is off.
func New(cfg Config) *Client {
	if cfg.URL == "" || cfg.Token == "" || cfg.Dir == "" {
		return nil
	}
	cfg.URL = strings.TrimRight(cfg.URL, "/")
	if cfg.HTTP == nil {
		cfg.HTTP = &http.Client{Timeout: sendBudget}
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Backoff == 0 {
		cfg.Backoff = time.Minute
	}
	return &Client{cfg: cfg, id: newIdentity(cfg.Dir)}
}

// FromEnv is New from IRGO_LEDGER_URL and IRGO_LEDGER_TOKEN, spooling under
// dir/ledger. Nil, so off, unless both are set.
func FromEnv(dir, version string) *Client {
	return New(Config{
		URL:     os.Getenv("IRGO_LEDGER_URL"),
		Token:   os.Getenv("IRGO_LEDGER_TOKEN"),
		Dir:     filepath.Join(dir, "ledger"),
		Version: version,
	})
}

// NewID is a random id for an event or an op: 32 hex digits.
func NewID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b) // crypto/rand.Read does not fail
	return hex.EncodeToString(b)
}

// Record fills in what e leaves empty, redacts it and appends it to the
// spool. It does not send. The error is for tests; callers ignore it.
func (c *Client) Record(e Event) error {
	if c == nil {
		return nil
	}
	if e.ID == "" {
		e.ID = NewID()
	}
	if e.TS == 0 {
		e.TS = c.cfg.Now().UnixMilli()
	}
	if e.Machine == "" {
		e.Machine = c.id.machine()
	}
	if e.Host == "" {
		e.Host = c.id.host
	}
	if e.Owner == "" {
		e.Owner = c.id.owner
	}
	if e.Repo == "" {
		e.Repo = c.id.repo
	}
	if e.Client == "" {
		e.Client = "cli"
	}
	if e.Version == "" {
		e.Version = c.cfg.Version
	}
	e.Detail = clip(Redact(e.Detail), maxDetail)
	e.Repo = Redact(e.Repo)
	for _, f := range []*string{&e.Host, &e.Owner, &e.Client, &e.Repo, &e.VM, &e.Command, &e.Version} {
		*f = clip(*f, maxField)
	}
	line, err := json.Marshal(e)
	if err != nil {
		return err
	}
	return c.appendSpool(append(line, '\n'))
}

// FlushAsync sends the spool in the background, unless a send from this
// process is already under way. Drain waits for it.
func (c *Client) FlushAsync() {
	if c == nil || !c.inFlight.CompareAndSwap(false, true) {
		return
	}
	c.wg.Go(func() {
		defer c.inFlight.Store(false)
		ctx, cancel := context.WithTimeout(context.Background(), sendBudget)
		defer cancel()
		_ = c.Flush(ctx)
	})
}

// Drain waits for a background send, then sends what is left, all within
// budget. main calls it before exiting; past the budget the rest stays
// spooled for the next run.
func (c *Client) Drain(budget time.Duration) {
	if c == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), budget)
	defer cancel()
	done := make(chan struct{})
	go func() { c.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-ctx.Done():
		return
	}
	_ = c.Flush(ctx)
}

// errOffline is a flush skipped because a recent one failed.
var errOffline = errors.New("ledger: the last send failed recently; spooled for later")

// Flush sends every spooled event, a batch at a time. Another process
// already flushing is not an error: it will send what is here.
func (c *Client) Flush(ctx context.Context) error {
	if c == nil {
		return nil
	}
	if c.backingOff() {
		return errOffline
	}
	unlock, ok := tryLock(filepath.Join(c.cfg.Dir, "flush.lock"))
	if !ok {
		return nil
	}
	defer unlock()
	// A few rounds, so events recorded while a round was sending go too.
	for range 4 {
		more, err := c.flushRound(ctx)
		if err != nil || !more {
			return err
		}
	}
	return nil
}

// flushRound sends inflight.jsonl, first moving the spool there when it is
// not already: what a crashed flush left goes before anything newer.
func (c *Client) flushRound(ctx context.Context) (more bool, err error) {
	inflight := filepath.Join(c.cfg.Dir, "inflight.jsonl")
	if _, err := os.Stat(inflight); errors.Is(err, os.ErrNotExist) {
		moved, err := c.takeSpool(inflight)
		if err != nil || !moved {
			return false, err
		}
	}
	b, err := os.ReadFile(inflight)
	if err != nil {
		return false, err
	}
	var lines [][]byte
	for l := range bytes.SplitSeq(b, []byte("\n")) {
		if json.Valid(l) {
			lines = append(lines, l) // a torn or empty line is dropped
		}
	}
	for i := 0; i < len(lines); i += maxBatch {
		batch := lines[i:min(i+maxBatch, len(lines))]
		if err := c.post(ctx, batch); err != nil {
			c.markOffline()
			// Keep what was not sent; a batch the Worker did store is
			// harmless to send again, the ids make it a no-op.
			_ = writeAtomic(inflight, append(bytes.Join(lines[i:], []byte("\n")), '\n'))
			return false, err
		}
	}
	c.clearOffline()
	return true, os.Remove(inflight)
}

// post sends one batch. A 2xx, and a 400 or 413 (the Worker will never take
// these bytes, so sending them again would block the spool forever), count
// as done. Anything else, a 401 included, keeps them for later.
func (c *Client) post(ctx context.Context, batch [][]byte) error {
	body := append([]byte(`{"events":[`), bytes.Join(batch, []byte(","))...)
	body = append(body, "]}"...)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.URL+"/api/ledger/events", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.cfg.Token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.cfg.HTTP.Do(req)
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	switch {
	case resp.StatusCode/100 == 2, resp.StatusCode == http.StatusBadRequest, resp.StatusCode == http.StatusRequestEntityTooLarge:
		return nil
	}
	return fmt.Errorf("ledger: %s answered %s", c.cfg.URL, resp.Status)
}

// The backoff mark: the time of the last failed send, in Unix ms, read with
// the Client's clock so tests can move past it.
func (c *Client) offlinePath() string { return filepath.Join(c.cfg.Dir, "offline") }

func (c *Client) backingOff() bool {
	b, err := os.ReadFile(c.offlinePath())
	if err != nil {
		return false
	}
	var at int64
	if _, err := fmt.Sscan(string(b), &at); err != nil {
		return false
	}
	return c.cfg.Now().Sub(time.UnixMilli(at)) < c.cfg.Backoff
}

func (c *Client) markOffline() {
	_ = os.WriteFile(c.offlinePath(), fmt.Appendf(nil, "%d\n", c.cfg.Now().UnixMilli()), 0o600)
}

func (c *Client) clearOffline() { _ = os.Remove(c.offlinePath()) }

// The default Client, set once by main.
var std atomic.Pointer[Client]

// Configure makes c the Client Emit and DrainDefault use. Nil turns them off.
func Configure(c *Client) { std.Store(c) }

// Emit records e with the configured Client and starts sending it. It never
// blocks on the network and never fails.
func Emit(e Event) {
	if c := std.Load(); c != nil {
		_ = c.Record(e)
		c.FlushAsync()
	}
}

// DrainDefault is Drain on the configured Client.
func DrainDefault(budget time.Duration) { std.Load().Drain(budget) }
