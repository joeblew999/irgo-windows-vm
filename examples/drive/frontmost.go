//go:build darwin || windows

package drive

import "fmt"

// App is the application the user is working in, as Frontmost reports it.
type App struct {
	Name string
	PID  int
}

func (a App) String() string { return fmt.Sprintf("%s (pid %d)", a.Name, a.PID) }
