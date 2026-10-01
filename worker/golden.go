package main

// The golden image's private bucket, bound to the Worker as GOLDEN, and the
// only way to it: vm-golden-push and vm-golden-pull talk to these endpoints,
// so no machine needs R2's S3 keys.
//
//	GET    /api/golden/{key}            the object, streamed; Range for resume  (GOLDEN_TOKEN)
//	HEAD   /api/golden/{key}            its size and SHA-256                     (GOLDEN_TOKEN)
//	PUT    /api/golden/{key}            stored if it hashes to X-Golden-SHA256   (GOLDEN_PUSH_TOKEN)
//	DELETE /api/golden/{key}            removed; nothing there is success        (GOLDEN_PUSH_TOKEN)
//	GET    /api/golden-list/{kind}      manifests or chunks, a page at a time    (GOLDEN_PUSH_TOKEN)
//
// {key} is exactly one of the keys internal/utmvm/vm_golden_cache.go writes
// (isGoldenKey); anything else is 404, whatever the token.
//
// Bytes never pass through Go: a GET hands R2's stream to the response, and a
// PUT hands the request's stream to R2, so a 64 MiB chunk costs the Worker
// almost no CPU and no Wasm memory (platform_js.go). That is also why the
// Worker cannot hash a PUT itself: R2 does, against the sha256 the client
// claims, and refuses the put on a mismatch.

import (
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
)

// Blobs is the golden bucket as the API uses it.
type Blobs interface {
	// Head returns ok=false for a key that does not exist.
	Head(key string) (info BlobInfo, ok bool, err error)
	// Get returns the object, or the part rng names when rng is not nil. The
	// body must be copied with io.Copy into the ResponseWriter, which on
	// Workers hands the stream over without reading it.
	Get(key string, rng *byteRange) (info BlobInfo, body io.ReadCloser, ok bool, err error)
	// Put stores exactly size bytes from body, and refuses with
	// errDigestMismatch unless they hash to sha256 (lower-case hex).
	Put(key string, body io.Reader, size int64, sha256 string) (BlobInfo, error)
	Delete(key string) error
	// List returns one page of the keys under prefix, and the cursor for the
	// next page, "" after the last.
	List(prefix, cursor string) (objects []BlobInfo, next string, err error)
}

// BlobInfo is what the API says about a stored object.
type BlobInfo struct {
	Key    string `json:"key"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256,omitempty"` // as verified on PUT; "" for one not stored by this API
}

// byteRange is a resolved Range: Length bytes from Offset.
type byteRange struct{ Offset, Length int64 }

// errDigestMismatch is a PUT whose body does not hash to what it claimed.
var errDigestMismatch = errors.New("the body does not hash to X-Golden-SHA256")

const (
	varGoldenPushToken = "GOLDEN_PUSH_TOKEN" // secret: who may write and delete the golden image

	// maxGoldenPut is the largest object accepted. A chunk is a 64 MiB region
	// compressed with zstd, which adds a few KiB to data it cannot compress,
	// and Workers refuses a body over 100 MB on the Free and Pro plans before
	// the Worker sees it.
	maxGoldenPut = 80 << 20

	hdrSHA256 = "X-Golden-Sha256" // the object's SHA-256: claimed on PUT, verified one on GET and HEAD
	hdrSize   = "X-Golden-Size"   // the whole object's size, also on HEAD, where Content-Length may not survive
)

// goldenKinds are the prefixes golden-list lists. Only these two: latest is
// read directly, and nothing else in the bucket is reachable.
var goldenKinds = map[string]string{"manifests": "golden/manifests/", "chunks": "golden/chunks/"}

// golden is /api/golden/{key}.
func (env Env) golden(w http.ResponseWriter, r *http.Request, key string) {
	switch r.Method {
	case http.MethodGet, http.MethodHead:
		if !env.authorized(w, r, varGoldenToken) {
			return
		}
	case http.MethodPut, http.MethodDelete:
		if !env.authorized(w, r, varGoldenPushToken) {
			return
		}
	default:
		fail(w, http.StatusMethodNotAllowed, "GET, HEAD, PUT or DELETE")
		return
	}
	if !isGoldenKey(key) {
		fail(w, http.StatusNotFound, "not a golden-image key: golden/latest, golden/manifests/<sha256>.json or golden/chunks/<sha256>.zst")
		return
	}
	b, err := env.Golden()
	if err != nil {
		fail(w, http.StatusInternalServerError, "the GOLDEN bucket: %v", err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	switch r.Method {
	case http.MethodHead:
		info, ok, err := b.Head(key)
		switch {
		case err != nil:
			fail(w, http.StatusBadGateway, "reading %s: %v", key, err)
		case !ok:
			w.WriteHeader(http.StatusNotFound)
		default:
			describe(w, info)
			w.WriteHeader(http.StatusOK)
		}
	case http.MethodGet:
		goldenGet(w, r, b, key)
	case http.MethodPut:
		goldenPut(w, r, b, key)
	case http.MethodDelete:
		if err := b.Delete(key); err != nil {
			fail(w, http.StatusBadGateway, "deleting %s: %v", key, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func describe(w http.ResponseWriter, info BlobInfo) {
	w.Header().Set(hdrSize, strconv.FormatInt(info.Size, 10))
	if info.SHA256 != "" {
		w.Header().Set(hdrSHA256, info.SHA256)
	}
	w.Header().Set("Accept-Ranges", "bytes")
	w.Header().Set("Content-Type", "application/octet-stream")
}

// goldenGet serves the object, or with a Range header the part asked for,
// which is how vm-golden-pull resumes a chunk cut off part way.
func goldenGet(w http.ResponseWriter, r *http.Request, b Blobs, key string) {
	var rng *byteRange
	var size int64
	if h := r.Header.Get("Range"); h != "" {
		info, ok, err := b.Head(key)
		switch {
		case err != nil:
			fail(w, http.StatusBadGateway, "reading %s: %v", key, err)
			return
		case !ok:
			fail(w, http.StatusNotFound, "no such object")
			return
		}
		size = info.Size
		var satisfiable bool
		rng, satisfiable = parseRange(h, size)
		if !satisfiable {
			w.Header().Set("Content-Range", "bytes */"+strconv.FormatInt(size, 10))
			fail(w, http.StatusRequestedRangeNotSatisfiable, "range %q is outside the object's %d bytes", h, size)
			return
		}
	}
	info, body, ok, err := b.Get(key, rng)
	switch {
	case err != nil:
		fail(w, http.StatusBadGateway, "reading %s: %v", key, err)
		return
	case !ok:
		fail(w, http.StatusNotFound, "no such object")
		return
	}
	defer func() { _ = body.Close() }()
	describe(w, info)
	if rng != nil {
		if info.Size != size {
			// Replaced between the HEAD and the GET: the range was worked out
			// for other bytes.
			fail(w, http.StatusConflict, "%s changed while it was being read; ask again", key)
			return
		}
		w.Header().Set("Content-Range", "bytes "+strconv.FormatInt(rng.Offset, 10)+"-"+
			strconv.FormatInt(rng.Offset+rng.Length-1, 10)+"/"+strconv.FormatInt(size, 10))
		w.Header().Set("Content-Length", strconv.FormatInt(rng.Length, 10))
		w.WriteHeader(http.StatusPartialContent)
	} else {
		w.Header().Set("Content-Length", strconv.FormatInt(info.Size, 10))
		w.WriteHeader(http.StatusOK)
	}
	_, _ = io.Copy(w, body)
}

// parseRange resolves a single-range header against size. A header that is
// not one byte range is ignored (nil, true), as RFC 9110 allows, and the
// whole object is sent; a range that starts past the end is not satisfiable.
func parseRange(h string, size int64) (*byteRange, bool) {
	spec, ok := strings.CutPrefix(h, "bytes=")
	if !ok || strings.Contains(spec, ",") {
		return nil, true
	}
	first, last, ok := strings.Cut(strings.TrimSpace(spec), "-")
	if !ok {
		return nil, true
	}
	num := func(s string) (int64, bool) {
		if s == "" || !allBytes(s, func(c byte) bool { return '0' <= c && c <= '9' }) || len(s) > 18 {
			return 0, false
		}
		n, err := strconv.ParseInt(s, 10, 64)
		return n, err == nil
	}
	if first == "" { // the last N bytes
		n, ok := num(last)
		if !ok {
			return nil, true
		}
		if n == 0 || size == 0 {
			return nil, false
		}
		n = min(n, size)
		return &byteRange{size - n, n}, true
	}
	start, ok := num(first)
	if !ok {
		return nil, true
	}
	end := size - 1
	if last != "" {
		e, ok := num(last)
		if !ok || e < start {
			return nil, true
		}
		end = min(e, size-1)
	}
	if start >= size {
		return nil, false
	}
	return &byteRange{start, end - start + 1}, true
}

// goldenPut stores one object. The client says what the body hashes to; R2
// checks it as the body arrives and refuses the put if it does not match, so
// nothing is stored that is not what was sent. A manifest is named by that
// hash, so for one the claim must also be its name.
func goldenPut(w http.ResponseWriter, r *http.Request, b Blobs, key string) {
	if r.ContentLength < 0 {
		fail(w, http.StatusLengthRequired, "send Content-Length: R2 stores a stream only of known length")
		return
	}
	if r.ContentLength > maxGoldenPut {
		fail(w, http.StatusRequestEntityTooLarge, "%d bytes, over the %d this accepts", r.ContentLength, maxGoldenPut)
		return
	}
	sum := r.Header.Get(hdrSHA256)
	if !isHex(sum, 64) {
		fail(w, http.StatusBadRequest, "send %s: the body's SHA-256, 64 lower-case hex digits", hdrSHA256)
		return
	}
	if id, ok := strings.CutPrefix(key, "golden/manifests/"); ok && strings.TrimSuffix(id, ".json") != sum {
		fail(w, http.StatusBadRequest, "a manifest is named by its SHA-256, and %s says %s", hdrSHA256, sum)
		return
	}
	info, err := b.Put(key, r.Body, r.ContentLength, sum)
	switch {
	case errors.Is(err, errDigestMismatch):
		fail(w, http.StatusBadRequest, "%v; nothing was stored", err)
		return
	case err != nil:
		fail(w, http.StatusBadGateway, "storing %s: %v", key, err)
		return
	case info.Size != r.ContentLength:
		fail(w, http.StatusBadGateway, "%s was stored with %d bytes, and %d were sent", key, info.Size, r.ContentLength)
		return
	}
	writeJSON(w, http.StatusCreated, BlobInfo{Key: key, Size: info.Size, SHA256: sum})
}

// goldenList is /api/golden-list/{kind}: what vm-golden-push -delete needs to
// find every manifest and every chunk no manifest names. Write token only:
// a machine that only pulls has no reason to enumerate the bucket.
func (env Env) goldenList(w http.ResponseWriter, r *http.Request, kind string) {
	if !env.authorized(w, r, varGoldenPushToken) {
		return
	}
	prefix, ok := goldenKinds[kind]
	if !ok {
		fail(w, http.StatusNotFound, "list manifests or chunks")
		return
	}
	b, err := env.Golden()
	if err != nil {
		fail(w, http.StatusInternalServerError, "the GOLDEN bucket: %v", err)
		return
	}
	objs, next, err := b.List(prefix, r.URL.Query().Get("cursor"))
	if err != nil {
		fail(w, http.StatusBadGateway, "listing %s: %v", prefix, err)
		return
	}
	out := make([]BlobInfo, 0, len(objs))
	for _, o := range objs {
		if isGoldenKey(o.Key) { // anything else under the prefix is not ours to name
			out = append(out, BlobInfo{Key: o.Key, Size: o.Size})
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{"objects": out, "cursor": next})
}
