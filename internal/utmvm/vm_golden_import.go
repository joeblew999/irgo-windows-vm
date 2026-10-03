package utmvm

// The join between the private cache and vm-create: on a machine with no
// golden image, and the cache configured, the image is pulled (GoldenPull),
// UTM imports it as GoldenVMName, and vm-create clones it as usual. Minutes
// instead of a full install, and every later vm-create is a clone.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// goldenImporter is what pulling and importing the golden image needs from
// the outside world, as functions so the tests can run it with no UTM, no
// bucket and no VM.
type goldenImporter struct {
	lock     func() (func(), error)
	exists   func() (bool, error) // is GoldenVMName registered with UTM
	pull     func(context.Context, GoldenPullOptions, func(string, ...any)) (GoldenPullResult, error)
	name     func(bundle string) (string, error) // the VM name in a bundle's config.plist
	register func(bundle, name string) error     // UTM imports bundle, which stays where it is
	dir      string                              // where the pull goes; empty is GoldenPullDir
}

// importer is the real one.
var importer = goldenImporter{
	lock: func() (func(), error) { return Acquire(MachineLock) },
	exists: func() (bool, error) {
		_, ok, err := goldenEntry(windowsGuest)
		return ok, err
	},
	pull:     GoldenPull,
	name:     bundleVMName,
	register: importBundle,
}

// GoldenCacheFromEnv is the private cache's settings for reading, and whether
// it is configured. Nothing set is not configured, and not an error; some of
// it set and not the rest is the user meaning to use it, and an error naming
// what is missing, so vm-create does not quietly install instead.
func GoldenCacheFromEnv() (R2Config, bool, error) {
	if !GoldenCacheEnvSet() {
		return R2Config{}, false, nil
	}
	c, err := R2ConfigFromEnv(false)
	if err != nil {
		return c, false, err
	}
	return c, true, nil
}

// pullGolden pulls the golden image from the private cache and has UTM import
// it as GoldenVMName, under the machine lock, which is what vm-golden-pull and
// vm-golden-delete take too. The pull is kept in GoldenPullDir: UTM's copy is
// an APFS clone of it, so it costs no more space, and vm-golden-pull -delete
// removes it.
func (g goldenImporter) pullGolden(ctx context.Context, r R2Config, say func(string, ...any)) error {
	release, err := g.lock()
	if err != nil {
		return err
	}
	defer release()

	// Another process may have made or imported one while this waited.
	if ok, err := g.exists(); err != nil {
		return err
	} else if ok {
		say("          %s appeared while this was starting; cloning that", GoldenVMName)
		return nil
	}

	dir := g.dir
	if dir == "" {
		dir = GoldenPullDir()
	}
	res, err := g.pull(ctx, GoldenPullOptions{R2: r, Dir: dir, Parallel: GoldenParallel}, say)
	if err != nil {
		return fmt.Errorf("pulling the golden image: %w", err)
	}

	// UTM names an imported VM from its config.plist, so a bundle named for
	// something else would register under that name and be left there.
	got, err := g.name(res.Bundle)
	if err != nil {
		return fmt.Errorf("reading the VM name in %s: %w", Home(res.Bundle), err)
	}
	if got != GoldenVMName {
		return fmt.Errorf("the pulled image is a VM named %q, not %s; push the export of %s.\n"+
			"  vm-golden-pull -delete -force removes the pull", got, GoldenVMName, GoldenVMName)
	}

	say("… having UTM import %s as %s", Home(res.Bundle), GoldenVMName)
	if err := g.register(res.Bundle, GoldenVMName); err != nil {
		return err
	}
	// The record travels with the image; doctor reads it from here.
	if res.Golden != "" && res.Golden != windowsGuest.manifestPath() {
		b, err := os.ReadFile(res.Golden)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(windowsGuest.manifestPath()), 0o755); err != nil {
			return err
		}
		if err := writeFileSynced(windowsGuest.manifestPath(), b); err != nil {
			return err
		}
	}
	say("          imported; every vm-create from now on clones %s", GoldenVMName)
	return nil
}

// bundleVMName reads the VM's name out of a bundle's config.plist with
// plutil, which reads both the XML and the binary form.
func bundleVMName(bundle string) (string, error) {
	plist := filepath.Join(bundle, "config.plist")
	if _, err := os.Stat(plist); err != nil {
		return "", err
	}
	out, err := exec.Command("plutil", "-extract", "Information.Name", "raw", "-o", "-", plist).Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return "", fmt.Errorf("plutil: %s", strings.TrimSpace(string(ee.Stderr)))
		}
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}
