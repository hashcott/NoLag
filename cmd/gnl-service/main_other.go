//go:build !windows

// Command gnl-service is the privileged half of the GameNoLag client and runs
// only on Windows. This stub exists so the repository builds and tests on the
// machine it is developed on: the packages beneath it that hold real decisions —
// route planning, relay choice, the pipe protocol, key handling — are all built
// and tested everywhere.
package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "gnl-service runs on Windows only")
	os.Exit(1)
}
