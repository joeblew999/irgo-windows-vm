package main

import (
	"strings"
	"testing"
)

const repo = "https://github.com/joeblew999/irgo-windows-vm"

// TestRewriteLinks covers each destination a link can have, and the case that
// was shipped broken.
//
// Every external link on the published site pointed at
// ".../blob/main/https://..." because the pattern excluded ":" only as a
// target's first character, and in "https://x" the colon is the sixth. Local
// links were fine, the pages rendered, and the link checker passed — it only
// inspects local hrefs, and a doubled URL is still a valid absolute one.
//
// Negative control, run by hand when this was written: making hasScheme return
// false unconditionally fails the four "left alone" cases; making it return
// true unconditionally fails the four rewrite cases.
func TestRewriteLinks(t *testing.T) {
	for _, tc := range []struct {
		name, in, want string
	}{
		{
			name: "markdown file that becomes a page",
			in:   "see [USING.md](docs/USING.md) first",
			want: "see [USING.md](using.html) first",
		},
		{
			name: "page link keeps its anchor",
			in:   "[the exit codes](docs/USING.md#what-it-exits-with)",
			want: "[the exit codes](using.html#what-it-exits-with)",
		},
		{
			name: "generated page name is left alone",
			in:   "[MCP](mcp.html)",
			want: "[MCP](mcp.html)",
		},
		{
			name: "screenshot points at the published copy",
			in:   "![shot](docs/screens/windows-desktop-running.png)",
			want: "![shot](screens/windows-desktop-running.png)",
		},
		{
			name: "other repository file points into the repository",
			in:   "[MIT](LICENSE)",
			want: "[MIT](" + repo + "/blob/main/LICENSE)",
		},
		{
			name: "https is left alone",
			in:   "[releases](https://github.com/joeblew999/irgo-windows-vm/releases/latest)",
			want: "[releases](https://github.com/joeblew999/irgo-windows-vm/releases/latest)",
		},
		{
			name: "http is left alone",
			in:   "[mise](http://mise.jdx.dev)",
			want: "[mise](http://mise.jdx.dev)",
		},
		{
			name: "mailto is left alone",
			in:   "[mail](mailto:someone@example.com)",
			want: "[mail](mailto:someone@example.com)",
		},
		{
			name: "bare anchor is left alone",
			in:   "[evidence](#evidence)",
			want: "[evidence](#evidence)",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := string(rewriteLinks([]byte(tc.in), repo, "README.md"))
			if got != tc.want {
				t.Errorf("rewriteLinks:\n  in:   %s\n  got:  %s\n  want: %s", tc.in, got, tc.want)
			}
		})
	}
}

// TestRewriteLinksNeverDoublesAScheme is the shipped bug stated as a property,
// so it cannot come back in some other form: no output may ever contain a
// scheme after the first character.
// TestRewriteLinksResolvesFromTheSourceFile: a link inside docs/ is relative to
// docs/, as GitHub reads it. Negative control, run by hand: dropping the
// path.Join against the source's directory fails every case here.
func TestRewriteLinksResolvesFromTheSourceFile(t *testing.T) {
	for in, want := range map[string]string{
		"[r](RESULTS.md)":            "[r](results.html)",
		"[home](../README.md)":       "[home](index.html)",
		"![s](screens/vm/ready.png)": "![s](screens/vm/ready.png)",
		"[l](../LICENSE)":            "[l](" + repo + "/blob/main/LICENSE)",
	} {
		if got := string(rewriteLinks([]byte(in), repo, "docs/UPSTREAM.md")); got != want {
			t.Errorf("from docs/UPSTREAM.md, %s: got %s, want %s", in, got, want)
		}
	}
}

func TestRewriteLinksNeverDoublesAScheme(t *testing.T) {
	in := []byte("[a](https://example.com) [b](http://x.dev/y) [c](LICENSE) [d](docs/USING.md)")
	got := string(rewriteLinks(in, repo, "README.md"))
	for _, bad := range []string{"main/https://", "main/http://", "main/mailto:"} {
		if strings.Contains(got, bad) {
			t.Errorf("an absolute URL was rewritten as a repository path (%q): %s", bad, got)
		}
	}
}
