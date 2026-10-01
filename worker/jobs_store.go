package main

import (
	"crypto/sha256"
	"encoding/hex"
	"sync"
)

// CAS is a compare-and-swap over one small object: the job queue's index
// (jobs.go). R2 gives this with a conditional put (onlyIf etagMatches), and a
// put that loses the race stores nothing and returns null, so the queue is
// linearisable with no Durable Object. workers-go can call a Durable Object
// but not define one: the class would have to be JavaScript, outside go test.
type CAS interface {
	// Load returns the object and its etag, ok=false when there is none.
	Load(key string) (body []byte, etag string, ok bool, err error)
	// Swap stores body only if the object's etag is still etag, or, with
	// etag "", only if there is no object. ok=false means someone else wrote
	// first and nothing was stored.
	Swap(key string, body []byte, etag string) (ok bool, err error)
}

// JobBucket is the JOBS bucket: the index through CAS, and the binaries,
// logs and results through Blobs, streamed as the golden image is.
type JobBucket interface {
	CAS
	Blobs
}

// memJobs is a JobBucket in memory, for the host build and the tests. Its
// etag is the SHA-256 of the body, as good as R2's for a compare.
type memJobs struct {
	*memBlobs
	mu    sync.Mutex
	small map[string][]byte

	// beforeSwap, when set, runs inside Swap before the compare: a test
	// writes there to lose the race on purpose.
	beforeSwap func()
}

func newMemJobs() *memJobs { return &memJobs{memBlobs: newMemBlobs(), small: map[string][]byte{}} }

func etagOf(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:8])
}

func (m *memJobs) Load(key string) ([]byte, string, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.small[key]
	if !ok {
		return nil, "", false, nil
	}
	return append([]byte(nil), b...), etagOf(b), true, nil
}

func (m *memJobs) Swap(key string, body []byte, etag string) (bool, error) {
	if f := m.beforeSwap; f != nil {
		m.beforeSwap = nil
		f()
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	cur, ok := m.small[key]
	switch {
	case etag == "" && ok, etag != "" && (!ok || etagOf(cur) != etag):
		return false, nil
	}
	m.small[key] = append([]byte(nil), body...)
	return true, nil
}
