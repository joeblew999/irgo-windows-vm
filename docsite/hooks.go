package docsite

// Hooks: pages, or parts of pages, a project generates at build time.
//
// A hook is a command whose stdout is the output, or a file. docsite knows
// nothing about what the command does, which is the point: a command reference
// captured from a binary, a tool list from a live server, or an API from an
// OpenAPI document are each one line of config, and switching an API page from
// one generator to another is changing that line.

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

type hookJob struct {
	page string
	hook Hook
	dst  map[string][]byte
}

// runHooks runs every job at once and stores each output under its page.
// Every failure is reported, not just the first.
func runHooks(s *Site, jobs []hookJob) error {
	type result struct {
		out []byte
		err error
	}
	results := make([]result, len(jobs))
	var wg sync.WaitGroup
	for i, j := range jobs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out, err := runHook(s, j.page, j.hook)
			results[i] = result{out, err}
		}()
	}
	wg.Wait()

	var errs []error
	for i, j := range jobs {
		if results[i].err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", j.page, results[i].err))
			continue
		}
		j.dst[j.page] = results[i].out
	}
	return errors.Join(errs...)
}

// runHook produces one hook's output, converted to markdown when it is an
// OpenAPI document.
//
// Empty output is an error. A generator whose capture went to the wrong stream
// publishes an empty page that builds, renders and looks right; a page that
// says nothing reads as an answer.
func runHook(s *Site, page string, h Hook) ([]byte, error) {
	var out []byte
	what := h.File
	if h.File != "" {
		b, err := os.ReadFile(filepath.Join(s.Root, filepath.FromSlash(h.File)))
		if err != nil {
			return nil, err
		}
		out = b
	} else {
		what = strings.Join(h.Run, " ")
		cmd := exec.Command(h.Run[0], h.Run[1:]...)
		cmd.Dir = s.Root
		cmd.Env = append(os.Environ(),
			"DOCSITE_ROOT="+s.Root, "DOCSITE_PAGE="+page,
			"DOCSITE_REPO="+s.Repo, "DOCSITE_BASE="+s.Base)
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		if err := cmd.Run(); err != nil {
			return nil, fmt.Errorf("`%s` failed: %w: %s", what, err, strings.TrimSpace(stderr.String()))
		}
		out = stdout.Bytes()
	}
	if len(bytes.TrimSpace(out)) == 0 {
		return nil, fmt.Errorf("`%s` produced nothing; publishing that would be an empty page that looks deliberate", what)
	}
	if h.Format == "openapi" {
		md, err := OpenAPIMarkdown(out)
		if err != nil {
			return nil, fmt.Errorf("`%s`: %w", what, err)
		}
		return md, nil
	}
	return out, nil
}
