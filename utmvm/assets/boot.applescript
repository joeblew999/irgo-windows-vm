on slowType(vm, t)
  tell application "UTM"
    repeat with c in characters of t
      input keystroke vm text (c as text)
      delay %0.3f
    end repeat
  end tell
end slowType

tell application "UTM"
  set vm to virtual machine id %q

  -- Clear whatever is on the prompt, then select the filesystem.
  input keystroke vm text (ASCII character 13)
  delay 1
  my slowType(vm, %q)
  input keystroke vm text (ASCII character 13)
  delay 2

  -- Launch the bootloader.
  my slowType(vm, %q)
  input keystroke vm text (ASCII character 13)

  -- Answer "Press any key to boot from CD or DVD".
  --
  -- The prompt appears a second or two after the loader starts and waits about
  -- five. A single well-timed press is fragile: it missed the window on a VM
  -- with four drives after working on one with three, because enumeration
  -- shifts the timing. So press repeatedly across the window instead.
  --
  -- The bound matters. An earlier version sent 40 presses over 16 seconds; the
  -- surplus arrived after Setup had started, landed in its UI, and left
  -- fragments of an EFI path sitting in the Product key field — wrecking an
  -- install that had already partitioned the disk. Eight presses over ~6s stays
  -- inside the prompt window, and Setup shows no interactive UI for far longer
  -- than that.
  repeat 8 times
    delay 0.8
    input keystroke vm text (ASCII character 13)
  end repeat
end tell
