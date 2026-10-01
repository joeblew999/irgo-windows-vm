package docsite

import (
	"strings"
	"testing"
)

const repo = "https://github.com/example/project"

// rewriterSite is a site with the shape the link tests need: two file pages,
// a generated one, and a screenshots directory.
func rewriterSite() *Site {
	return &Site{
		Repo: repo, Branch: "main", Screens: "docs/screens",
		Pages: []Page{
			{PageConfig{Src: "README.md", Out: "index.html"}},
			{PageConfig{Src: "docs/USING.md", Out: "using.html"}},
			{PageConfig{Src: "docs/RESULTS.md", Out: "results.html"}},
			{PageConfig{Out: "mcp.html", Generate: &Hook{Run: []string{"x"}}}},
		},
	}
}

func rewrite(s *Site, in, src string) string {
	return string(newLinkRewriter(s).rewrite([]byte(in), src))
}

// TestRewriteLinks covers each destination a link can have, and the case that
// was shipped broken: every external link pointed at ".../blob/main/https://..."
// because ":" was excluded only as a target's first character.
//
// Negative control, run by hand: making hasScheme return false
// unconditionally fails the four "left alone" cases; making it return true
// fails the rewrite cases.
func TestRewriteLinks(t *testing.T) {
	for _, tc := range []struct {
		name, in, want string
	}{
		{"markdown file that becomes a page", "see [USING.md](docs/USING.md) first", "see [USING.md](using.html) first"},
		{"page link keeps its anchor", "[the exit codes](docs/USING.md#what-it-exits-with)", "[the exit codes](using.html#what-it-exits-with)"},
		{"generated page name is left alone", "[MCP](mcp.html)", "[MCP](mcp.html)"},
		{"corpus file is left alone", "[all](llms-full.txt)", "[all](llms-full.txt)"},
		{"screenshot points at the published copy", "![shot](docs/screens/desktop.png)", "![shot](screens/desktop.png)"},
		{"other repository file points into the repository", "[MIT](LICENSE)", "[MIT](" + repo + "/blob/main/LICENSE)"},
		{"https is left alone", "[r](https://github.com/example/project/releases)", "[r](https://github.com/example/project/releases)"},
		{"http is left alone", "[mise](http://mise.jdx.dev)", "[mise](http://mise.jdx.dev)"},
		{"mailto is left alone", "[mail](mailto:someone@example.com)", "[mail](mailto:someone@example.com)"},
		{"bare anchor is left alone", "[evidence](#evidence)", "[evidence](#evidence)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := rewrite(rewriterSite(), tc.in, "README.md"); got != tc.want {
				t.Errorf("\n  in:   %s\n  got:  %s\n  want: %s", tc.in, got, tc.want)
			}
		})
	}
}

// TestRewriteLinksResolvesFromTheSourceFile: a link inside docs/ is relative to
// docs/, as GitHub reads it.
//
// Negative control, run by hand: dropping the path.Join against the source's
// directory fails every case here.
func TestRewriteLinksResolvesFromTheSourceFile(t *testing.T) {
	for in, want := range map[string]string{
		"[r](RESULTS.md)":            "[r](results.html)",
		"[home](../README.md)":       "[home](index.html)",
		"![s](screens/vm/ready.png)": "![s](screens/vm/ready.png)",
		"[l](../LICENSE)":            "[l](" + repo + "/blob/main/LICENSE)",
	} {
		if got := rewrite(rewriterSite(), in, "docs/UPSTREAM.md"); got != want {
			t.Errorf("from docs/UPSTREAM.md, %s: got %s, want %s", in, got, want)
		}
	}
}

// TestRewriteLinksNeverDoublesAScheme is the shipped bug stated as a property.
func TestRewriteLinksNeverDoublesAScheme(t *testing.T) {
	got := rewrite(rewriterSite(), "[a](https://example.com) [b](http://x.dev/y) [c](LICENSE) [d](docs/USING.md)", "README.md")
	for _, bad := range []string{"main/https://", "main/http://", "main/mailto:"} {
		if strings.Contains(got, bad) {
			t.Errorf("an absolute URL was rewritten as a repository path (%q): %s", bad, got)
		}
	}
}

// TestRewriteLinksBranchAndNoRepo: repository links name the configured
// branch, and with no repository URL they are left for the check to report
// rather than pointed at a guess.
func TestRewriteLinksBranchAndNoRepo(t *testing.T) {
	s := rewriterSite()
	s.Branch = "trunk"
	if got := rewrite(s, "[l](LICENSE)", "README.md"); got != "[l]("+repo+"/blob/trunk/LICENSE)" {
		t.Errorf("branch: got %s", got)
	}
	s.Repo = ""
	if got := rewrite(s, "[l](LICENSE)", "README.md"); got != "[l](LICENSE)" {
		t.Errorf("no repo: got %s", got)
	}
}
