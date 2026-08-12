// Prints "<windowID>\t<title>" for every UTM window, one per line.
//
// This exists because there is no other way to reliably see inside a running
// guest. `screencapture` without a window ID grabs whatever Space is frontmost,
// so it returns the developer's editor or browser rather than the VM; and
// macOS's accessibility API refuses to enumerate UTM's windows without
// Accessibility permission, which a CLI cannot grant itself.
//
// CGWindowListCopyWindowInfo has neither limitation. Deliberately omitting
// .optionOnScreenOnly is the important part: the VM window is usually on a
// different Space, and with that option set it would not be listed at all.
// Given the ID, `screencapture -l <id>` captures that window wherever it lives,
// without stealing focus or switching Spaces.
import CoreGraphics
import Foundation

let opts: CGWindowListOption = [.excludeDesktopElements]
guard let list = CGWindowListCopyWindowInfo(opts, kCGNullWindowID) as? [[String: Any]] else {
    exit(1)
}
for w in list {
    let owner = w[kCGWindowOwnerName as String] as? String ?? ""
    let name = w[kCGWindowName as String] as? String ?? ""
    let num = w[kCGWindowNumber as String] as? Int ?? 0
    if owner == "UTM" && !name.isEmpty {
        print("\(num)\t\(name)")
    }
}
