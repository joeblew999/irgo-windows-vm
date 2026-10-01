package wire

import "time"

// The ledger: who used which VM, on which machine, doing what
// (docs/DEVELOPMENT.md, "The ledger"). internal/ledger posts events; the
// Worker stores them in D1 and answers history and the current view.

// LedgerType is an event's type.
type LedgerType string

// The event types. An op pairs the ones that open work with the ones that
// close it: start with end, lease-acquire with lease-release; a reap closes
// the op it names too.
const (
	LedgerStart        LedgerType = "start"
	LedgerEnd          LedgerType = "end"
	LedgerLeaseAcquire LedgerType = "lease-acquire"
	LedgerLeaseRelease LedgerType = "lease-release"
	LedgerVMCreate     LedgerType = "vm-create"
	LedgerVMDelete     LedgerType = "vm-delete"
	LedgerReap         LedgerType = "reap"
)

// LedgerTypes is every type the Worker stores.
var LedgerTypes = []LedgerType{LedgerStart, LedgerEnd, LedgerLeaseAcquire, LedgerLeaseRelease, LedgerVMCreate, LedgerVMDelete, LedgerReap}

// Opens is t opening an op; Closes is t closing one.
func (t LedgerType) Opens() bool { return t == LedgerStart || t == LedgerLeaseAcquire }
func (t LedgerType) Closes() bool {
	return t == LedgerEnd || t == LedgerLeaseRelease || t == LedgerReap
}

// Known is t being one of LedgerTypes.
func (t LedgerType) Known() bool {
	for _, x := range LedgerTypes {
		if x == t {
			return true
		}
	}
	return false
}

// Limits the Worker enforces on posted events; the client keeps under them.
// A batch is at most LedgerMaxBatch events because each is one statement,
// and a Worker on the Free plan may make 50 D1 queries per invocation.
const (
	LedgerMaxBatch  = 25
	LedgerMaxBody   = 256 << 10
	LedgerMaxField  = 200
	LedgerMaxDetail = 500
	LedgerMaxLimit  = 1000 // ledger-events' limit
)

// LedgerEvent is one thing that happened, as posted and as read back.
type LedgerEvent struct {
	ID         string     `json:"id"`
	TS         int64      `json:"ts"` // Unix milliseconds
	Received   int64      `json:"received,omitempty"`
	Type       LedgerType `json:"type"`
	Op         string     `json:"op,omitempty"`
	Machine    string     `json:"machine"`
	Host       string     `json:"host,omitempty"`
	Owner      string     `json:"owner,omitempty"`
	Client     string     `json:"client,omitempty"`
	Repo       string     `json:"repo,omitempty"`
	VM         string     `json:"vm,omitempty"`
	Command    string     `json:"command,omitempty"`
	Exit       *int64     `json:"exit,omitempty"`
	DurationMS *int64     `json:"duration_ms,omitempty"`
	Expires    *int64     `json:"expires,omitempty"` // Unix ms; a lease past this is stale
	Version    string     `json:"version,omitempty"`
	Detail     string     `json:"detail,omitempty"`
}

// LedgerBatch is ledger-post's body.
type LedgerBatch struct {
	Events []LedgerEvent `json:"events"`
}

// LedgerPosted is ledger-post's answer. Rejected events are never stored
// and sending them again will not change that.
type LedgerPosted struct {
	Accepted   int              `json:"accepted"`
	Duplicates int              `json:"duplicates"`
	Rejected   []LedgerRejected `json:"rejected"`
}

// LedgerRejected is one event refused, and why.
type LedgerRejected struct {
	ID    string `json:"id"`
	Error string `json:"error"`
}

// LedgerEvents is ledger-events' answer: newest first.
type LedgerEvents struct {
	Since  time.Time     `json:"since"`
	Events []LedgerEvent `json:"events"`
}

// LedgerView is what is true now, worked out from the events in a window.
type LedgerView struct {
	Now        int64           `json:"now"`
	Since      int64           `json:"since"`
	StaleAfter string          `json:"stale_after"`
	Truncated  bool            `json:"truncated"`
	Machines   []LedgerMachine `json:"machines"`
	VMs        []LedgerVM      `json:"vms"`
	Open       []LedgerOpen    `json:"open"`
	Recent     []LedgerEvent   `json:"recent"`
}

// LedgerMachine is a machine as last seen.
type LedgerMachine struct {
	Machine     string `json:"machine"`
	Host        string `json:"host"`
	LastSeen    int64  `json:"last_seen"`
	LastOwner   string `json:"last_owner"`
	LastCommand string `json:"last_command"`
	Version     string `json:"version"`
}

// LedgerVM is a VM on a machine.
type LedgerVM struct {
	Machine      string     `json:"machine"`
	Host         string     `json:"host"`
	VM           string     `json:"vm"`
	State        string     `json:"state"` // in-use, stale, idle or deleted
	Owner        string     `json:"owner"` // of the last event
	Client       string     `json:"client"`
	Repo         string     `json:"repo"`
	LastCommand  string     `json:"last_command"`
	LastType     LedgerType `json:"last_type"`
	LastActivity int64      `json:"last_activity"`
	Created      int64      `json:"created,omitempty"`
	Deleted      int64      `json:"deleted,omitempty"`
	Open         int        `json:"open"`
}

// LedgerOpen is work opened and not closed.
type LedgerOpen struct {
	LedgerEvent
	AgeSeconds int64  `json:"age_s"`
	Stale      bool   `json:"stale"`
	Why        string `json:"why,omitempty"`
}
