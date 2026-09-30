package main

import (
	"fmt"

	"github.com/joeblew999/irgo-windows-vm/internal/utmvm"
)

func runVersion(values, []string) error { fmt.Println(version); return nil }

// runCommands prints one command name per line. The site's flag reference and
// the docs check read this rather than scraping the usage text, whose layout
// can change.
func runCommands(values, []string) error {
	for _, c := range commands {
		fmt.Println(c.Name)
	}
	return nil
}

// runHelp prints the walkthrough. The bare usage is the list, for someone who
// typed a command wrong; this is for someone who asked what the tool is.
func runHelp(values, []string) error {
	fmt.Printf(`irgo-winvm — build a Go program on your Mac, run it on real Windows.

Three steps, in this order. Each one is cheap to repeat: if it is already
done, it says so and stops.

  1  iso-create   get the Windows installer (%s from Microsoft, or
                  built locally from an .esd you already have)
  2  vm-create    make a VM and install Windows on it (about 45 minutes,
                  unattended — you do not click anything)
  3  app-create   push your .exe into that VM, run it, print what it said

Undo, in the same shape:

     iso-delete   remove the installer
     vm-delete    remove the VM
     app-delete   remove your .exe from the VM

A VM in minutes, once one has been installed the slow way:

     vm-golden-create  seal an installed, disposable VM into the golden
                       image; from then on vm-create clones it and boots
                       the clone instead of installing, and other VMs keep
                       running. Each agent takes its own -vm name.
     vm-golden-delete  remove the golden image

When something is wrong:

     vm-screen    save a PNG of the VM's screen — the only way to see a
                  boot that is stuck, since it looks identical from here
     vm-repair    fix what stops -gui runs on a VM that has lived a while:
                  an expired password (no desktop session) and a stale
                  WebView2 registration; -reboot restarts it afterwards
     doctor       what is installed, what is missing, and where this run
                  wrote its log and screenshots

Your .exe is anything you built with GOOS=windows GOARCH=arm64. The programs
in examples/ are examples of that, and what this repository
uses to find out what breaks in glaze and native on Windows. In a checkout
of this repository:

     glaze-check  build the conformance suite and run it here (-windows: on
                  the VM), and record every test and each window's screenshot
                  in docs/GLAZE-STATUS.md
     glaze-status print that record, and whether it still holds

Every command takes -h for its flags.
`, utmvm.ISODownloadSize())
	return nil
}
