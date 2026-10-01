package wire

import (
	"encoding/json"
	"time"
)

// Error is every error answer's body.
type Error struct {
	Error string `json:"error"`
	Code  Code   `json:"code"`
}

// Health is GET /api/health.
type Health struct {
	OK bool `json:"ok"`
}

// GlazeRun is the stored record of a target's newest run: which run it is,
// when the Worker received it, where its files are served, and its manifest
// (glazecheck.Manifest, written as shots.json) exactly as it was posted.
type GlazeRun struct {
	Target   string          `json:"target"`
	Run      string          `json:"run"`
	Received time.Time       `json:"received"`
	Base     string          `json:"base"` // where the run's files are served, ending in /
	Manifest json.RawMessage `json:"manifest"`
}

// GlazeLatest is GET /api/glaze-status: a run per target, nil for a target
// that has none.
type GlazeLatest map[string]*GlazeRun

// GlazePosted is the answer to a posted run.
type GlazePosted struct {
	Target   string `json:"target"`
	Run      string `json:"run"`
	Pictures int    `json:"pictures"`
}

// BlobInfo is what the API says about a stored golden object.
type BlobInfo struct {
	Key    string `json:"key"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256,omitempty"` // as verified on PUT; "" for one not stored by this API
}

// GoldenList is one page of a golden listing; Cursor is "" after the last.
type GoldenList struct {
	Objects []BlobInfo `json:"objects"`
	Cursor  string     `json:"cursor"`
}
