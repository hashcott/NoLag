//go:build !windows

// Command gnl-ui is the tray interface and runs only on Windows. This stub
// exists so the repository builds and tests on the machine it is developed on;
// the drawing of the icon, which is the only part of this that is a decision
// rather than a system call, is built and tested everywhere.
package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "gnl-ui runs on Windows only")
	os.Exit(1)
}
