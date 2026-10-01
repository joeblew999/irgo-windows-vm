package utmvm

// The golden cache through the project's Worker (worker/golden.go), which has
// the bucket bound, so no machine needs R2's S3 keys. Reads carry
// IRGO_GOLDEN_TOKEN; writes, deletes and listing carry IRGO_GOLDEN_PUSH_TOKEN.
// Every byte still goes through the same checks as the S3 path: the Worker
// adds one more, R2 refusing a put that does not hash to what was claimed.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// The Worker's headers (worker/golden.go).
const (
	hdrGoldenSHA256 = "X-Golden-Sha256"
	hdrGoldenSize   = "X-Golden-Size"
)

type workerStore struct {
	c  R2Config
	hc *http.Client
}

// newWorkerStore uses a client with no overall timeout, like isoDownload: a
// 64 MiB chunk over a slow uplink takes minutes. ctx bounds each call.
func newWorkerStore(c R2Config) workerStore { return workerStore{c: c, hc: &http.Client{}} }

func (s workerStore) url(key string) string { return s.c.WorkerURL + "/api/golden/" + key }

// do sends one request, trying again on a dropped connection, a 5xx or a 429:
// a push is hundreds of requests, and one lost to a transient fault should not
// cost the rest. Everything else is the answer.
func (s workerStore) do(ctx context.Context, method, u, token string, body []byte, header http.Header) (*http.Response, error) {
	const attempts = 4
	for attempt := 1; ; attempt++ {
		var rd io.Reader
		if body != nil {
			rd = bytes.NewReader(body)
		}
		req, err := http.NewRequestWithContext(ctx, method, u, rd)
		if err != nil {
			return nil, err
		}
		for k, v := range header {
			req.Header[k] = v
		}
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := s.hc.Do(req)
		retry := err != nil || resp.StatusCode >= 500 || resp.StatusCode == http.StatusTooManyRequests
		if !retry || attempt == attempts || ctx.Err() != nil {
			return resp, err
		}
		if resp != nil {
			_ = resp.Body.Close() // retried
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(time.Duration(attempt) * time.Second):
		}
	}
}

// failure turns an unexpected answer into an error naming the key and what
// the Worker said, never the token.
func (s workerStore) failure(method, key string, resp *http.Response) error {
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	msg := strings.TrimSpace(string(b))
	var e struct{ Error string }
	if json.Unmarshal(b, &e) == nil && e.Error != "" {
		msg = e.Error
	}
	hint := ""
	if resp.StatusCode == http.StatusUnauthorized {
		hint = " (IRGO_GOLDEN_TOKEN for reading, IRGO_GOLDEN_PUSH_TOKEN for writing, must be the Worker's)"
	}
	return fmt.Errorf("%s %s through the Worker: HTTP %s: %s%s", method, key, resp.Status, msg, hint)
}

func (s workerStore) head(ctx context.Context, key string) (int64, string, bool, error) {
	resp, err := s.do(ctx, http.MethodHead, s.url(key), s.c.Token, nil, nil)
	if err != nil {
		return 0, "", false, err
	}
	defer func() { _ = resp.Body.Close() }()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return 0, "", false, nil
	default:
		return 0, "", false, s.failure("HEAD", key, resp)
	}
	// Not Content-Length: the runtime does not keep it on an answer to HEAD.
	n, err := strconv.ParseInt(resp.Header.Get(hdrGoldenSize), 10, 64)
	if err != nil {
		return 0, "", false, fmt.Errorf("HEAD %s through the Worker: no usable %s", key, hdrGoldenSize)
	}
	return n, resp.Header.Get(hdrGoldenSHA256), true, nil
}

func (s workerStore) get(ctx context.Context, key string) ([]byte, bool, error) {
	resp, err := s.do(ctx, http.MethodGet, s.url(key), s.c.Token, nil, nil)
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = resp.Body.Close() }()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return nil, false, nil
	default:
		return nil, false, s.failure("GET", key, resp)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	return b, err == nil, err
}

func (s workerStore) put(ctx context.Context, key string, b []byte) error {
	sum := sha256.Sum256(b)
	h := http.Header{}
	h.Set(hdrGoldenSHA256, hex.EncodeToString(sum[:]))
	resp, err := s.do(ctx, http.MethodPut, s.url(key), s.c.PushToken, b, h)
	if err != nil {
		return fmt.Errorf("uploading %s: %w", key, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusCreated {
		return s.failure("PUT", key, resp)
	}
	return nil
}

func (s workerStore) del(ctx context.Context, key string) error {
	resp, err := s.do(ctx, http.MethodDelete, s.url(key), s.c.PushToken, nil, nil)
	if err != nil {
		return fmt.Errorf("deleting %s: %w", key, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNoContent {
		return s.failure("DELETE", key, resp)
	}
	return nil
}

// list pages through /api/golden-list, which knows two prefixes only.
func (s workerStore) list(ctx context.Context, prefix string) (map[string]int64, error) {
	kind := map[string]string{goldenPrefix + "manifests/": "manifests", goldenPrefix + "chunks/": "chunks"}[prefix]
	if kind == "" {
		return nil, fmt.Errorf("the Worker lists %smanifests/ and %schunks/ only, not %s", goldenPrefix, goldenPrefix, prefix)
	}
	out := map[string]int64{}
	cursor := ""
	for {
		u := s.c.WorkerURL + "/api/golden-list/" + kind + "?cursor=" + url.QueryEscape(cursor)
		resp, err := s.do(ctx, http.MethodGet, u, s.c.PushToken, nil, nil)
		if err != nil {
			return nil, fmt.Errorf("listing %s: %w", prefix, err)
		}
		var page struct {
			Objects []struct {
				Key  string `json:"key"`
				Size int64  `json:"size"`
			} `json:"objects"`
			Cursor string `json:"cursor"`
		}
		if resp.StatusCode != http.StatusOK {
			err = s.failure("LIST", prefix, resp)
		} else {
			err = json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(&page)
		}
		_ = resp.Body.Close() // read
		if err != nil {
			return nil, err
		}
		for _, o := range page.Objects {
			out[o.Key] = o.Size
		}
		if page.Cursor == "" {
			return out, nil
		}
		if page.Cursor == cursor {
			return nil, errors.New("listing through the Worker: the cursor did not move")
		}
		cursor = page.Cursor
	}
}

func (s workerStore) fetch(_ context.Context, key, dest string, want digest) error {
	h := http.Header{}
	h.Set("Authorization", "Bearer "+s.c.Token)
	return isoDownload(s.url(key), h, dest, want, nil)
}
