//go:build darwin

package keeper

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"time"

	"github.com/joeblew999/irgo-windows-vm/internal/device"
)

// Caffeinate holds the Mac awake with a `caffeinate -i -s -w <keeper pid>`
// child: -i holds off idle sleep, -s system sleep on AC power, and -w makes
// it exit when the keeper does, even when the keeper is killed with SIGKILL,
// so no hold outlives it. The hold is checked in `pmset -g assertions`.
type Caffeinate struct {
	cmd    *exec.Cmd
	exited chan struct{} // closed when cmd has exited
}

// Hold starts caffeinate and returns once pmset lists its assertion.
func (c *Caffeinate) Hold() error {
	if c.Held() {
		return nil
	}
	cmd := exec.Command("/usr/bin/caffeinate", "-i", "-s", "-w", strconv.Itoa(os.Getpid()))
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting caffeinate: %w", err)
	}
	exited := make(chan struct{})
	go func() { _ = cmd.Wait(); close(exited) }()
	c.cmd, c.exited = cmd, exited
	for deadline := time.Now().Add(3 * time.Second); ; {
		select {
		case <-exited:
			c.cmd = nil
			return fmt.Errorf("caffeinate (pid %d) exited at once", cmd.Process.Pid)
		default:
		}
		out, err := device.Assertions()
		if err == nil && device.HoldsAwake(out, cmd.Process.Pid) {
			return nil
		}
		if time.Now().After(deadline) {
			_ = cmd.Process.Kill()
			c.cmd = nil
			if err != nil {
				return fmt.Errorf("caffeinate (pid %d) started, and pmset -g assertions could not be read to check it: %w", cmd.Process.Pid, err)
			}
			return fmt.Errorf("caffeinate (pid %d) started, and pmset -g assertions does not list it holding the Mac awake after 3 s", cmd.Process.Pid)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// Release stops caffeinate and returns once pmset no longer lists its
// assertion.
func (c *Caffeinate) Release() error {
	if c.cmd == nil {
		return nil
	}
	pid := c.cmd.Process.Pid
	if err := c.cmd.Process.Kill(); err != nil && err != os.ErrProcessDone {
		return fmt.Errorf("stopping caffeinate (pid %d): %w", pid, err)
	}
	c.cmd = nil
	for deadline := time.Now().Add(3 * time.Second); ; {
		out, err := device.Assertions()
		if err == nil && !device.HoldsAwake(out, pid) {
			return nil
		}
		if time.Now().After(deadline) {
			if err != nil {
				return fmt.Errorf("caffeinate (pid %d) was stopped, and pmset -g assertions could not be read to check: %w", pid, err)
			}
			return fmt.Errorf("caffeinate (pid %d) was stopped, and pmset -g assertions still lists it after 3 s", pid)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// Held is whether the hold is in place: caffeinate started here, not
// stopped, and still running. One that died on its own is not held, so the
// next pass holds again.
func (c *Caffeinate) Held() bool {
	if c.cmd == nil {
		return false
	}
	select {
	case <-c.exited:
		c.cmd = nil
		return false
	default:
		return true
	}
}

// Holder names the process holding the Mac awake.
func (c *Caffeinate) Holder() string {
	if c.cmd == nil {
		return "nothing"
	}
	return fmt.Sprintf("caffeinate pid %d", c.cmd.Process.Pid)
}
