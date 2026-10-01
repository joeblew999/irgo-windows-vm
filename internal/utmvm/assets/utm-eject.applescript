-- Take the install medium out of a stopped VM, through UTM, without a restart.
--
-- Argument: the VM's UUID. Prints "1" when it removed the medium, "0" when it
-- was already out.
--
-- Why it has to come out: media mastered with efisys_noprompt.bin boots itself,
-- so after Setup's first reboot the firmware picks the CD again and never
-- reaches Windows on the disk (see ejectInstallMedia).
--
-- UTM's scripting names a drive by id, interface and size, never by file, so
-- the medium is found by position: vm-create writes the NVMe disk, then the
-- install ISO, the answer-file CD and the guest-tools CD, and UTM keeps that
-- order. Three CDs means the first is the install medium; two means it is
-- already out; anything else is not a bundle vm-create wrote, and is refused
-- rather than guessed at. UTM deletes the dropped drive's file from Data/ when
-- it saves: the bundle's copy of the ISO, never the media it came from.
tell application "UTM"
	set vm to virtual machine id %q
	set keep to {}
	set cds to 0
	-- Fetched into a variable first: "drives of (configuration of vm)" is
	-- resolved as an object reference and fails with -1700 (measured).
	set cfg to configuration of vm
	repeat with d in (drives of cfg)
		if (interface of d) is NVMe then
			set end of keep to {id:(id of d)}
		else
			set cds to cds + 1
			if cds is not 1 then set end of keep to {id:(id of d)}
		end if
	end repeat
	if cds is 2 then return "0"
	if cds is not 3 then error "expected the install, answer-file and guest-tools CDs on " & (name of vm) & ", found " & cds & " CDs; not guessing which is the install medium"
	update configuration of vm with {drives:keep}
	return "1"
end tell
