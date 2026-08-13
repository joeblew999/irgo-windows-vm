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
			in:   "see [AGENTS.md](AGENTS.md) first",
			want: "see [AGENTS.md](agents.html) first",
		},
		{
			name: "page link keeps its anchor",
			in:   "[the traps](AGENTS.md#things-that-cost-hours)",
			want: "[the traps](agents.html#things-that-cost-hours)",
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
			got := string(rewriteLinks([]byte(tc.in), repo))
			if got != tc.want {
				t.Errorf("rewriteLinks:\n  in:   %s\n  got:  %s\n  want: %s", tc.in, got, tc.want)
			}
		})
	}
}

// TestRewriteLinksNeverDoublesAScheme is the shipped bug stated as a property,
// so it cannot come back in some other form: no output may ever contain a
// scheme after the first character.
func TestRewriteLinksNeverDoublesAScheme(t *testing.T) {
	in := []byte("[a](https://example.com) [b](http://x.dev/y) [c](LICENSE) [d](AGENTS.md)")
	got := string(rewriteLinks(in, repo))
	for _, bad := range []string{"main/https://", "main/http://", "main/mailto:"} {
		if strings.Contains(got, bad) {
			t.Errorf("an absolute URL was rewritten as a repository path (%q): %s", bad, got)
		}
	}
}
