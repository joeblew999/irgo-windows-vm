package glazecheck

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/joeblew999/irgo-windows-vm/internal/workerclient"
	"github.com/joeblew999/irgo-windows-vm/wire"
)

// Post sends a target's recorded run, its shots.json and every picture it
// names, to the Worker (wire's glaze-post route), so the site's Glaze status
// page shows it without a redeploy. CI's conformance job calls it through
// `glaze-check -post`. A target with no manifest has recorded no pictures,
// and there is nothing to post: that is said and is not an error.
func Post(ctx context.Context, root, target string, c *workerclient.Client, say func(string, ...any)) error {
	if !isTarget(target) {
		return fmt.Errorf("no such target %q: %s or %s", target, TargetMac, TargetWindows)
	}
	dir := filepath.Join(root, filepath.FromSlash(ShotsDir), target)
	m, ok, err := readManifestIn(dir)
	if err != nil {
		return err
	}
	if !ok {
		say("no %s: this run recorded no pictures, nothing to post", filepath.Join(dir, manifestName))
		return nil
	}
	manifest, err := os.ReadFile(filepath.Join(dir, manifestName))
	if err != nil {
		return err
	}
	pictures := map[string][]byte{}
	for _, r := range m.Tests {
		if r.Picture == "" {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, r.Picture))
		if err != nil {
			return fmt.Errorf("the manifest names %s: %w", r.Picture, err)
		}
		pictures[r.Picture] = b
	}
	say("posting %s's run to %s: shots.json and %d pictures", target, c.URL(wire.RouteGlazePost, target), len(pictures))
	res, err := c.GlazePost(ctx, target, manifest, pictures)
	if err != nil {
		return err
	}
	say("stored as run %s, %d pictures", res.Run, res.Pictures)
	return nil
}
