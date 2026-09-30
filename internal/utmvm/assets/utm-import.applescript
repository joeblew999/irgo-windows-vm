-- Register a bundle written outside UTM's folder, without restarting UTM.
--
-- Argument: the bundle's POSIX path. Prints the name UTM registered it under.
--
-- Two things this replaces, both measured 30 Sep 2026:
--
--  * Writing the bundle into UTM's Documents folder. macOS App Data protection
--    refuses this process `touch` there ("Operation not permitted", even
--    unsandboxed), so vm-create could only work from a terminal that had been
--    granted access to other apps' data.
--  * Restarting UTM so it rescans that folder. Quitting UTM stops every VM it
--    is running.
--
-- `import` is UTMData.importNewUTM (v4.7.5): UTM copies the bundle into its
-- own folder with FileManager.copyItem, which on APFS is a clone and costs
-- nothing, and registers it at once. The copy is UTM's to delete; the staged
-- original is ours, and the caller removes it.
set theBundle to POSIX file %q
tell application "UTM"
	set vm to import new virtual machine from theBundle
	return name of vm
end tell
