// Command site is the docs site's hooks: the pages that have no markdown file,
// generated from the code they describe. docsite (the docsite module in this
// repository) runs each one from site/docsite.toml, in the repository root,
// and publishes what it prints on stdout.
//
//	go run ./site reference    the command reference, captured from the binary
//	go run ./site mcp          the MCP page, captured from a live server's tool list
//	go run ./site api          the Worker API page, from wire's route table
//	go run ./site glaze-live   the script the Glaze status page ends with
//
// Everything else on the site is a markdown file: if the site is wrong there,
// the markdown is wrong. Here, a wrong flag or route is a bug in the Go code.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/joeblew999/irgo-windows-vm/wire"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: go run ./site reference|mcp|api|glaze-live")
		os.Exit(2)
	}
	out, err := generate(os.Args[1], ".")
	if err != nil {
		fmt.Fprintln(os.Stderr, "site:", err)
		os.Exit(1)
	}
	if _, err := os.Stdout.Write(out); err != nil {
		fmt.Fprintln(os.Stderr, "site:", err)
		os.Exit(1)
	}
}

// generate is one hook's output. root is the repository root, where docsite
// runs hooks.
func generate(name, root string) ([]byte, error) {
	switch name {
	case "reference":
		return generateReference(root)
	case "mcp":
		s, err := generateMCP(root)
		return []byte(s), err
	case "api":
		return generateAPI(), nil
	case "glaze-live":
		return glazeLive()
	}
	return nil, fmt.Errorf("no hook %q", name)
}

// glazeLive is the script at the end of the Glaze status page. Served by the
// Cloudflare Worker (worker/), it asks for the newest run CI posted and shows
// it above the recorded one, so the page is current without a redeploy;
// anywhere else, GitHub Pages included, the request finds nothing and the page
// stays as rendered. The path is the glaze-latest route's, from wire's table,
// relative so it resolves against wherever the site is served.
func glazeLive() ([]byte, error) {
	path, err := json.Marshal(strings.TrimPrefix(wire.MustFind(wire.RouteGlazeLatest).Path, "/"))
	if err != nil {
		return nil, err
	}
	return []byte(strings.Replace(glazeLiveHTML, "LIVE_API", string(path), 1)), nil
}

// glazeLiveHTML puts everything from the response in as text, or as an image
// URL under the API: never as HTML. No HTML comment in it: the page template
// used to strip it, so it was never published.
const glazeLiveHTML = `<script>
(function () {
  var prose = document.querySelector(".prose");
  if (!prose || !window.fetch) return;
  fetch(LIVE_API, { cache: "no-store" }).then(function (r) {
    var ct = r.headers.get("Content-Type") || "";
    return r.ok && ct.indexOf("application/json") === 0 ? r.json() : null;
  }).then(function (all) {
    if (!all) return;
    var box = document.createElement("div");
    box.className = "markdown-alert markdown-alert-note live";
    var title = document.createElement("p");
    title.className = "markdown-alert-title";
    title.textContent = "Latest runs posted by CI, live";
    box.appendChild(title);
    var any = false;
    ["mac", "windows"].forEach(function (target) {
      var run = all[target];
      if (!run || !run.manifest) return;
      any = true;
      var m = run.manifest;
      var p = document.createElement("p");
      var b = document.createElement("strong");
      b.textContent = (target === "mac" ? "Mac" : "Windows") + ": ";
      p.appendChild(b);
      p.appendChild(document.createTextNode((m.Verdict || "") + " — " + (m.When || "") +
        (m.Commit ? ", commit " + m.Commit.slice(0, 12) : "") + (m.Platform ? ", " + m.Platform : "")));
      if (m.RunURL && /^https:\/\/github\.com\//.test(m.RunURL)) {
        p.appendChild(document.createTextNode(" "));
        var a = document.createElement("a");
        a.href = m.RunURL;
        a.textContent = "(run)";
        p.appendChild(a);
      }
      box.appendChild(p);
      var shots = document.createElement("p");
      shots.className = "live-shots";
      (m.Tests || []).forEach(function (t) {
        if (!t.Picture) return;
        var img = document.createElement("img");
        img.src = run.base + encodeURIComponent(t.Picture);
        img.alt = t.Test + ": " + t.Result;
        img.title = img.alt;
        img.loading = "lazy";
        shots.appendChild(img);
      });
      box.appendChild(shots);
    });
    if (any) prose.insertBefore(box, prose.firstElementChild ? prose.firstElementChild.nextSibling : null);
  }).catch(function () {});
})();
</script>
`
