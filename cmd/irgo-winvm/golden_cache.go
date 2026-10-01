package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"time"

	"github.com/joeblew999/irgo-windows-vm/internal/utmvm"
)

// r2Config reads the bucket and its credentials from the environment; write
// is whether the command changes the bucket. Missing settings are the command
// called wrongly: exit 2, every variable named.
func r2Config(write bool) (utmvm.R2Config, error) {
	c, err := utmvm.R2ConfigFromEnv(write)
	if errors.Is(err, utmvm.ErrR2NotConfigured) {
		return c, fmt.Errorf("%w: %w", errUsage, err)
	}
	return c, err
}

func vmGoldenPushFlags() *flag.FlagSet {
	fs := flag.NewFlagSet("vm-golden-push", flag.ContinueOnError)
	fs.String("bundle", "", "the golden image's bundle directory to upload (a copy UTM exported; this process cannot read UTM's container)")
	fs.String("golden", utmvm.GoldenJSONPath(), "the golden image's record to carry with it")
	fs.Int64("parallel", utmvm.GoldenParallel, "chunks moving at once, each about 100 MB of memory")
	fs.Bool("delete", false, "remove a pushed image from the bucket instead: its manifest, and every chunk no other manifest needs")
	fs.String("id", "", "with -delete, the manifest to remove (default: latest)")
	fs.Bool("force", false, "with -delete, actually delete; without this it only lists")
	return fs
}

// runVMGoldenPush uploads the golden image to the owner's private R2 bucket,
// or with -delete removes one from it. Only chunks the bucket lacks are sent.
func runVMGoldenPush(v values, _ []string) error {
	say := utmvm.Printer("vm-golden-push")
	r, err := r2Config(true)
	if err != nil {
		return err
	}
	say("bucket: %s", r.Where())
	if v.Bool("delete") {
		return goldenCacheDelete(r, v.String("id"), v.Bool("force"), say)
	}
	bundle := v.String("bundle")
	if bundle == "" {
		return fmt.Errorf("%w: -bundle names the directory to upload", errUsage)
	}
	res, err := utmvm.GoldenPush(context.Background(), utmvm.GoldenPushOptions{
		R2:          r,
		Bundle:      bundle,
		Golden:      v.String("golden"),
		ToolVersion: version,
		Parallel:    int(v.Int64("parallel")),
	}, say)
	if err != nil {
		return err
	}
	say("pushed %s: %s of files, %s of data, %d chunks sent (%s), %d already there",
		res.ID, utmvm.HumanBytes(res.Bytes), utmvm.HumanBytes(res.Stored), res.Uploaded,
		utmvm.HumanBytes(res.UploadedBytes), res.Reused)
	say("pull it elsewhere with: irgo-winvm vm-golden-pull -id %s", res.ID)
	return nil
}

// goldenCacheDelete is vm-golden-push's undo. It does not ask whether the
// bucket is private: removing the image is what you would do if it were not.
func goldenCacheDelete(r utmvm.R2Config, id string, force bool, say func(string, ...any)) error {
	ctx := context.Background()
	say("STEP 1/2  what would go")
	rm, err := utmvm.InspectGoldenCacheRemoval(ctx, r, id)
	if err != nil {
		return err
	}
	if rm.ID == "" && len(rm.Chunks) == 0 && !rm.MovesLatest {
		say("          nothing to delete")
		return nil
	}
	if rm.ID != "" {
		say("          manifest %s, pushed %s", rm.ID, rm.Created.Format(time.RFC3339))
	} else if id != "" {
		say("          no manifest %s", id)
	}
	say("          %d chunks no other manifest needs, %s", len(rm.Chunks), utmvm.HumanBytes(rm.Bytes))
	switch {
	case !rm.MovesLatest:
	case rm.NewLatest != "":
		say("          latest would then name %s", rm.NewLatest)
	default:
		say("          latest would be removed: nothing would be left to pull")
	}
	say("          %d other manifests stay", rm.Remaining)
	if !force {
		return fmt.Errorf("deleting that from %s. Pass -force to do it (%w)", r.Where(), errRefused)
	}
	say("STEP 2/2  deleting")
	return utmvm.GoldenCacheDelete(ctx, r, rm, say)
}

func vmGoldenPullFlags() *flag.FlagSet {
	fs := flag.NewFlagSet("vm-golden-pull", flag.ContinueOnError)
	fs.String("dir", utmvm.GoldenPullDir(), "where the bundle and its golden.json go")
	fs.String("id", "", "the manifest to pull (default: latest)")
	fs.Int64("parallel", utmvm.GoldenParallel, "chunks moving at once")
	fs.Bool("delete", false, "remove what a pull left in -dir instead")
	fs.Bool("force", false, "with -delete, actually delete; without this it only lists")
	return fs
}

// runVMGoldenPull downloads the golden image from the owner's private R2
// bucket, verified chunk by chunk and file by file, or with -delete removes
// what an earlier pull left.
func runVMGoldenPull(v values, _ []string) error {
	say := utmvm.Printer("vm-golden-pull")
	dir := v.String("dir")
	if v.Bool("delete") {
		rm, err := utmvm.InspectGoldenPull(dir)
		if err != nil {
			return err
		}
		say("pull:   %s", utmvm.Home(dir))
		if !rm.Found {
			say("        nothing there; nothing to delete")
			return nil
		}
		say("        %s on disk", utmvm.HumanBytes(rm.Bytes))
		if !v.Bool("force") {
			return fmt.Errorf("%s of pulled golden image. Pulling it again takes minutes. Pass -force to do it (%w)",
				utmvm.HumanBytes(rm.Bytes), errRefused)
		}
		if err := utmvm.GoldenPullDelete(dir); err != nil {
			return err
		}
		say("removed %s", utmvm.Home(dir))
		return nil
	}

	r, err := r2Config(false)
	if err != nil {
		return err
	}
	say("bucket: %s", r.Where())
	say("into:   %s", utmvm.Home(dir))
	res, err := utmvm.GoldenPull(context.Background(), utmvm.GoldenPullOptions{
		R2:       r,
		Dir:      dir,
		ID:       v.String("id"),
		Parallel: int(v.Int64("parallel")),
	}, say)
	if err != nil {
		return err
	}
	if res.Skipped {
		say("%s was already pulled: %s", res.ID, utmvm.Home(res.Bundle))
		return nil
	}
	say("pulled %s: %s of files, %s downloaded", res.ID, utmvm.HumanBytes(res.Bytes), utmvm.HumanBytes(res.Downloaded))
	say("bundle: %s", utmvm.Home(res.Bundle))
	if res.Golden != "" {
		say("record: %s", utmvm.Home(res.Golden))
	}
	return nil
}
