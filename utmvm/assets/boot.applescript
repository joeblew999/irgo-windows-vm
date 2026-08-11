
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
  input keystroke vm text (ASCII character 13)
  delay 1
  my slowType(vm, %q)
  input keystroke vm text (ASCII character 13)
  delay 2
  my slowType(vm, %q)
  input keystroke vm text (ASCII character 13)
  delay 3
  -- Exactly one key for the "Press any key to boot from CD" prompt. Never more:
  -- surplus keypresses reach Windows Setup and break the install.
  input keystroke vm text (ASCII character 13)
end tell