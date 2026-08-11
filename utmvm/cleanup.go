package utmvm

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Removal describes what deleting a VM would reclaim.
type Removal struct {
	Name       string
	Path       string
	TotalBytes int64 // bytes on disk, counting hardlinks once
	Running    bool
}

// InspectRemoval reports what a Delete would do, without doing it.
//
// Worth having as a separate step because the apparent size of a bundle is
// misleading: `du` counts the hardlinked install ISO at full size, but removing
// the bundle only frees it if no other VM still links to it. This reports the
// space that would actually come back.
func InspectRemoval(ref string) (Removal, error) {
	var r Removal

	e, err := Find(ref)
	if err != nil {
		return r, err
	}
	r.Name = e.Name
	r.Running = strings.EqualFold(e.Status, "started")

	dir, err := DefaultVMDir()
	if err != nil {
		return r, err
	}
	r.Path = filepath.Join(dir, e.Name+".utm")
	if _, err := os.Stat(r.Path); err != nil {
		return r, fmt.Errorf("bundle not found at %s", r.Path)
	}
	r.TotalBytes = reclaimableBytes(r.Path)
	return r, nil
}

// reclaimableBytes sums file sizes, counting each inode once. A file with more
// than one link is shared with another bundle, so deleting this one frees
// nothing — counting it would overstate the saving.
func reclaimableBytes(root string) int64 {
	seen := map[uint64]bool{}
	var total int64
	filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		ino, nlink, ok := inodeInfo(p)
		if ok {
			if nlink > 1 {
				return nil // shared with another VM; deleting this one frees nothing
			}
			if seen[ino] {
				return nil
			}
			seen[ino] = true
		}
		total += info.Size()
		return nil
	})
	return total
}

// Delete stops a VM if needed and removes its bundle.
//
// The stop comes first and is deliberate: removing files under a running QEMU
// leaves the process writing to deleted inodes, so the space is not returned
// until it exits and UTM is left with a phantom entry.
func Delete(ref string, force bool) (Removal, error) {
	r, err := InspectRemoval(ref)
	if err != nil {
		return r, err
	}
	if r.Running {
		if !force {
			return r, fmt.Errorf("%s is running; stop it first or pass -force", r.Name)
		}
		vm := Named(ref)
		_ = vm.Stop()
		for i := 0; i < 15; i++ {
			if st, _ := vm.Status(); !strings.EqualFold(st, "started") {
				break
			}
			time.Sleep(2 * time.Second)
		}
	}
	if err := os.RemoveAll(r.Path); err != nil {
		return r, fmt.Errorf("removing %s: %w", r.Path, err)
	}
	return r, nil
}

// Prune removes generated artefacts that are not VMs: staged payload images and
// probe builds. It never touches downloaded ISOs, which are expensive to
// re-fetch and cheap to keep.
func Prune(dirs ...string) (int64, []string, error) {
	var freed int64
	var removed []string
	for _, d := range dirs {
		entries, err := os.ReadDir(d)
		if err != nil {
			continue
		}
		for _, e := range entries {
			name := e.Name()
			isArtefact := strings.HasSuffix(name, ".img") ||
				strings.HasSuffix(name, ".dmg") ||
				strings.HasPrefix(name, "irgo-winvm-payload-")
			if !isArtefact {
				continue
			}
			p := filepath.Join(d, name)
			if info, err := e.Info(); err == nil {
				freed += info.Size()
			}
			if err := os.RemoveAll(p); err == nil {
				removed = append(removed, p)
			}
		}
	}
	return freed, removed, nil
}
