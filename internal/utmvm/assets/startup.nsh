@echo -off
# Auto-boot script for the UTM/EDK2 UEFI shell.
#
# Why this exists: UTM's aarch64 firmware does not reliably auto-select a boot
# option and drops to the interactive shell instead. The shell looks for
# startup.nsh on every mapped filesystem and runs it, so this is the supported
# hook for making the VM boot itself.
#
# Order matters. Installed Windows is tried FIRST so that once Setup has
# written the ESP, reboots go straight to the OS instead of restarting the
# installer in a loop.
#
# The installer is launched via cdboot_noprompt.efi, NOT efi\boot\bootaa64.efi.
# The latter is the "Press any key to boot from CD or DVD" stub: with nobody at
# the keyboard it times out and falls back to this shell. The _noprompt variant
# is the same loader without that wait, which is what makes an unattended
# install actually unattended.

echo Searching for an installed Windows...
for %d in fs0 fs1 fs2 fs3 fs4 fs5
  if exist %d:\EFI\Microsoft\Boot\bootmgfw.efi then
    echo Booting installed Windows from %d:
    %d:
    cd \EFI\Microsoft\Boot
    bootmgfw.efi
  endif
endfor

echo No installed Windows found. Falling back to install media...
for %d in fs0 fs1 fs2 fs3 fs4 fs5
  if exist %d:\EFI\Microsoft\Boot\cdboot_noprompt.efi then
    echo Booting Setup without the keypress prompt from %d:
    %d:
    cd \EFI\Microsoft\Boot
    cdboot_noprompt.efi
  endif
endfor

echo Nothing bootable found on fs0..fs5.
echo Check that the install ISO is attached and is ARM64 (bootaa64.efi present).
