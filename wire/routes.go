// Package wire is the Worker's HTTP API declared once: every route, the token
// each needs, what goes in and comes out, the status codes, the size limits,
// the shared key patterns and headers, and the error codes. The Worker
// (worker/) registers one handler per route by name and enforces the scope
// and the limit from here; the clients (internal/workerclient) build every
// URL and pick every token from here; the site's API page and the Worker's
// /api/openapi.json are generated from here. A route that is not in Routes
// does not exist.
//
// It imports the standard library only, and nothing the TinyGo build of the
// Worker cannot take: no regexps (one compiled at package level overflows
// TinyGo's Wasm stack before main) and no reflection. TestStandardLibraryOnly
// keeps it that way; `mise run worker:wasm` is what proves TinyGo takes it.
//
// It is a package of the root module rather than a module of its own: a
// replace directive in the root go.mod would break the documented
// `go install …/cmd/irgo-winvm@latest`, which refuses a module with one.
// worker/ and site/ reach it through a replace in their own go.mod, which
// nothing installs.
package wire

import (
	"net/http"
	"net/url"
	"strings"
)

// Prefix is where the API lives. Cloudflare serves the site's static files
// for every other path, and sends only these to the Worker
// (worker/wrangler.toml, run_worker_first).
const Prefix = "/api/"

// APIVersion is the OpenAPI document's info.version. Bump it when a route
// changes in a way an existing client would notice.
const APIVersion = "1"

// Route is one endpoint.
type Route struct {
	// Name is how the Worker registers its handler and a client calls it.
	Name   string
	Method string
	// Path is the pattern: literal segments, {name} for one segment, and a
	// final {name...} for the rest of the path, slashes included.
	Path    string
	Scope   Scope
	Summary string
	// Params describes each {name} in Path, in order, and any query parameter.
	Params []Param

	// Request is the zero value of the body's Go type, nil when the body is
	// not JSON (RequestType says what it is) or there is none.
	Request     any
	RequestType string // the body's media type; "" for no body
	// MaxBody is the largest body accepted, in bytes; 0 for no body. The
	// Worker refuses a larger Content-Length with CodeTooLarge before the
	// handler runs.
	MaxBody int64
	// NeedLength is a body that must declare Content-Length (CodeLengthRequired).
	NeedLength bool
	// Headers are request headers the route reads.
	Headers []Param

	// Success is the status of a successful answer; Also lists other
	// successful ones (206 for a range).
	Success int
	Also    []int
	// Response is the zero value of a JSON answer's Go type, nil when the
	// answer is not JSON (ResponseType says what it is) or has no body.
	Response     any
	ResponseType string
	// ResponseHeaders are headers a successful answer carries.
	ResponseHeaders []Param
	// Errors are the codes the handler itself answers with. The ones every
	// route of its kind can get (CodeUnauthorized and CodeNotConfigured for a
	// route with a scope, CodeTooLarge and CodeLengthRequired from MaxBody and
	// NeedLength) are added by Errs, not listed here.
	Errors []Code

	// Commands are the irgo-winvm commands that call this route, so their MCP
	// tools can name it (internal/mcpserver) instead of restating it.
	Commands []string
}

// Param is a path, query or header parameter.
type Param struct {
	Name, In, Summary string // In is "path", "query" or "header"
}

// Route names, for the Worker's handler table and the client's calls.
const (
	RouteHealth       = "health"
	RouteOpenAPI      = "openapi"
	RouteGlazeLatest  = "glaze-latest"
	RouteGlazePost    = "glaze-post"
	RouteGlazeFile    = "glaze-file"
	RouteGoldenHead   = "golden-head"
	RouteGoldenGet    = "golden-get"
	RouteGoldenPut    = "golden-put"
	RouteGoldenDelete = "golden-delete"
	RouteGoldenList   = "golden-list"
)

var goldenKeyParam = Param{"key", "path", "golden/latest, golden/manifests/<sha256>.json or golden/chunks/<sha256>.zst; anything else is 404"}

// Routes is every endpoint the Worker serves, in the order the API page and
// the OpenAPI document list them.
var Routes = []Route{
	{
		Name: RouteHealth, Method: http.MethodGet, Path: "/api/health", Scope: ScopeNone,
		Summary: "the Worker is up; reads no secret and no bucket",
		Success: http.StatusOK, Response: Health{}, ResponseType: TypeJSON,
	},
	{
		Name: RouteOpenAPI, Method: http.MethodGet, Path: "/api/openapi.json", Scope: ScopeNone,
		Summary: "this API as an OpenAPI 3.1 document, generated from the same table as the Worker's routing",
		Success: http.StatusOK, ResponseType: TypeJSON,
	},
	{
		Name: RouteGlazeLatest, Method: http.MethodGet, Path: "/api/glaze-status", Scope: ScopeNone,
		Summary: "the newest glaze run per target (null for a target with none), read by the site's Glaze status page",
		Success: http.StatusOK, Response: GlazeLatest{}, ResponseType: TypeJSON,
		Errors: []Code{CodeStorage, CodeInternal},
	},
	{
		Name: RouteGlazePost, Method: http.MethodPost, Path: "/api/glaze-status/{target}", Scope: ScopeGlazeWrite,
		Summary: "record a run: its shots.json and every picture it names, as CI's conformance job sends them. " +
			"Refused when older than the stored run, so a re-run of an old workflow cannot roll the page back",
		Params:      []Param{{"target", "path", "mac or windows; must match the manifest's Target"}},
		RequestType: TypeMultipart, MaxBody: MaxRunBytes,
		Success: http.StatusCreated, Response: GlazePosted{}, ResponseType: TypeJSON,
		Errors:   []Code{CodeNotFound, CodeUnsupportedMediaType, CodeBadRequest, CodeConflict, CodeStorage, CodeInternal},
		Commands: []string{"glaze-check"},
	},
	{
		Name: RouteGlazeFile, Method: http.MethodGet, Path: "/api/glaze-status/{target}/runs/{run}/{file}", Scope: ScopeNone,
		Summary: "one stored file of a run, served immutable: a run's id is the hash of its manifest",
		Params: []Param{
			{"target", "path", "mac or windows"},
			{"run", "path", "the run's id: the first 8 bytes of its manifest's SHA-256, 16 hex digits"},
			{"file", "path", "shots.json or a picture the manifest names"},
		},
		Success: http.StatusOK, ResponseType: "image/png or application/json",
		Errors: []Code{CodeNotFound, CodeStorage, CodeInternal},
	},
	{
		Name: RouteGoldenHead, Method: http.MethodHead, Path: "/api/golden/{key...}", Scope: ScopeGoldenRead,
		Summary: "an object's size and SHA-256, without its bytes; 404 with no body when it is not there",
		Params:  []Param{goldenKeyParam},
		Success: http.StatusOK,
		ResponseHeaders: []Param{
			{HeaderSize, "header", "the whole object's size (Workers drops Content-Length from an answer to HEAD)"},
			{HeaderSHA256, "header", "its SHA-256 as verified when it was stored; absent for one stored another way"},
		},
		Errors:   []Code{CodeNotFound, CodeStorage, CodeInternal},
		Commands: []string{"vm-golden-pull", "vm-golden-push", "vm-create"},
	},
	{
		Name: RouteGoldenGet, Method: http.MethodGet, Path: "/api/golden/{key...}", Scope: ScopeGoldenRead,
		Summary: "the object, streamed from R2 without passing through Go; a single byte Range answers 206, which is how a pull resumes",
		Params:  []Param{goldenKeyParam},
		Headers: []Param{{"Range", "header", "optional, one range: bytes=<first>-[<last>] or bytes=-<n>"}},
		Success: http.StatusOK, Also: []int{http.StatusPartialContent}, ResponseType: TypeOctets,
		ResponseHeaders: []Param{
			{HeaderSize, "header", "the whole object's size"},
			{HeaderSHA256, "header", "its SHA-256 as verified when it was stored"},
		},
		Errors:   []Code{CodeNotFound, CodeRangeNotSatisfiable, CodeConflict, CodeStorage, CodeInternal},
		Commands: []string{"vm-golden-pull", "vm-golden-push", "vm-create"},
	},
	{
		Name: RouteGoldenPut, Method: http.MethodPut, Path: "/api/golden/{key...}", Scope: ScopeGoldenWrite,
		Summary:     "store an object, only if it hashes to " + HeaderSHA256 + " (R2 checks it as the stream arrives); a manifest's claim must also be its name",
		Params:      []Param{goldenKeyParam},
		Headers:     []Param{{HeaderSHA256, "header", "required: the body's SHA-256, 64 lower-case hex digits"}},
		RequestType: TypeOctets, MaxBody: MaxGoldenPut, NeedLength: true,
		Success: http.StatusCreated, Response: BlobInfo{}, ResponseType: TypeJSON,
		Errors:   []Code{CodeNotFound, CodeBadRequest, CodeDigestMismatch, CodeStorage, CodeInternal},
		Commands: []string{"vm-golden-push"},
	},
	{
		Name: RouteGoldenDelete, Method: http.MethodDelete, Path: "/api/golden/{key...}", Scope: ScopeGoldenWrite,
		Summary:  "remove an object; nothing there is success too",
		Params:   []Param{goldenKeyParam},
		Success:  http.StatusNoContent,
		Errors:   []Code{CodeNotFound, CodeStorage, CodeInternal},
		Commands: []string{"vm-golden-push"},
	},
	{
		Name: RouteGoldenList, Method: http.MethodGet, Path: "/api/golden-list/{kind}", Scope: ScopeGoldenWrite,
		Summary: "one page of the manifests' or the chunks' keys and sizes, for vm-golden-push -delete, which must find every chunk no manifest names. " +
			"The write token, so a machine that only pulls cannot enumerate the bucket",
		Params: []Param{
			{"kind", "path", "manifests or chunks"},
			{"cursor", "query", "the previous page's cursor; empty for the first page"},
		},
		Success: http.StatusOK, Response: GoldenList{}, ResponseType: TypeJSON,
		Errors:   []Code{CodeNotFound, CodeStorage, CodeInternal},
		Commands: []string{"vm-golden-push"},
	},
}

// Media types the table uses.
const (
	TypeJSON      = "application/json"
	TypeOctets    = "application/octet-stream"
	TypeMultipart = "multipart/form-data"
)

// Find returns the route with the given name.
func Find(name string) (Route, bool) {
	for _, r := range Routes {
		if r.Name == name {
			return r, true
		}
	}
	return Route{}, false
}

// MustFind is Find for a name written in code: a misspelt one panics on the
// first call, which every test reaches.
func MustFind(name string) Route {
	r, ok := Find(name)
	if !ok {
		panic("wire: no route named " + name)
	}
	return r
}

// Errs is every error code the route can answer with: its own, plus those
// its scope and its body limits bring.
func (r Route) Errs() []Code {
	var out []Code
	if r.Scope != ScopeNone {
		out = append(out, CodeUnauthorized, CodeNotConfigured)
	}
	if r.NeedLength {
		out = append(out, CodeLengthRequired)
	}
	if r.MaxBody > 0 {
		out = append(out, CodeTooLarge)
	}
	for _, c := range r.Errors {
		if !hasCode(out, c) {
			out = append(out, c)
		}
	}
	return out
}

func hasCode(cs []Code, c Code) bool {
	for _, x := range cs {
		if x == c {
			return true
		}
	}
	return false
}

// Match finds the route for a request. ok is false when no route has this
// path, and allowed is then empty; when routes have the path but none this
// method, ok is false and allowed lists their methods (answer 405). params
// are the path's {name} values in order, unescaped as r.URL.Path is.
//
// Matched by hand, not by net/http's "GET /x/{y}" patterns, which never
// match under TinyGo 0.42 (docs/DEVELOPMENT.md, "Go or TinyGo").
func Match(method, path string) (route Route, params []string, allowed []string, ok bool) {
	for _, r := range Routes {
		p, hit := matchPath(r.Path, path)
		if !hit {
			continue
		}
		if r.Method == method {
			return r, p, nil, true
		}
		allowed = append(allowed, r.Method)
	}
	return Route{}, nil, allowed, false
}

// matchPath matches path against pattern. A {name} matches one segment, an
// empty one included, so a handler (behind its scope) decides what is not
// there; a final {name...} matches the rest, slashes and nothing included.
func matchPath(pattern, path string) ([]string, bool) {
	ps, xs := strings.Split(pattern, "/"), strings.Split(path, "/")
	var params []string
	for i, seg := range ps {
		if i >= len(xs) { // "/api/golden" for "/api/golden/{key...}" too
			return nil, false
		}
		if strings.HasPrefix(seg, "{") && strings.HasSuffix(seg, "...}") {
			return append(params, strings.Join(xs[i:], "/")), true
		}
		if strings.HasPrefix(seg, "{") && strings.HasSuffix(seg, "}") {
			params = append(params, xs[i])
			continue
		}
		if seg != xs[i] {
			return nil, false
		}
	}
	if len(xs) != len(ps) {
		return nil, false
	}
	return params, true
}

// URL is the route's address under origin (https://host, no trailing
// slash, or "" for a path alone), with params put into its {name}s in order.
// Each is escaped; a {name...} keeps its slashes. It panics when the count is
// wrong, which is a bug in the caller that any test of it reaches.
func (r Route) URL(origin string, params ...string) string {
	var b strings.Builder
	b.WriteString(origin)
	n := 0
	for i, seg := range strings.Split(r.Path, "/") {
		if i > 0 {
			b.WriteByte('/')
		}
		switch {
		case strings.HasPrefix(seg, "{") && strings.HasSuffix(seg, "...}"):
			parts := strings.Split(param(r, params, n), "/")
			for j, s := range parts {
				parts[j] = url.PathEscape(s)
			}
			b.WriteString(strings.Join(parts, "/"))
			n++
		case strings.HasPrefix(seg, "{") && strings.HasSuffix(seg, "}"):
			b.WriteString(url.PathEscape(param(r, params, n)))
			n++
		default:
			b.WriteString(seg)
		}
	}
	if n != len(params) {
		panic("wire: " + r.Name + " takes fewer parameters than it was given")
	}
	return b.String()
}

func param(r Route, params []string, i int) string {
	if i >= len(params) {
		panic("wire: " + r.Name + " takes more parameters than it was given")
	}
	return params[i]
}
