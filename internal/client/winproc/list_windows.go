//go:build windows

// Package winproc lists running process names.
//
// Through the toolhelp snapshot API — the same thing Task Manager uses. It asks
// the operating system what is running and reads back names. It does not open a
// handle into any process, read anyone's memory, or load anything into another
// program's address space. That is a hard constraint: this client is never
// inside a game's process, so an anti-cheat watching for exactly that finds
// nothing to object to.
package winproc

import (
	"fmt"

	"golang.org/x/sys/windows"
)

// List returns the names of running processes, such as "TslGame.exe".
func List() ([]string, error) {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil, fmt.Errorf("winproc: snapshot: %w", err)
	}
	defer windows.CloseHandle(snap)

	var e windows.ProcessEntry32
	e.Size = uint32(unsafeSizeof(e))
	if err := windows.Process32First(snap, &e); err != nil {
		return nil, fmt.Errorf("winproc: first entry: %w", err)
	}
	var out []string
	for {
		out = append(out, windows.UTF16ToString(e.ExeFile[:]))
		if err := windows.Process32Next(snap, &e); err != nil {
			if err == windows.ERROR_NO_MORE_FILES {
				return out, nil
			}
			// Enumeration can fail part way under heavy load. Returning what was
			// collected would look like the game had exited and pull its routes out
			// mid-match, so this is an error and the caller keeps its previous state.
			return nil, fmt.Errorf("winproc: next entry: %w", err)
		}
	}
}
