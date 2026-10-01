package main

// The ledger's page, GET /api/ledger/. Rendered here, not a static page of
// the site: Cloudflare serves static assets before the Worker runs and to
// anyone, so a page that must stay private cannot be one. It is the same
// view as /api/ledger/vms, as HTML, with the site's stylesheet.
//
// Built with html.EscapeString rather than html/template, which leans on
// reflection TinyGo is the wrong place to discover gaps in; every value from
// the database goes through esc.

import (
	"fmt"
	"html"
	"net/http"
	"strings"
	"time"
)

func esc(s string) string { return html.EscapeString(s) }

func when(ms int64) string {
	if ms == 0 {
		return ""
	}
	return time.UnixMilli(ms).UTC().Format("2006-01-02 15:04:05Z")
}

func age(sec int64) string {
	d := time.Duration(sec) * time.Second
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", sec)
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%.1fh", d.Hours())
	}
	return fmt.Sprintf("%.1fd", d.Hours()/24)
}

func (env Env) ledgerPage(w http.ResponseWriter, r *http.Request) {
	v, code, err := env.view(r)
	if err != nil {
		fail(w, code, "%v", err)
		return
	}
	var b strings.Builder
	row := func(cells ...string) {
		b.WriteString("<tr>")
		for _, c := range cells {
			b.WriteString("<td>" + c + "</td>")
		}
		b.WriteString("</tr>\n")
	}
	head := func(cells ...string) {
		b.WriteString("<table><thead><tr>")
		for _, c := range cells {
			b.WriteString("<th>" + c + "</th>")
		}
		b.WriteString("</tr></thead><tbody>\n")
	}
	b.WriteString(`<!doctype html><html lang="en"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="robots" content="noindex">
<title>Ledger — irgo-windows-vm</title>
<link rel="stylesheet" href="/style.css">
<style>
.ledger{max-width:none;padding:1.5rem 1rem 3rem}
.ledger table{font-size:.85rem;display:block;overflow-x:auto}
.state{font-weight:600;white-space:nowrap}
.state-in-use{color:var(--tip)} .state-stale{color:var(--caution)}
.state-idle{color:var(--muted)} .state-deleted{color:var(--muted);text-decoration:line-through}
</style></head><body><main class="prose ledger">
`)
	fmt.Fprintf(&b, "<h1>Ledger</h1>\n<p>Who used which VM, on which machine, doing what. Events since %s, at %s; open work with no expiry is stale after %s.%s The local lock files on each machine are the authority; this is the record.</p>\n",
		esc(when(v.Since)), esc(when(v.Now)), esc(v.StaleAfter),
		map[bool]string{true: " <strong>Truncated: more events than the view reads; narrow ?since=.</strong>", false: ""}[v.Truncated])

	b.WriteString("<h2>VMs</h2>\n")
	if len(v.VMs) == 0 {
		b.WriteString("<p>No VM in the window.</p>\n")
	} else {
		head("state", "VM", "machine", "owner", "client", "repo", "last", "when", "created", "open")
		for _, m := range v.VMs {
			row(`<span class="state state-`+esc(m.State)+`">`+esc(m.State)+`</span>`, esc(m.VM), esc(m.Host)+" <small>"+esc(m.Machine)+"</small>",
				esc(m.Owner), esc(m.Client), esc(m.Repo), esc(m.LastCommand)+" <small>"+esc(m.LastType)+"</small>",
				esc(when(m.LastActivity)), esc(when(m.Created)), fmt.Sprint(m.Open))
		}
		b.WriteString("</tbody></table>\n")
	}

	b.WriteString("<h2>Open now</h2>\n")
	if len(v.Open) == 0 {
		b.WriteString("<p>Nothing started and not ended, nothing leased and not released.</p>\n")
	} else {
		head("", "what", "VM", "machine", "owner", "client", "repo", "since", "age")
		for _, o := range v.Open {
			flag := `<span class="state state-in-use">running</span>`
			if o.Stale {
				flag = `<span class="state state-stale">stale</span> <small>` + esc(o.Why) + `</small>`
			}
			row(flag, esc(o.Command)+" <small>"+esc(o.Type)+"</small>", esc(o.VM), esc(o.Host), esc(o.Owner),
				esc(o.Client), esc(o.Repo), esc(when(o.TS)), esc(age(o.AgeSeconds)))
		}
		b.WriteString("</tbody></table>\n")
	}

	b.WriteString("<h2>Machines</h2>\n")
	head("host", "machine", "last seen", "last owner", "last command", "version")
	for _, m := range v.Machines {
		row(esc(m.Host), "<small>"+esc(m.Machine)+"</small>", esc(when(m.LastSeen)), esc(m.LastOwner), esc(m.LastCommand), esc(m.Version))
	}
	b.WriteString("</tbody></table>\n")

	b.WriteString("<h2>Recent events</h2>\n")
	head("when", "type", "command", "VM", "host", "owner", "client", "exit", "took", "detail")
	for _, e := range v.Recent {
		exit, took := "", ""
		if e.Exit != nil {
			exit = fmt.Sprint(*e.Exit)
		}
		if e.DurationMS != nil {
			took = (time.Duration(*e.DurationMS) * time.Millisecond).Round(time.Millisecond).String()
		}
		row(esc(when(e.TS)), esc(e.Type), esc(e.Command), esc(e.VM), esc(e.Host), esc(e.Owner), esc(e.Client), esc(exit), esc(took), esc(e.Detail))
	}
	b.WriteString("</tbody></table>\n<p><small>JSON: <code>/api/ledger/vms</code>, <code>/api/ledger/events?owner=&amp;vm=&amp;machine=&amp;since=24h</code>.</small></p>\n</main></body></html>\n")

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Robots-Tag", "noindex")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'self' 'unsafe-inline'; base-uri 'none'; frame-ancestors 'none'")
	_, _ = w.Write([]byte(b.String()))
}
