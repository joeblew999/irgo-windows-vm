package wire

// Scope is which token a route needs. Each token does one job and none does
// another's: the read token cannot write, the write token cannot read, and
// neither posts a glaze run.
type Scope string

const (
	ScopeNone        Scope = "none"
	ScopeGlazeWrite  Scope = "glaze-status-write"
	ScopeGoldenRead  Scope = "golden-read"
	ScopeGoldenWrite Scope = "golden-write"
)

// ScopeInfo says where a scope's token lives on each side.
type ScopeInfo struct {
	Scope Scope
	// Secret is the Worker's secret holding the token (wrangler secret put).
	// While it is not set, every route of the scope answers CodeNotConfigured:
	// an unconfigured Worker does not fall open.
	Secret string
	// Env is the variable a client reads it from.
	Env     string
	Summary string
}

// Scopes is every scope that needs a token, in the order the docs list them.
var Scopes = []ScopeInfo{
	{ScopeGlazeWrite, "GLAZE_STATUS_TOKEN", "GLAZE_STATUS_TOKEN", "CI's conformance job posting a glaze run"},
	{ScopeGoldenRead, "GOLDEN_TOKEN", "IRGO_GOLDEN_TOKEN", "reading the golden image: every machine that pulls"},
	{ScopeGoldenWrite, "GOLDEN_PUSH_TOKEN", "IRGO_GOLDEN_PUSH_TOKEN", "writing, deleting and listing it: only where you push"},
}

// Info returns the scope's entry in Scopes; ok is false for ScopeNone.
func (s Scope) Info() (ScopeInfo, bool) {
	for _, i := range Scopes {
		if i.Scope == s {
			return i, true
		}
	}
	return ScopeInfo{}, false
}

// Origin variables: where a client finds the Worker. Two, because CI's glaze
// post and the golden cache were configured separately and both are set in
// places this repository does not control (a repository variable, .env.r2).
const (
	EnvGlazeURL  = "GLAZE_STATUS_URL"
	EnvGoldenURL = "IRGO_GOLDEN_URL"
)
