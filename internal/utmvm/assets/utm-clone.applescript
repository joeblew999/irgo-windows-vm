-- Clone a stopped VM into a new one, through UTM, without restarting it.
--
-- Arguments, in order: the source VM's name, the system disk's interface
-- (NVMe for Windows, VirtIO for Linux) twice, bare and for a message, the new
-- VM's memory in MiB (0 keeps the source's), the new VM's name, the new MAC,
-- and the new name again.
-- Prints the MAC and the memory the new VM ended up with, tab-separated, so
-- the caller can check both took.
--
-- Why each part is here (UTM v4.7.5 source, Scripting/ and Platform/UTMData.swift):
--
--  * `duplicate` is UTMData.clone: copyfile with CLONE and DATA_SPARSE, so on
--    APFS the copy is instant and stays sparse. It always gives the copy a new
--    UUID, and names it "<source> 2" until the configuration below renames it,
--    which also moves the bundle to "<name>.utm".
--  * It keeps the source's MAC unless UTM's global IsRegenerateMACOnClone is
--    on, and that defaults to off. Two running clones with one MAC compete for
--    one DHCP lease, so the MAC is always set here, never left to a setting.
--  * `drives` lists what to KEEP, by id: UTM removes every drive not listed,
--    and deletes its file from Data/ when it saves. Only the system disk is
--    kept: the first drive on its interface, which is where every bundle this
--    tool writes puts it. On Windows the install ISO (5.26 GB, and a separate
--    copy on irgo-win11), the answer-file CD and the guest-tools CD are done
--    with once Windows and the agent are installed. On Linux the seed CD is
--    on VirtIO too, second; UTM reports it, like the disk, as not removable,
--    and its scripting has no disk-or-CD property (measured 3 Oct 2026).
--  * `memory` is set in the same properties: a test clone is made with 4 GiB
--    where the image has 8, so one fits beside irgo-win11 on a 16 GiB Mac
--    (capacity_model.go). Read back, because a property UTM ignores is not an
--    error.
--  * All of it is done by UTM, which can read its own container. This process
--    cannot: macOS App Data protection returns "Operation not permitted" for
--    ls, cat and touch in ~/Library/Containers/com.utmapp.UTM, even
--    unsandboxed (measured 30 Sep 2026). stat on a known path still works.
tell application "UTM"
	set src to virtual machine named %q
	set keep to {}
	-- Fetched into a variable first: "drives of (configuration of src)" is
	-- resolved as an object reference and fails with -1700 (measured).
	set cfg to configuration of src
	repeat with d in (drives of cfg)
		if (interface of d) is %s and (count of keep) is 0 then set end of keep to {id:(id of d)}
	end repeat
	if (count of keep) is not 1 then error "found no %s system disk on " & (name of src)
	set mem to %d
	if mem is 0 then set mem to memory of cfg
	duplicate src with properties {configuration:{name:%q, drives:keep, memory:mem, network interfaces:{{index:0, address:%q}}}}
	set newCfg to configuration of (virtual machine named %q)
	return (address of item 1 of (network interfaces of newCfg)) & tab & ((memory of newCfg) as text)
end tell
