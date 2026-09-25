//go:build windows

package winproc

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

func unsafeSizeof(e windows.ProcessEntry32) uintptr { return unsafe.Sizeof(e) }
