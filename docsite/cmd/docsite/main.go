// Command docsite renders a project's markdown into a static site, serves it,
// and checks it.
//
//	docsite build  [-config docsite.toml] [-root dir] [-out dir]
//	docsite serve  [-config ...] [-port 8127]
//	docsite check  [-config ...]
//	docsite pages  [-config ...] [-intent]
//
// Without a config file it publishes README.md and docs/*.md. See the
// module's README for the config.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/joeblew999/irgo-windows-vm/docsite"
)

const usage = `docsite renders a project's markdown into a static site.

  docsite build   render the site into its out directory
  docsite serve   build it, then serve it on localhost
  docsite check   build it into a temporary directory and check it: links,
                  anchors, images, screenshots, corpus, sitemap, and links
                  into the site from source files
  docsite pages   list the pages, one source per line (-intent: intent only)

Flags, for every command:
`

func main() {
	if len(os.Args) < 2 || os.Args[1] == "-h" || os.Args[1] == "help" || os.Args[1] == "--help" {
		fmt.Fprint(os.Stderr, usage)
		newFlags("build").PrintDefaults()
		if len(os.Args) < 2 {
			os.Exit(2)
		}
		return
	}
	cmd := os.Args[1]
	fs := newFlags(cmd)
	_ = fs.Parse(os.Args[2:]) // ExitOnError
	if err := run(cmd, fs); err != nil {
		fmt.Fprintln(os.Stderr, "docsite:", err)
		os.Exit(1)
	}
}

var (
	configFlag, rootFlag, outFlag, shaFlag *string
	portFlag                               *int
	intentFlag                             *bool
)

func newFlags(cmd string) *flag.FlagSet {
	fs := flag.NewFlagSet("docsite "+cmd, flag.ExitOnError)
	configFlag = fs.String("config", "", "config file (default: docsite.toml in -root or the current directory, if there is one)")
	rootFlag = fs.String("root", "", "project directory (default: the config's root, else the current directory)")
	outFlag = fs.String("out", "", "output directory (default: the config's out, else _site in the root)")
	shaFlag = fs.String("sha", "", "commit for the build stamp (default: read from git)")
	portFlag = fs.Int("port", 8127, "port for serve")
	intentFlag = fs.Bool("intent", false, "pages: list only the pages marked intent")
	return fs
}

func run(cmd string, fs *flag.FlagSet) error {
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected arguments: %v", fs.Args())
	}
	s, err := docsite.Load(*configFlag, *rootFlag)
	if err != nil {
		return err
	}
	if *outFlag != "" {
		if s.Out, err = filepath.Abs(*outFlag); err != nil {
			return err
		}
	}
	opt := docsite.Options{SHA: *shaFlag, Log: os.Stdout}

	switch cmd {
	case "build", "serve":
		fmt.Printf("docsite: %s -> %s\n", where(s), s.Out)
		if err := docsite.Build(s, opt); err != nil {
			return err
		}
		if cmd == "serve" {
			return docsite.Serve(s, *portFlag, os.Stdout)
		}
		return nil
	case "check":
		// Into a temporary directory, so the check is of what the source
		// produces now, never of an out directory left by an older build.
		tmp, err := os.MkdirTemp("", "docsite-check-*")
		if err != nil {
			return err
		}
		defer func() { _ = os.RemoveAll(tmp) }()
		s.Out = tmp
		opt.Log = nil
		fmt.Printf("docsite: checking %s\n", where(s))
		if err := docsite.Build(s, opt); err != nil {
			return err
		}
		r, err := docsite.Check(s)
		if err != nil {
			return err
		}
		for _, n := range r.Notes {
			fmt.Println("  ok  ", n)
		}
		for _, p := range r.Problems {
			fmt.Println("  FAIL", p)
		}
		switch len(r.Problems) {
		case 0:
		case 1:
			return fmt.Errorf("1 problem")
		default:
			return fmt.Errorf("%d problems", len(r.Problems))
		}
		fmt.Printf("docsite: %d pages, no problems\n", len(s.Pages))
		return nil
	case "pages":
		for _, p := range s.Pages {
			if *intentFlag && !p.Intent {
				continue
			}
			src := p.Src
			if src == "" {
				src = "(generated)"
			}
			fmt.Printf("%s\t%s\n", p.Out, src)
		}
		return nil
	}
	return fmt.Errorf("unknown command %q (build, serve, check or pages)", cmd)
}

func where(s *docsite.Site) string {
	if s.Config != "" {
		return s.Root + " (" + s.Config + ")"
	}
	return s.Root + " (no config: README.md and docs/*.md)"
}
