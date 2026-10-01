//go:build darwin || windows

package drive

import "fmt"

// App is the application the user is working in, as Frontmost reports it.
// Window and Title are the foreground window on Windows; macOS reports the
// application only and leaves them empty.
type App struct {
	Name   string // the process: its name on macOS, its executable on Windows
	PID    int
	Window uint32
	Title  string
}

// Same reports whether a and b are the same application and window. The
// title is not compared: a console's changes with whatever runs in it.
func (a App) Same(b App) bool { return a.PID == b.PID && a.Window == b.Window && a.Name == b.Name }

func (a App) String() string {
	if a.Window == 0 {
		return fmt.Sprintf("%s (pid %d)", a.Name, a.PID)
	}
	return fmt.Sprintf("%s %q (pid %d, window %#x)", a.Name, a.Title, a.PID, a.Window)
}
