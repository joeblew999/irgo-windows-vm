// Package workerclient is the one client of the project's Cloudflare Worker
// (worker/). Every URL is built from wire's route table and every token is
// picked by the route's scope, so a caller names a route and never writes a
// path or decides which token goes with it. A successful answer is the
// route's declared status; anything else is an *Error carrying the Worker's
// wire.Code.
//
// No other code builds a Worker URL: TestNoURLsOutsideTheClient (a grep, as
// the compiler cannot enforce it) fails on a "/api/" path anywhere else.
package workerclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/joeblew999/irgo-windows-vm/wire"
)

// Client talks to one Worker.
type Client struct {
	origin string
	tokens map[wire.Scope]string
	hc     *http.Client
	// Attempts is how many times a request is sent before its answer is
	// final: a dropped connection, a 5xx or a 429 is tried again, waiting a
	// second longer each time. A push is hundreds of requests, and one lost
	// to a transient fault should not cost the rest.
	Attempts int
}

// New is a client for the Worker at origin (checked with CheckOrigin by the
// caller), holding the token for each scope it may use. Its HTTP client has
// no overall timeout, because a 64 MiB chunk over a slow uplink takes
// minutes: the context bounds each call.
func New(origin string, tokens map[wire.Scope]string) *Client {
	return &Client{origin: strings.TrimSuffix(origin, "/"), tokens: tokens, hc: &http.Client{}, Attempts: 4}
}

// CheckOrigin refuses a Worker address that is not an origin, or that would
// send the tokens in clear: https, or http to this machine only (wrangler
// dev, the tests).
func CheckOrigin(s string) error {
	u, err := url.Parse(s)
	switch {
	case err != nil:
		return err
	case u.Host == "" || (u.Path != "" && u.Path != "/") || u.RawQuery != "":
		return fmt.Errorf("%q is not an origin such as https://irgo-windows-vm.example.workers.dev", s)
	case u.Scheme == "https":
		return nil
	case u.Scheme == "http" && (u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1" || u.Hostname() == "::1"):
		return nil
	default:
		return fmt.Errorf("%q: the tokens go only over https (or http to localhost)", s)
	}
}

// URL is the route's address on this Worker. It panics on an unknown route
// or the wrong number of params, which any test of the caller reaches.
func (c *Client) URL(route string, params ...string) string {
	return wire.MustFind(route).URL(c.origin, params...)
}

// Header is the Authorization the route's scope needs, empty for a route
// that needs none, for a caller that sends the request itself (a resumable
// download).
func (c *Client) Header(route string) http.Header {
	h := http.Header{}
	if s := wire.MustFind(route).Scope; s != wire.ScopeNone {
		h.Set("Authorization", "Bearer "+c.tokens[s])
	}
	return h
}

// Error is an answer that was not the route's success.
type Error struct {
	Route   wire.Route
	What    string // the key or target asked about, or the route's path
	Status  int
	Code    wire.Code // "" when the body was not a wire.Error
	Message string
}

func (e *Error) Error() string {
	hint := ""
	if e.Status == http.StatusUnauthorized || e.Status == http.StatusServiceUnavailable {
		if i, ok := e.Route.Scope.Info(); ok {
			hint = fmt.Sprintf(" (this needs %s, which must be the Worker's %s)", i.Env, i.Secret)
		}
	}
	return fmt.Sprintf("%s %s through the Worker: HTTP %d %s: %s%s",
		e.Route.Method, e.What, e.Status, http.StatusText(e.Status), e.Message, hint)
}

// IsCode reports whether err is an *Error with code c.
func IsCode(err error, c wire.Code) bool {
	var e *Error
	return errors.As(err, &e) && e.Code == c
}

// Do sends one request to route and returns the answer when its status is
// the route's success; otherwise the body is read into an *Error and closed.
// The caller closes a returned body.
func (c *Client) Do(ctx context.Context, route string, params []string, query url.Values, body []byte, header http.Header) (*http.Response, error) {
	r := wire.MustFind(route)
	u := r.URL(c.origin, params...)
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	what := r.Path
	if len(params) > 0 {
		what = strings.Join(params, "/")
	}
	resp, err := c.send(ctx, r, u, body, header)
	if err != nil {
		return nil, fmt.Errorf("%s %s through the Worker: %w", r.Method, what, err)
	}
	if resp.StatusCode == r.Success || containsInt(r.Also, resp.StatusCode) {
		return resp, nil
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	e := &Error{Route: r, What: what, Status: resp.StatusCode, Message: strings.TrimSpace(string(b))}
	var we wire.Error
	if json.Unmarshal(b, &we) == nil && we.Error != "" {
		e.Code, e.Message = we.Code, we.Error
	}
	if e.Message == "" {
		e.Message = "no body"
	}
	return nil, e
}

func containsInt(xs []int, x int) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

// send is one request, tried again on a dropped connection, a 5xx or a 429.
func (c *Client) send(ctx context.Context, r wire.Route, u string, body []byte, header http.Header) (*http.Response, error) {
	attempts := max(c.Attempts, 1)
	for attempt := 1; ; attempt++ {
		var rd io.Reader
		if body != nil {
			rd = bytes.NewReader(body)
		}
		req, err := http.NewRequestWithContext(ctx, r.Method, u, rd)
		if err != nil {
			return nil, err
		}
		for k, v := range header {
			req.Header[k] = v
		}
		for k, v := range c.Header(r.Name) {
			req.Header[k] = v
		}
		resp, err := c.hc.Do(req)
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

// decode reads a JSON answer of at most limit bytes into v and closes it.
func decode(resp *http.Response, limit int64, v any) error {
	defer func() { _ = resp.Body.Close() }()
	return json.NewDecoder(io.LimitReader(resp.Body, limit)).Decode(v)
}

// Health is GET /api/health.
func (c *Client) Health(ctx context.Context) (wire.Health, error) {
	var h wire.Health
	resp, err := c.Do(ctx, wire.RouteHealth, nil, nil, nil, nil)
	if err == nil {
		err = decode(resp, 4096, &h)
	}
	return h, err
}

// GoldenHead is an object's size and recorded SHA-256 ("" when none was);
// ok is false when there is no such object.
func (c *Client) GoldenHead(ctx context.Context, key string) (info wire.BlobInfo, ok bool, err error) {
	resp, err := c.Do(ctx, wire.RouteGoldenHead, []string{key}, nil, nil, nil)
	if IsNotFound(err) {
		return wire.BlobInfo{}, false, nil
	}
	if err != nil {
		return wire.BlobInfo{}, false, err
	}
	_ = resp.Body.Close() // an answer to HEAD has none
	// Not Content-Length: the runtime does not keep it on an answer to HEAD.
	n, err := strconv.ParseInt(resp.Header.Get(wire.HeaderSize), 10, 64)
	if err != nil {
		return wire.BlobInfo{}, false, fmt.Errorf("HEAD %s through the Worker: no usable %s", key, wire.HeaderSize)
	}
	return wire.BlobInfo{Key: key, Size: n, SHA256: resp.Header.Get(wire.HeaderSHA256)}, true, nil
}

// IsNotFound is err being the Worker's 404. A HEAD's 404 has no body, so it
// is told by status, not code.
func IsNotFound(err error) bool {
	var e *Error
	return errors.As(err, &e) && e.Status == http.StatusNotFound
}

// GoldenGet returns a small object (a manifest, latest), at most limit
// bytes; ok is false when it is not there. Chunks are fetched with
// GoldenFetchURL, resumably.
func (c *Client) GoldenGet(ctx context.Context, key string, limit int64) (b []byte, ok bool, err error) {
	resp, err := c.Do(ctx, wire.RouteGoldenGet, []string{key}, nil, nil, nil)
	if IsNotFound(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = resp.Body.Close() }()
	b, err = io.ReadAll(io.LimitReader(resp.Body, limit))
	return b, err == nil, err
}

// GoldenFetch is the URL and headers to download an object with a resumable
// downloader of the caller's (Range is the route's).
func (c *Client) GoldenFetch(key string) (string, http.Header) {
	return c.URL(wire.RouteGoldenGet, key), c.Header(wire.RouteGoldenGet)
}

// GoldenPut stores b at key with its SHA-256, which R2 checks behind the
// Worker.
func (c *Client) GoldenPut(ctx context.Context, key string, b []byte, sha256 string) error {
	h := http.Header{}
	h.Set(wire.HeaderSHA256, sha256)
	resp, err := c.Do(ctx, wire.RouteGoldenPut, []string{key}, nil, b, h)
	if err != nil {
		return err
	}
	_ = resp.Body.Close() // the BlobInfo echoes what was sent
	return nil
}

// GoldenDelete removes an object; nothing there is success.
func (c *Client) GoldenDelete(ctx context.Context, key string) error {
	resp, err := c.Do(ctx, wire.RouteGoldenDelete, []string{key}, nil, nil, nil)
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	return nil
}

// GoldenList returns every key of kind (a key of wire.GoldenListKinds) with
// its size, through every page.
func (c *Client) GoldenList(ctx context.Context, kind string) (map[string]int64, error) {
	if _, ok := wire.GoldenListKinds[kind]; !ok {
		return nil, fmt.Errorf("the Worker lists %s only, not %q", strings.Join(listKinds(), " and "), kind)
	}
	out := map[string]int64{}
	cursor := ""
	for {
		resp, err := c.Do(ctx, wire.RouteGoldenList, []string{kind}, url.Values{"cursor": {cursor}}, nil, nil)
		if err != nil {
			return nil, err
		}
		var page wire.GoldenList
		if err := decode(resp, 16<<20, &page); err != nil {
			return nil, fmt.Errorf("listing %s through the Worker: %w", kind, err)
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

func listKinds() []string {
	var ks []string
	for k := range wire.GoldenListKinds {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

// GlazeLatest is the newest run per target.
func (c *Client) GlazeLatest(ctx context.Context) (wire.GlazeLatest, error) {
	var l wire.GlazeLatest
	resp, err := c.Do(ctx, wire.RouteGlazeLatest, nil, nil, nil, nil)
	if err == nil {
		err = decode(resp, wire.MaxRunBytes, &l)
	}
	return l, err
}

// GlazePost records a run: its manifest (shots.json) and the pictures it
// names, by file name.
func (c *Client) GlazePost(ctx context.Context, target string, manifest []byte, pictures map[string][]byte) (wire.GlazePosted, error) {
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	part := func(name, file, ctype string, b []byte) error {
		h := textproto.MIMEHeader{}
		h.Set("Content-Disposition", fmt.Sprintf(`form-data; name=%q; filename=%q`, name, file))
		h.Set("Content-Type", ctype)
		w, err := mw.CreatePart(h)
		if err == nil {
			_, err = w.Write(b)
		}
		return err
	}
	if err := part(wire.GlazeManifestPart, wire.GlazeManifestFile, wire.TypeJSON, manifest); err != nil {
		return wire.GlazePosted{}, err
	}
	names := make([]string, 0, len(pictures))
	for n := range pictures {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		if err := part(n, n, "image/png", pictures[n]); err != nil {
			return wire.GlazePosted{}, err
		}
	}
	if err := mw.Close(); err != nil {
		return wire.GlazePosted{}, err
	}
	h := http.Header{}
	h.Set("Content-Type", mw.FormDataContentType())
	var out wire.GlazePosted
	resp, err := c.Do(ctx, wire.RouteGlazePost, []string{target}, nil, body.Bytes(), h)
	if err == nil {
		err = decode(resp, 4096, &out)
	}
	return out, err
}
