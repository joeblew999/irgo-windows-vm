package utmvm

// The golden cache through the project's Worker (worker/golden.go), which has
// the bucket bound, so no machine needs R2's S3 keys. The requests, their
// paths and which token each carries are internal/workerclient's, from the
// golden-* routes of wire.Routes: reads carry IRGO_GOLDEN_TOKEN; writes,
// deletes and listing IRGO_GOLDEN_PUSH_TOKEN. Every byte still goes through
// the same checks as the S3 path: the Worker adds one more, R2 refusing a put
// that does not hash to what was claimed.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"github.com/joeblew999/irgo-windows-vm/internal/workerclient"
	"github.com/joeblew999/irgo-windows-vm/wire"
)

type workerStore struct{ c *workerclient.Client }

func newWorkerStore(c R2Config) workerStore {
	return workerStore{workerclient.New(c.WorkerURL, map[wire.Scope]string{
		wire.ScopeGoldenRead:  c.Token,
		wire.ScopeGoldenWrite: c.PushToken,
	})}
}

// Where names the golden routes for a person, without any credential.
func (s workerStore) Where() string { return s.c.URL(wire.RouteGoldenGet, "") }

func (s workerStore) head(ctx context.Context, key string) (int64, string, bool, error) {
	info, ok, err := s.c.GoldenHead(ctx, key)
	return info.Size, info.SHA256, ok, err
}

func (s workerStore) get(ctx context.Context, key string) ([]byte, bool, error) {
	return s.c.GoldenGet(ctx, key, 64<<20)
}

func (s workerStore) put(ctx context.Context, key string, b []byte) error {
	sum := sha256.Sum256(b)
	if err := s.c.GoldenPut(ctx, key, b, hex.EncodeToString(sum[:])); err != nil {
		return fmt.Errorf("uploading %s: %w", key, err)
	}
	return nil
}

func (s workerStore) del(ctx context.Context, key string) error {
	if err := s.c.GoldenDelete(ctx, key); err != nil {
		return fmt.Errorf("deleting %s: %w", key, err)
	}
	return nil
}

// list pages through golden-list, which knows two prefixes only.
func (s workerStore) list(ctx context.Context, prefix string) (map[string]int64, error) {
	for kind, p := range wire.GoldenListKinds {
		if p == prefix {
			return s.c.GoldenList(ctx, kind)
		}
	}
	return nil, fmt.Errorf("the Worker lists %smanifests/ and %schunks/ only, not %s", goldenPrefix, goldenPrefix, prefix)
}

func (s workerStore) fetch(_ context.Context, key, dest string, want digest) error {
	u, h := s.c.GoldenFetch(key)
	return isoDownload(u, h, dest, want, nil)
}
