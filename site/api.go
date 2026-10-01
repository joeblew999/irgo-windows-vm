package main

// The Worker API page, generated from wire's route table: the same table the
// Worker routes by, the clients build their URLs from, and the Worker's
// /api/openapi.json is generated from. Nothing here names a route, a token or
// a status; a wrong one on the page is a wrong entry in wire/routes.go.
//
// Imported rather than captured, unlike the command and MCP pages: package
// wire is the standard library only (its TestStandardLibraryOnly), so it
// brings nothing into this module's build.

import (
	"fmt"
	"net/http"
	"reflect"
	"strconv"
	"strings"

	"github.com/joeblew999/irgo-windows-vm/wire"
)

// generateAPI renders the route table as markdown.
func generateAPI() []byte {
	var b strings.Builder
	b.WriteString("# Worker API\n\n")
	b.WriteString("Every endpoint of the Cloudflare Worker (`worker/`), generated from the route table in\n")
	b.WriteString("`wire/routes.go` each time the site is built. The Worker routes by that table and enforces\n")
	b.WriteString("each route's token and size limit from it, and `irgo-winvm` builds every request from it,\n")
	b.WriteString("so this page, the Worker and its clients cannot disagree. The same table, as OpenAPI 3.1,\n")
	b.WriteString("is served by the Worker at `" + wire.MustFind(wire.RouteOpenAPI).Path + "`.\n\n")
	b.WriteString("How the Worker is built and deployed, and how to add a route, is in\n")
	b.WriteString("[Development](development.html#the-worker-api).\n\n")

	b.WriteString("## Routes\n\n| route | method and path | token | success |\n|---|---|---|---|\n")
	for _, r := range wire.Routes {
		fmt.Fprintf(&b, "| [`%s`](#%s) | `%s %s` | %s | %s |\n", r.Name, r.Name, r.Method, r.Path, scopeCell(r.Scope), statuses(r))
	}

	b.WriteString("\n## Tokens\n\nEach token does one job, and none does another's. A request without the right one is\n")
	b.WriteString("refused whatever the path, and a Worker whose secret is not set refuses every route of that\n")
	b.WriteString("scope rather than falling open.\n\n| scope | Worker secret | client variable | for |\n|---|---|---|---|\n")
	for _, s := range wire.Scopes {
		fmt.Fprintf(&b, "| `%s` | `%s` | `%s` | %s |\n", s.Scope, s.Secret, s.Env, s.Summary)
	}

	b.WriteString("\n## Errors\n\nEvery error answer is JSON, `{\"error\": <message>, \"code\": <code>}`. Match on the code,\nnever on the message.\n\n| code | status | means |\n|---|---|---|\n")
	for _, c := range wire.Codes {
		fmt.Fprintf(&b, "| `%s` | %d | %s |\n", c.Code, c.Status, c.Summary)
	}

	b.WriteString("\n## Every route\n\n")
	for _, r := range wire.Routes {
		fmt.Fprintf(&b, "### %s\n\n`%s %s`. %s.\n\n", r.Name, r.Method, r.Path, capital(r.Summary))
		fmt.Fprintf(&b, "- **Token:** %s\n", scopeCell(r.Scope))
		for _, p := range append(append([]wire.Param{}, r.Params...), r.Headers...) {
			fmt.Fprintf(&b, "- **`%s`** (%s): %s\n", p.Name, p.In, p.Summary)
		}
		if r.RequestType != "" {
			fmt.Fprintf(&b, "- **Body:** `%s`, at most %s", r.RequestType, size(r.MaxBody))
			if r.NeedLength {
				b.WriteString(", with Content-Length")
			}
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, "- **Answers:** %s", statuses(r))
		if r.ResponseType != "" {
			fmt.Fprintf(&b, ", `%s`", r.ResponseType)
		}
		if r.Response != nil {
			fmt.Fprintf(&b, " (`wire.%s`)", reflect.TypeOf(r.Response).Name())
		}
		b.WriteString("\n")
		for _, h := range r.ResponseHeaders {
			fmt.Fprintf(&b, "- **`%s`** in the answer: %s\n", h.Name, h.Summary)
		}
		var errs []string
		for _, c := range r.Errs() {
			errs = append(errs, fmt.Sprintf("`%s` (%d)", c, c.Status()))
		}
		if errs != nil {
			fmt.Fprintf(&b, "- **Errors:** %s\n", strings.Join(errs, ", "))
		}
		if len(r.Commands) > 0 {
			fmt.Fprintf(&b, "- **Called by:** `irgo-winvm %s`\n", strings.Join(r.Commands, "`, `irgo-winvm "))
		}
		b.WriteString("\n")
	}
	return []byte(b.String())
}

func scopeCell(s wire.Scope) string {
	i, ok := s.Info()
	if !ok {
		return "none"
	}
	return "`" + i.Secret + "`"
}

func statuses(r wire.Route) string {
	out := []string{strconv.Itoa(r.Success) + " " + http.StatusText(r.Success)}
	for _, s := range r.Also {
		out = append(out, strconv.Itoa(s)+" "+http.StatusText(s))
	}
	return strings.Join(out, ", ")
}

func size(n int64) string {
	if n >= 1<<20 && n%(1<<20) == 0 {
		return strconv.FormatInt(n>>20, 10) + " MiB"
	}
	return strconv.FormatInt(n, 10) + " bytes"
}

func capital(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
