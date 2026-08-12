package utmvm

import (
	"fmt"
	"os"
)

// A Windows 11 install consumes roughly this much once it settles. The sparse
// disk starts near zero, so the free-space figure at creation time is
// misleading — the cost arrives later, during install, when failing is most
// expensive.
const WindowsInstallBytes = 30 << 30

// SpaceCheck reports whether a target directory can host an install.
type SpaceCheck struct {
	FreeBytes     int64
	RequiredBytes int64
	ISOBytes      int64 // 0 when the ISO is hardlinked and therefore free
	OK            bool
}

func (s SpaceCheck) String() string {
	return fmt.Sprintf("%s free, ~%s needed", HumanBytes(s.FreeBytes), HumanBytes(s.RequiredBytes))
}

// CheckSpace verifies there is room before a VM is created.
//
// Worth doing up front because the failure mode is so bad: the sparse disk and
// hardlinked ISO make a new bundle look almost free, then Windows Setup runs
// out of space mid-install and leaves a corrupt VM that has to be rebuilt from
// scratch — after a 20-minute wait.
//
// The ISO costs nothing when it can be hardlinked into the same volume, so it
// is only counted when a copy would be needed.
func CheckSpace(targetDir, isoPath string) (SpaceCheck, error) {
	var s SpaceCheck

	free, err := statfsAvailable(targetDir)
	if err != nil {
		return s, fmt.Errorf("checking free space on %s: %w", targetDir, err)
	}
	s.FreeBytes = free
	s.RequiredBytes = WindowsInstallBytes

	if isoPath != "" && !sameVolume(targetDir, isoPath) {
		if n, err := fileSize(isoPath); err == nil {
			s.ISOBytes = n
			s.RequiredBytes += n
		}
	}

	s.OK = s.FreeBytes >= s.RequiredBytes
	return s, nil
}

// sameVolume reports whether two paths live on one filesystem, in which case a
// hardlink works and the ISO is free.
//
// A false answer only costs an over-estimate of the space needed, which is the
// safe direction — so a platform that cannot tell says no.
func sameVolume(a, b string) bool { return sameDevice(a, b) }

func fileSize(p string) (int64, error) {
	fi, err := os.Stat(p)
	if err != nil {
		return 0, err
	}
	return fi.Size(), nil
}
