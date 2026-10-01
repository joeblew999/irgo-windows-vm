package main

// The Worker's HTTP API, routed from the table in package wire
// (wire/routes.go): each route there has one handler here, registered by
// name in handlers, and the dispatcher enforces the route's token scope and
// body limits before the handler runs. The site itself is not here:
// Cloudflare serves site/dist as static assets before the Worker runs, and
// only /api/* reaches this handler (wrangler.toml, run_worker_first).
//
// Everything here is plain Go behind small interfaces (Env, Store, Blobs), so
// it is tested with `go test` on the host; platform_js.go binds them to R2 and
// the Worker's environment.

import (
	"bytes"
	"crypto/sha256"
	"crypto/subtle"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"strings"
	"time"

	"github.com/joeblew999/irgo-windows-vm/wire"
)

// Store is the part of an R2 bucket the API uses. Get returns ok=false for a
// key that does not exist. There is deliberately no List: nothing here lists.
type Store interface {
	Get(key string) (body []byte, contentType string, ok bool, err error)
	Put(key string, body []byte, contentType string) error
}

// Env is what the platform supplies per request.
type Env struct {
	Var    func(name string) string // vars and secrets from wrangler.toml / wrangler secret
	Site   func() (Store, error)    // the SITE bucket: glaze status, public data
	Golden func() (Blobs, error)    // the GOLDEN bucket: the private golden image
	Now    func() time.Time
}

// handlerFunc serves one route. params are the route's {name}s in order;
// the scope and the body limits were enforced before it was called.
type handlerFunc func(env Env, w http.ResponseWriter, r *http.Request, params []string)

// handlers is one handler per route in wire.Routes, by name. Handler panics
// at start when the two disagree, and TestHandlersAreTheTable says which.
var handlers = map[string]handlerFunc{
	wire.RouteHealth:       health,
	wire.RouteOpenAPI:      serveOpenAPI,
	wire.RouteGlazeLatest:  Env.glazeLatest,
	wire.RouteGlazePost:    Env.glazePost,
	wire.RouteGlazeFile:    Env.glazeFile,
	wire.RouteGoldenHead:   Env.goldenHead,
	wire.RouteGoldenGet:    Env.goldenGet,
	wire.RouteGoldenPut:    Env.goldenPut,
	wire.RouteGoldenDelete: Env.goldenDelete,
	wire.RouteGoldenList:   Env.goldenList,
}

// checkHandlers is nil when handlers has exactly the table's routes.
func checkHandlers() error {
	var missing, extra []string
	for _, r := range wire.Routes {
		if handlers[r.Name] == nil {
			missing = append(missing, r.Name)
		}
	}
	for name := range handlers {
		if _, ok := wire.Find(name); !ok {
			extra = append(extra, name)
		}
	}
	if missing != nil || extra != nil {
		return fmt.Errorf("worker: routes with no handler %v, handlers with no route %v", missing, extra)
	}
	return nil
}

// Handler is the whole API.
func Handler(env Env) http.Handler {
	if err := checkHandlers(); err != nil {
		panic(err)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		route, params, allowed, ok := wire.Match(r.Method, r.URL.Path)
		switch {
		case !ok && allowed != nil:
			w.Header().Set("Allow", strings.Join(allowed, ", "))
			fail(w, wire.CodeMethodNotAllowed, "%s %s: %s only", r.Method, r.URL.Path, strings.Join(allowed, ", "))
			return
		case !ok:
			fail(w, wire.CodeNotFound, "no such endpoint: %s %s", r.Method, r.URL.Path)
			return
		}
		if !env.authorized(w, r, route.Scope) {
			return
		}
		if route.NeedLength && r.ContentLength < 0 {
			fail(w, wire.CodeLengthRequired, "send Content-Length: R2 stores a stream only of known length")
			return
		}
		if route.MaxBody > 0 && r.ContentLength > route.MaxBody {
			fail(w, wire.CodeTooLarge, "%d bytes, over the %d this accepts", r.ContentLength, route.MaxBody)
			return
		}
		handlers[route.Name](env, w, r, params)
	})
}

// authorized answers whether the request carries the token for scope. A
// secret that is not set refuses everything: an unconfigured Worker must not
// fall open. Compared as SHA-256 digests so the comparison does not depend on
// the length of either.
func (env Env) authorized(w http.ResponseWriter, r *http.Request, scope wire.Scope) bool {
	if scope == wire.ScopeNone {
		return true
	}
	info, ok := scope.Info()
	want := ""
	if ok {
		want = env.Var(info.Secret)
	}
	if want == "" {
		fail(w, wire.CodeNotConfigured, "refused: %s is not set on this Worker", info.Secret)
		return false
	}
	got, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	g, h := sha256.Sum256([]byte(got)), sha256.Sum256([]byte(want))
	if !ok || got == "" || subtle.ConstantTimeCompare(g[:], h[:]) != 1 {
		w.Header().Set("WWW-Authenticate", `Bearer realm="irgo-windows-vm"`)
		fail(w, wire.CodeUnauthorized, "refused: no valid bearer token")
		return false
	}
	return true
}

func health(_ Env, w http.ResponseWriter, _ *http.Request, _ []string) {
	writeJSON(w, http.StatusOK, wire.Health{OK: true})
}

// openapiJSON is wire/openapi's document, generated into this file at build
// time (mise run worker:wasm) rather than at run time, so the Worker carries
// no reflection over the table. TestOpenAPIIsCurrent fails while it is stale.
//
//go:embed openapi.json
var openapiJSON []byte

func serveOpenAPI(_ Env, w http.ResponseWriter, _ *http.Request, _ []string) {
	w.Header().Set("Content-Type", wire.TypeJSON)
	w.Header().Set("Cache-Control", "public, max-age=300")
	w.Header().Set("Access-Control-Allow-Origin", "*") // for an API browser on another origin
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(openapiJSON)
}

func latestKey(target string) string { return "glaze/" + target + "/latest.json" }
func runKey(target, id, file string) string {
	return "glaze/" + target + "/runs/" + id + "/" + file
}

// manifestView is the part of glazecheck.Manifest (internal/glazecheck/shots.go,
// written as shots.json) the Worker checks. The manifest is stored as the bytes
// that were posted, never re-encoded, so this is not a second definition of it.
type manifestView struct {
	Target string
	When   time.Time
	Tests  []struct{ Picture string }
}

func (env Env) glazeLatest(w http.ResponseWriter, _ *http.Request, _ []string) {
	st, err := env.Site()
	if err != nil {
		fail(w, wire.CodeInternal, "the SITE bucket: %v", err)
		return
	}
	// The stored records as they are: each is a wire.GlazeRun.
	out := map[string]json.RawMessage{}
	for _, t := range wire.GlazeTargets {
		b, _, ok, gErr := st.Get(latestKey(t))
		switch {
		case gErr != nil:
			fail(w, wire.CodeStorage, "reading %s: %v", latestKey(t), gErr)
			return
		case !ok:
			out[t] = json.RawMessage("null")
		default:
			out[t] = b
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, out)
}

func (env Env) glazePost(w http.ResponseWriter, r *http.Request, params []string) {
	target := params[0]
	if !wire.IsGlazeTarget(target) {
		fail(w, wire.CodeNotFound, "no such target %q: %s", target, strings.Join(wire.GlazeTargets, " or "))
		return
	}
	mt, mp, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mt != wire.TypeMultipart {
		fail(w, wire.CodeUnsupportedMediaType, "send %s: a part named %s (shots.json) and one part per picture, named by its file name", wire.TypeMultipart, wire.GlazeManifestPart)
		return
	}
	manifest, pictures, err := readRun(multipart.NewReader(http.MaxBytesReader(w, r.Body, wire.MaxRunBytes), mp["boundary"]))
	if err != nil {
		fail(w, wire.CodeBadRequest, "%v", err)
		return
	}
	var m manifestView
	if err := json.Unmarshal(manifest, &m); err != nil {
		fail(w, wire.CodeBadRequest, "manifest is not shots.json: %v", err)
		return
	}
	if m.Target != target {
		fail(w, wire.CodeBadRequest, "manifest is for target %q, posted to %q", m.Target, target)
		return
	}
	if m.When.IsZero() {
		fail(w, wire.CodeBadRequest, "manifest has no When")
		return
	}
	named := map[string]bool{}
	for _, t := range m.Tests {
		if t.Picture == "" {
			continue
		}
		named[t.Picture] = true
		if _, ok := pictures[t.Picture]; !ok {
			fail(w, wire.CodeBadRequest, "manifest names %s, which was not sent", t.Picture)
			return
		}
	}
	for name := range pictures {
		if !named[name] {
			fail(w, wire.CodeBadRequest, "%s was sent but the manifest does not name it", name)
			return
		}
	}

	st, err := env.Site()
	if err != nil {
		fail(w, wire.CodeInternal, "the SITE bucket: %v", err)
		return
	}
	// An older run must not replace a newer one: a re-run of an old workflow
	// would otherwise roll the page back. A current record that cannot be read
	// is cannot tell, and refuses.
	if b, _, ok, gErr := st.Get(latestKey(target)); gErr != nil {
		fail(w, wire.CodeStorage, "reading %s: %v", latestKey(target), gErr)
		return
	} else if ok {
		var cur wire.GlazeRun
		var cm manifestView
		if jErr := json.Unmarshal(b, &cur); jErr != nil || json.Unmarshal(cur.Manifest, &cm) != nil {
			fail(w, wire.CodeConflict, "cannot tell whether this run is newer: %s does not parse; delete it to start again", latestKey(target))
			return
		}
		if m.When.Before(cm.When) {
			fail(w, wire.CodeConflict, "refused: this run (%s) is older than the stored one (%s)", m.When.Format(time.RFC3339), cm.When.Format(time.RFC3339))
			return
		}
	}

	sum := sha256.Sum256(manifest)
	id := hex.EncodeToString(sum[:8])
	for name, b := range pictures {
		if err := st.Put(runKey(target, id, name), b, "image/png"); err != nil {
			fail(w, wire.CodeStorage, "storing %s: %v", name, err)
			return
		}
	}
	if err := st.Put(runKey(target, id, wire.GlazeManifestFile), manifest, wire.TypeJSON); err != nil {
		fail(w, wire.CodeStorage, "storing %s: %v", wire.GlazeManifestFile, err)
		return
	}
	rec, err := json.Marshal(wire.GlazeRun{
		Target: target, Run: id, Received: env.Now().UTC(),
		Base:     wire.MustFind(wire.RouteGlazeFile).URL("", target, id, ""),
		Manifest: manifest,
	})
	if err != nil {
		fail(w, wire.CodeInternal, "%v", err)
		return
	}
	if err := st.Put(latestKey(target), rec, wire.TypeJSON); err != nil {
		fail(w, wire.CodeStorage, "storing %s: %v", latestKey(target), err)
		return
	}
	// Success only once the record reads back as written.
	back, _, ok, err := st.Get(latestKey(target))
	if err != nil || !ok || !bytes.Equal(back, rec) {
		fail(w, wire.CodeStorage, "%s did not read back as written (err=%v, found=%v)", latestKey(target), err, ok)
		return
	}
	writeJSON(w, http.StatusCreated, wire.GlazePosted{Target: target, Run: id, Pictures: len(pictures)})
}

var pngMagic = []byte("\x89PNG\r\n\x1a\n")

// readRun reads the posted parts: exactly one manifest, and pictures that are
// PNGs with safe names, each at most once.
func readRun(mr *multipart.Reader) (manifest []byte, pictures map[string][]byte, err error) {
	pictures = map[string][]byte{}
	for {
		p, pErr := mr.NextPart()
		if errors.Is(pErr, io.EOF) {
			break
		}
		if pErr != nil {
			return nil, nil, fmt.Errorf("reading the form: %v", pErr)
		}
		name := p.FormName()
		b, rErr := io.ReadAll(io.LimitReader(p, wire.MaxPictureBytes+1))
		if rErr != nil {
			return nil, nil, fmt.Errorf("reading part %q: %v", name, rErr)
		}
		if len(b) > wire.MaxPictureBytes {
			return nil, nil, fmt.Errorf("part %q is over %d bytes", name, wire.MaxPictureBytes)
		}
		switch {
		case name == wire.GlazeManifestPart:
			if manifest != nil {
				return nil, nil, errors.New("manifest sent twice")
			}
			manifest = b
		case wire.IsPicture(name):
			if _, dup := pictures[name]; dup {
				return nil, nil, fmt.Errorf("%s sent twice", name)
			}
			if !bytes.HasPrefix(b, pngMagic) {
				return nil, nil, fmt.Errorf("%s is not a PNG", name)
			}
			if len(pictures) == wire.MaxPictures {
				return nil, nil, fmt.Errorf("more than %d pictures", wire.MaxPictures)
			}
			pictures[name] = b
		default:
			return nil, nil, fmt.Errorf("unexpected part %q: only %s and <name>.png", name, wire.GlazeManifestPart)
		}
	}
	if manifest == nil {
		return nil, nil, fmt.Errorf("no part named %s", wire.GlazeManifestPart)
	}
	return manifest, pictures, nil
}

func (env Env) glazeFile(w http.ResponseWriter, _ *http.Request, params []string) {
	target, id, file := params[0], params[1], params[2]
	if !wire.IsGlazeTarget(target) || !wire.IsRunID(id) || (file != wire.GlazeManifestFile && !wire.IsPicture(file)) {
		fail(w, wire.CodeNotFound, "no such file")
		return
	}
	st, err := env.Site()
	if err != nil {
		fail(w, wire.CodeInternal, "the SITE bucket: %v", err)
		return
	}
	b, ct, ok, err := st.Get(runKey(target, id, file))
	switch {
	case err != nil:
		fail(w, wire.CodeStorage, "reading %s: %v", file, err)
	case !ok:
		fail(w, wire.CodeNotFound, "no such file")
	default:
		if ct == "" {
			ct = wire.TypeOctets
		}
		w.Header().Set("Content-Type", ct)
		// A run's files never change: its id is the hash of its manifest.
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		_, _ = w.Write(b)
	}
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", wire.TypeJSON)
	w.WriteHeader(code)
	_, _ = w.Write(append(b, '\n'))
}

// fail answers with code's status and a wire.Error.
func fail(w http.ResponseWriter, code wire.Code, format string, a ...any) {
	writeJSON(w, code.Status(), wire.Error{Error: fmt.Sprintf(format, a...), Code: code})
}
