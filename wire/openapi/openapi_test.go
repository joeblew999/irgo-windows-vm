package openapi

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/joeblew999/irgo-windows-vm/wire"
)

// TestDocumentHasEveryRoute: each route is an operation at its path, with
// its scope's security and a response for every code it can answer.
//
// Negative control (by hand, 1 Oct 2026): giving every operation an empty
// security list fails this for every route with a scope; restored.
func TestDocumentHasEveryRoute(t *testing.T) {
	b, err := Document()
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Paths map[string]map[string]struct {
			OperationID string           `json:"operationId"`
			Security    []map[string]any `json:"security"`
			Responses   map[string]any   `json:"responses"`
		}
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, r := range wire.Routes {
		op, ok := doc.Paths[openAPIPath(r.Path)][strings.ToLower(r.Method)]
		if !ok || op.OperationID != r.Name {
			t.Errorf("%s %s is not in the document", r.Method, r.Path)
			continue
		}
		n++
		if r.Scope == wire.ScopeNone && len(op.Security) != 0 ||
			r.Scope != wire.ScopeNone && (len(op.Security) != 1 || op.Security[0][string(r.Scope)] == nil) {
			t.Errorf("%s: security %v, scope %s", r.Name, op.Security, r.Scope)
		}
		for _, c := range append(r.Errs(), wire.Code("")) {
			s := r.Success
			if c != "" {
				s = c.Status()
			}
			if _, ok := op.Responses[strconv.Itoa(s)]; !ok {
				t.Errorf("%s: no response %d (%s)", r.Name, s, c)
			}
		}
	}
	if n != len(wire.Routes) {
		t.Fatalf("%d of %d routes found", n, len(wire.Routes))
	}
	if again, _ := Document(); string(again) != string(b) {
		t.Error("two calls gave different documents")
	}
}
