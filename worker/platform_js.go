//go:build js && wasm

package main

import (
	"bytes"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"strings"
	"syscall/js"

	"github.com/syumai/workers-go/cloudflare"
	"github.com/syumai/workers-go/cloudflare/d1"
	"github.com/syumai/workers-go/cloudflare/r2"
)

// ledgerDB is the D1 binding LEDGER (wrangler.toml), through workers-go's
// database/sql driver. Opened per request: the binding belongs to the
// request's environment.
func ledgerDB() (*sql.DB, error) {
	c, err := d1.OpenConnector("LEDGER")
	if err != nil {
		return nil, fmt.Errorf("LEDGER is not bound (wrangler.toml, d1_databases): %w", err)
	}
	return sql.OpenDB(c), nil
}

// getenv reads a var or secret of the current request's environment.
func getenv(name string) string { return cloudflare.Getenv(name) }

// siteBucket is the R2 binding SITE (wrangler.toml).
func siteBucket() (Store, error) {
	b, err := r2.NewBucket("SITE")
	if err != nil {
		return nil, err
	}
	return r2Store{b}, nil
}

type r2Store struct{ b *r2.Bucket }

func (s r2Store) Get(key string) ([]byte, string, bool, error) {
	o, err := s.b.Get(key)
	if err != nil || o == nil {
		return nil, "", false, err
	}
	body, err := io.ReadAll(o.Body)
	if err != nil {
		return nil, "", false, err
	}
	return body, o.HTTPMetadata.ContentType, true, nil
}

func (s r2Store) Put(key string, body []byte, contentType string) error {
	_, err := s.b.Put(key, io.NopCloser(bytes.NewReader(body)), &r2.PutOptions{
		HTTPMetadata: r2.HTTPMetadata{ContentType: contentType},
	})
	return err
}

// goldenBucket is the R2 binding GOLDEN (wrangler.toml), driven through
// syscall/js rather than workers-go's r2 package, whose Put reads the whole
// body into Wasm memory and whose Get takes no range.
func goldenBucket() (Blobs, error) {
	b := cloudflare.GetBinding("GOLDEN")
	if b.IsUndefined() {
		return nil, errors.New("GOLDEN is not bound (wrangler.toml, r2_buckets)")
	}
	return jsBlobs{b}, nil
}

type jsBlobs struct{ b js.Value }

// The two halves of workers-go that let a stream pass through without Go
// reading it (internal/jsutil/stream.go, v0.36.0): a request body is a
// RawJSBodyGetter, and the ResponseWriter is a RawJSBodyWriter, which io.Copy
// reaches through the body's WriteTo.
type (
	rawJSBodyGetter interface{ GetRawJSBody() js.Value }
	rawJSBodyWriter interface{ WriteRawJSBody(body js.Value) }
)

// metaSHA256 is the custom metadata that holds an object's SHA-256. The S3
// path in vm_golden_cache.go writes the same name (x-amz-meta-zsha256), so
// objects pushed either way read the same.
const metaSHA256 = "zsha256"

func blobInfo(o js.Value) BlobInfo {
	info := BlobInfo{Key: o.Get("key").String(), Size: int64(o.Get("size").Float())}
	if m := o.Get("customMetadata"); m.Truthy() {
		if v := m.Get(metaSHA256); v.Type() == js.TypeString {
			info.SHA256 = v.String()
		}
	}
	return info
}

func (s jsBlobs) Head(key string) (BlobInfo, bool, error) {
	o, err := await(s.b.Call("head", key))
	if err != nil || o.IsNull() {
		return BlobInfo{}, false, err
	}
	return blobInfo(o), true, nil
}

func (s jsBlobs) Get(key string, rng *byteRange) (BlobInfo, io.ReadCloser, bool, error) {
	opts := js.Global().Get("Object").New()
	if rng != nil {
		r := js.Global().Get("Object").New()
		r.Set("offset", rng.Offset)
		r.Set("length", rng.Length)
		opts.Set("range", r)
	}
	o, err := await(s.b.Call("get", key, opts))
	if err != nil || o.IsNull() {
		return BlobInfo{}, nil, false, err
	}
	return blobInfo(o), &jsStream{v: o.Get("body")}, true, nil
}

func (s jsBlobs) Put(key string, body io.Reader, size int64, sum string) (BlobInfo, error) {
	raw, ok := body.(rawJSBodyGetter)
	if !ok {
		return BlobInfo{}, errors.New("the request body is not the runtime's stream; refusing to read it into memory")
	}
	opts := js.Global().Get("Object").New()
	opts.Set("sha256", sum) // R2 hashes what arrives and refuses the put on a mismatch
	meta := js.Global().Get("Object").New()
	meta.Set(metaSHA256, sum)
	opts.Set("customMetadata", meta)
	o, err := await(s.b.Call("put", key, raw.GetRawJSBody(), opts))
	if err != nil {
		if isChecksumError(err) {
			return BlobInfo{}, fmt.Errorf("%w (%v)", errDigestMismatch, err)
		}
		return BlobInfo{}, err
	}
	if o.IsNull() {
		return BlobInfo{}, errors.New("R2 stored nothing")
	}
	return blobInfo(o), nil
}

// isChecksumError is R2 refusing a put whose body does not match its sha256:
// "put: The SHA-256 checksum you specified did not match what we received.
// [...] (10037)", measured 1 Oct 2026 under wrangler dev.
func isChecksumError(err error) bool {
	m := err.Error()
	return strings.Contains(m, "(10037)") || strings.Contains(m, "checksum you specified did not match")
}

func (s jsBlobs) Delete(key string) error {
	_, err := await(s.b.Call("delete", key))
	return err
}

func (s jsBlobs) List(prefix, cursor string) ([]BlobInfo, string, error) {
	opts := js.Global().Get("Object").New()
	opts.Set("prefix", prefix)
	opts.Set("limit", 1000)
	opts.Set("include", js.Global().Get("Array").New("customMetadata"))
	if cursor != "" {
		opts.Set("cursor", cursor)
	}
	res, err := await(s.b.Call("list", opts))
	if err != nil {
		return nil, "", err
	}
	objs := res.Get("objects")
	out := make([]BlobInfo, objs.Length())
	for i := range out {
		out[i] = blobInfo(objs.Index(i))
	}
	next := ""
	if res.Get("truncated").Truthy() {
		next = res.Get("cursor").String()
	}
	return out, next, nil
}

// jsStream is an R2 object's body, which is never read in Go: io.Copy into
// the ResponseWriter hands the stream itself to the Response.
type jsStream struct {
	v      js.Value
	handed bool
}

func (s *jsStream) Read([]byte) (int, error) {
	return 0, errors.New("an R2 body is streamed by the runtime, not read in Go")
}

func (s *jsStream) WriteTo(w io.Writer) (int64, error) {
	rw, ok := w.(rawJSBodyWriter)
	if !ok {
		return 0, errors.New("the ResponseWriter cannot take a stream; refusing to read it into memory")
	}
	rw.WriteRawJSBody(s.v)
	s.handed = true
	return 0, nil
}

// Close cancels a body that was never handed over, so R2 stops sending it.
func (s *jsStream) Close() error {
	if !s.handed && s.v.Truthy() {
		s.v.Call("cancel")
	}
	return nil
}

// await waits for a promise. Wasm code here runs in its own goroutine
// (workers-go's handleRequest), so blocking it lets the event loop run.
func await(p js.Value) (js.Value, error) {
	type result struct {
		v   js.Value
		err error
	}
	ch := make(chan result, 1)
	var then, catch js.Func
	then = js.FuncOf(func(_ js.Value, args []js.Value) any {
		ch <- result{v: args[0]}
		return nil
	})
	catch = js.FuncOf(func(_ js.Value, args []js.Value) any {
		ch <- result{err: errors.New(args[0].Call("toString").String())}
		return nil
	})
	p.Call("then", then, catch)
	r := <-ch
	then.Release()
	catch.Release()
	return r.v, r.err
}
