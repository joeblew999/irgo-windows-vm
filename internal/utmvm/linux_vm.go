package utmvm

// A Linux VM from Ubuntu's cloud image.
//
// There is no installer. The image is an installed system: it is converted to
// the raw sparse disk every VM here has, and its first boot runs cloud-init,
// which reads a seed CD for the account to make and the packages to add. So
// "install" is a download, a conversion of seconds and one boot of about half
// a minute, and nothing is typed at any point: UTM's firmware boots the disk
// by itself (docs/RESULTS.md, "A Linux guest by hand").
//
// The host side is what Windows uses, unchanged: a bundle written to staging
// and imported by UTM, started with a display, waited for through the guest
// agent.

import (
	"bytes"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	qcow2reader "github.com/lima-vm/go-qcow2reader"
	"github.com/lima-vm/go-qcow2reader/convert"
)

// The image, pinned: one dated release and its SHA-256, from Ubuntu's own
// SHA256SUMS for that release. Not noble/current/, which changes daily: a VM
// that differs between two machines gives results that cannot be compared.
// A newer pin is a new name, and prune removes the image it replaces.
const (
	linuxImageRelease = "20260926"
	linuxImageURL     = "https://cloud-images.ubuntu.com/noble/" + linuxImageRelease + "/noble-server-cloudimg-arm64.img"
	linuxImageSHA256  = "1d6bffe64b848468ac97f821d369a4846d983de1800ccf6b5ec8853e85cefc55"
	linuxImageBytes   = 620224512

	// linuxImagePrefix starts every pin's file name in the media directory.
	linuxImagePrefix = "ubuntu-24.04-server-cloudimg-arm64-"
	linuxImageName   = linuxImagePrefix + linuxImageRelease + ".img"

	// seedLabel is the volume name cloud-init looks for (NoCloud).
	seedLabel = "cidata"

	// linuxBootWait is how long a Linux VM gets to answer. A first boot took
	// 34 s with its packages installed from a fast network, and a later one
	// 24 s; the rest is for a slow mirror.
	linuxBootWait = 10 * time.Minute

	// linuxCheckWait bounds the check that follows, which waits for
	// cloud-init to finish.
	linuxCheckWait = 10 * time.Minute
)

var (
	// linuxUserData is the seed's user-data.
	//
	//go:embed assets/linux-user-data.yaml
	linuxUserData []byte

	// linuxCheckScript checks, in the guest, what linuxUserData asked for.
	//
	//go:embed assets/vm-linux-check.sh
	linuxCheckScript string
)

// LinuxImagePath is where the pinned cloud image is kept: with the other
// media.
func LinuxImagePath() string { return filepath.Join(ISODir(), linuxImageName) }

// LinuxImageDownloadSize is what fetching the image costs, for messages.
func LinuxImageDownloadSize() string { return HumanBytes(linuxImageBytes) }

// ensureLinuxImage returns the pinned image, downloading it when it is not
// here. Either way the file returned has the pinned SHA-256: a download is
// renamed into place only on a match (isoDownload), and a file already here
// is hashed again, which takes a second and is the only thing that says it is
// still the image.
func ensureLinuxImage(say func(string, ...any)) (path string, had bool, err error) {
	path = LinuxImagePath()
	if _, sErr := os.Stat(path); sErr == nil {
		got, hErr := fileSHA256(path)
		if hErr != nil {
			return "", true, fmt.Errorf("reading %s: %w", Home(path), hErr)
		}
		if got != linuxImageSHA256 {
			return "", true, fmt.Errorf("%s is not the pinned image: its SHA-256 is %s, want %s.\n"+
				"  Remove it and run this again to download it", Home(path), got, linuxImageSHA256)
		}
		return path, true, nil
	} else if !errors.Is(sErr, os.ErrNotExist) {
		return "", false, sErr
	}
	say("… downloading Ubuntu Server 24.04 ARM64, the cloud image of %s (%s), from %s",
		linuxImageRelease, LinuxImageDownloadSize(), linuxImageURL)
	last := time.Now()
	progress := func(done, total int64) {
		if time.Since(last) < 5*time.Second {
			return
		}
		last = time.Now()
		say("  %s of %s", HumanBytes(done), HumanBytes(total))
	}
	if dErr := isoDownload(linuxImageURL, nil, path, sha256Digest(linuxImageSHA256), progress); dErr != nil {
		return "", false, fmt.Errorf("downloading the cloud image to %s: %w", Home(path), dErr)
	}
	return path, false, nil
}

// fileSHA256 is the SHA-256 of a file, in hex.
func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }() // read-only
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// gptSignature is at the start of the second 512-byte sector of a GPT disk.
var gptSignature = []byte("EFI PART")

// convertImage writes the qcow2 image at src as a raw sparse file of size
// bytes at dst: the kind of disk every VM here has, which the capacity model
// measures and UTM clones. Ubuntu publishes qcow2 only.
//
// Zero clusters are not written, so the file stays sparse. The image's own
// size is 3.5 GiB; the rest of size is empty, and cloud-init grows the root
// partition into it on the first boot.
//
// Success means the file has that size and a partition table where the
// firmware will look for one.
func convertImage(src, dst string, size int64) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }() // read-only
	img, err := qcow2reader.Open(in)
	if err != nil {
		return fmt.Errorf("opening %s as a disk image: %w", Home(src), err)
	}
	defer func() { _ = img.Close() }() // read-only
	if err := img.Readable(); err != nil {
		return fmt.Errorf("%s (%s) cannot be read: %w", Home(src), img.Type(), err)
	}
	if img.Size() <= 0 || img.Size() > size {
		return fmt.Errorf("%s holds a disk of %d bytes, which does not fit the %d-byte disk to be made",
			Home(src), img.Size(), size)
	}
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	// Close is checked: it is where a full disk shows up.
	if err := convert.Convert(out, img, convert.Options{}); err != nil {
		_ = out.Close()
		return fmt.Errorf("converting %s: %w", Home(src), err)
	}
	if err := out.Truncate(size); err != nil {
		_ = out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}

	check, err := os.Open(dst)
	if err != nil {
		return err
	}
	defer func() { _ = check.Close() }() // read-only
	fi, err := check.Stat()
	if err != nil {
		return err
	}
	sig := make([]byte, len(gptSignature))
	if _, err := check.ReadAt(sig, 512); err != nil {
		return fmt.Errorf("reading back %s: %w", dst, err)
	}
	if fi.Size() != size || !bytes.Equal(sig, gptSignature) {
		return fmt.Errorf("%s was converted, and is %d bytes with %q where a partition table starts; want %d bytes and %q",
			dst, fi.Size(), sig, size, gptSignature)
	}
	return nil
}

// seedHostname is name as a hostname: lower case, letters, digits and
// hyphens, at most 63 of them, not starting or ending with a hyphen. A name
// with none of those is "linux".
func seedHostname(name string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	h := strings.Trim(b.String(), "-")
	if len(h) > 63 {
		h = strings.Trim(h[:63], "-")
	}
	if h == "" {
		return "linux"
	}
	return h
}

// buildSeed writes the CD cloud-init reads on the first boot: user-data, the
// same for every VM, and meta-data naming this one. instance is what tells
// cloud-init a machine is new; a VM's UUID is used.
//
// The image is what isoBuildImage writes for the answer file, under the label
// cloud-init looks for. Measured 2 Oct 2026: read from a VirtIO CD.
func buildSeed(imagePath, instance, hostname string) error {
	stage, err := os.MkdirTemp("", "irgo-winvm-seed-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(stage) }() // scratch
	meta := fmt.Sprintf("instance-id: %s\nlocal-hostname: %s\n", instance, hostname)
	if err := os.WriteFile(filepath.Join(stage, "user-data"), linuxUserData, 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(stage, "meta-data"), []byte(meta), 0o644); err != nil {
		return err
	}
	return isoBuildImage(imagePath, stage, 16, seedLabel)
}

// createLinuxBundle writes a UTM bundle for a Linux VM named name under
// outDir, from the cloud image at image, and returns its path.
func createLinuxBundle(name, outDir, image string) (string, error) {
	g := linuxGuest
	bundle := filepath.Join(outDir, name+bundleExt)
	if _, err := os.Stat(bundle); err == nil {
		return "", fmt.Errorf("%s already exists; remove it or choose another name", bundle)
	}
	data := filepath.Join(bundle, bundleData)
	if err := os.MkdirAll(data, 0o755); err != nil {
		return "", err
	}
	// A half-built bundle is one UTM would reject without saying why.
	ok := false
	defer func() {
		if !ok {
			_ = os.RemoveAll(bundle) // cleanup of a failed create; the create error is the news
		}
	}()

	var opts Options
	opts.setDefaults()
	if err := convertImage(image, filepath.Join(data, diskImage), int64(opts.DiskGiB)<<30); err != nil {
		return "", fmt.Errorf("system disk: %w", err)
	}
	cfg := Config{
		Name:       name,
		UUID:       newUUID(),
		MemoryMiB:  g.memoryMiB,
		CPUCount:   opts.CPUCount,
		MACAddress: randomMAC(),
		guest:      g,
	}
	if err := buildSeed(filepath.Join(data, seedISO), cfg.UUID, seedHostname(name)); err != nil {
		return "", fmt.Errorf("seed CD: %w", err)
	}
	// The seed is a VirtIO CD, not a USB one like the Windows CDs. As a USB
	// CD cloud-init never ran: no account, no network, two minutes waiting
	// for one, then a login prompt nobody can use (measured twice, 2 Oct
	// 2026; docs/TRAPS.md). As VirtIO it is /dev/vdb and is read.
	cfg.Drives = []Drive{
		{ID: newUUID(), ImageName: diskImage, Type: DriveDisk, Interface: g.diskIface},
		{ID: newUUID(), ImageName: seedISO, Type: DriveCD, Interface: IfaceVirtIO, ReadOnly: true},
	}
	plist, err := cfg.Plist()
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(bundle, "config.plist"), []byte(plist), 0o644); err != nil {
		return "", err
	}
	ok = true
	return bundle, nil
}

// vmCreateLinux is VMCreate for -os linux, after UTM is known to be there.
// begin and stage are VMCreate's own; steps is its step count, set here once
// it is known whether the VM has to be made.
func vmCreateLinux(opts VMCreateOptions, res *VMCreateResult, steps *int,
	begin func(string), stage func(string, bool, string, error) error, say func(string, ...any)) error {
	e, fErr := Find(opts.VMName)
	made := false
	switch {
	case errors.Is(fErr, ErrNoVM):
		// From the golden image when there is one: a clone, a boot of about
		// half a minute, and no network needed.
		if !opts.NoGolden {
			if _, ok, gErr := goldenEntry(linuxGuest); gErr != nil {
				return stage("the golden image", false, "", gErr)
			} else if ok {
				*steps = 2
				begin("a clone of the golden image, " + GoldenLinuxVMName)
				if _, cErr := CloneFromGolden(opts.VMName, GuestLinux, say); cErr != nil {
					return stage("clone the golden image", false, "", cErr)
				}
				res.Ready = true
				_ = stage("clone the golden image", false, "cloned, booted, named, checked", nil)
				return nil
			}
		}
		if !opts.Install {
			say("          there is no VM %s. Re-run with -install to make it from Ubuntu's cloud image:", opts.VMName)
			say("          a download of %s the first time, then about a minute.", LinuxImageDownloadSize())
			if !opts.NoGolden {
				say("          Or make a golden image once (vm-golden-create -vm <that VM>), and every later")
				say("          vm-create -os linux is a clone of seconds.")
			}
			return nil
		}
		if !opts.NoGolden {
			say("          there is no Linux golden image (%s); making %s from Ubuntu's cloud image", GoldenLinuxVMName, opts.VMName)
		}
		*steps = 6
		begin("the Ubuntu cloud image")
		image, had, iErr := ensureLinuxImage(say)
		if iErr != nil {
			return stage("the cloud image", false, "", iErr)
		}
		_ = stage("the cloud image", had, filepath.Base(image)+", SHA-256 as pinned", nil)
		res.ISO = image

		begin("permission to drive UTM")
		if aErr := CheckAutomation(); aErr != nil {
			return stage("control UTM", false, "", aErr)
		}
		_ = stage("control UTM", true, "permitted", nil)

		// Written outside UTM's folder and imported by UTM, under the machine
		// lock, as a Windows bundle is: the image is read while the bundle
		// is made, and prune must not take it away underneath.
		begin("the VM bundle")
		release, lErr := Acquire(MachineLock)
		if lErr != nil {
			return stage("VM bundle", false, "", lErr)
		}
		staged := filepath.Join(stagingDir(), opts.VMName+bundleExt)
		removeStaged(staged) // left by a run that died between writing and importing
		say("… converting the image to a raw disk in %s", Home(staged))
		_, cErr := createLinuxBundle(opts.VMName, stagingDir(), image)
		if cErr == nil {
			say("… having UTM import it")
			cErr = registerBundle(staged, opts.VMName)
		}
		release()
		if cErr != nil {
			return stage("VM bundle", false, "", cErr)
		}
		_ = stage("VM bundle", false, "created and registered "+opts.VMName, nil)
		if e, fErr = Find(opts.VMName); fErr != nil {
			return stage("VM bundle", false, "", fErr)
		}
		made = true
	case fErr != nil:
		return stage("VM bundle", false, "", fErr)
	default:
		*steps = 4
		begin("the VM bundle")
		_ = stage("VM bundle", true, e.Name+" ("+e.Status+")", nil)
	}

	begin("booting it")
	vm := Named(e.UUID)
	switch {
	case vm.AgentReady():
		_ = stage("boot", true, "running, agent answering", nil)
	case vm.IsPaused():
		say("… resuming from suspend")
		if rErr := vm.Resume(); rErr != nil {
			return stage("resume", false, "", rErr)
		}
		if wErr := vm.waitForAgentEvery(2*time.Minute, time.Second); wErr != nil {
			return stage("resume", false, "", wErr)
		}
		_ = stage("resume", false, "restored from suspend, agent answering", nil)
	default:
		bundle, bErr := BundlePath(e.Name)
		if bErr != nil {
			return stage("boot", false, "", bErr)
		}
		wait := linuxBootWait
		if opts.Timeout > 0 && opts.Timeout < wait {
			wait = opts.Timeout
		}
		// Nothing is typed: the firmware boots the disk by itself. Until
		// cloud-init has installed the agent, a first boot looks from here
		// exactly like a VM that will never answer.
		if rErr := EnsureReady(e.UUID, bundle, wait, say); rErr != nil {
			return stage("boot", false, "", rErr)
		}
		_ = stage("boot", false, "booted, agent answering", nil)
	}

	begin("checking it is the machine promised")
	if cErr := linuxCheck(e.UUID, made, say); cErr != nil {
		return stage("check", false, "", cErr)
	}
	res.Ready = true
	detail := "checked: cloud-init done or off, the account there"
	if made {
		detail += ", SSH is off"
	}
	_ = stage("check", false, detail, nil)
	return nil
}

// linuxCheck runs vm-linux-check.sh in the guest and says its lines. An agent
// that answers means the guest is up, not that its first boot has finished:
// the script waits for cloud-init, then checks the account and, on a VM this
// run made, that nothing listens on port 22. On one that existed,
// vm-ssh-create may since have turned SSH on, so there it only says which.
func linuxCheck(vmRef string, made bool, say func(string, ...any)) error {
	g := linuxGuest
	script := g.publicPath("irgo-vm-linux-check.sh")
	if err := pushScript(vmRef, script, linuxCheckScript); err != nil {
		return fmt.Errorf("pushing the check script: %w", err)
	}
	argv := []string{"/bin/sh", script, "-User", linuxUser}
	if made {
		argv = append(argv, "-New")
	}
	res, err := appExec(vmRef, argv, linuxCheckWait, say)
	if err != nil {
		return err
	}
	for _, l := range strings.Split(res.Stdout, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			say("%s", l)
		}
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("the check script exited %d in the guest: %s", res.ExitCode, lastLine(res.Stdout))
	}
	return nil
}

// linuxCloned gives a fresh clone of the Linux golden image the name it is
// known by, as the seed gives a VM made from the cloud image, and checks it
// as vm-create checks a VM it has just made. The image has cloud-init off, so
// nothing else would: every clone would be called after the VM it was sealed
// from.
func linuxCloned(vmRef, name string, say func(string, ...any)) error {
	host := seedHostname(name)
	res, err := appExec(vmRef, []string{"hostnamectl", "set-hostname", host}, time.Minute, say)
	if err != nil {
		return fmt.Errorf("naming %s: %w", name, err)
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("naming %s: hostnamectl exited %d in the guest: %s", name, res.ExitCode, lastLine(res.Stdout))
	}
	say("  hostname %s", host)
	return linuxCheck(vmRef, true, say)
}

// linuxUser is the account the seed makes.
const linuxUser = "dev"

// lastLine is the last line of s that says something.
func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}
