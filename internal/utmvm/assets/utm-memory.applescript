-- Every VM UTM knows, with its status and the memory it is configured with,
-- for the capacity check (vm_capacity.go). One line per VM:
--   <uuid> TAB <status> TAB <memory MiB, or ?> TAB <name>
--
-- Through UTM, not config.plist: macOS App Data protection refuses this
-- process UTM's container, and UTM reads its own configuration. "?" is a VM
-- whose memory UTM would not say; if it is running, the check cannot tell how
-- much is free and refuses.
tell application "UTM"
	set out to ""
	repeat with vm in virtual machines
		set mem to "?"
		try
			-- Fetched into a variable first, as in utm-eject.applescript:
			-- a property of (configuration of vm) can resolve as an object
			-- reference and fail.
			set cfg to configuration of vm
			set mem to (memory of cfg) as text
		end try
		set out to out & (id of vm) & tab & (status of vm) & tab & mem & tab & (name of vm) & linefeed
	end repeat
	return out
end tell
