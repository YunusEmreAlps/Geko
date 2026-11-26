//go:build windows

package config

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

var replaceFile = windows.NewLazySystemDLL("kernel32.dll").NewProc("ReplaceFileW")

func replaceConfigFile(temp, target string) (string, error) {
	from, err := windows.UTF16PtrFromString(temp)
	if err != nil {
		return "", err
	}
	to, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return "", err
	}
	// ReplaceFile preserves the destination's ACLs and metadata. Do not fall
	// back to delete-then-rename, which would expose a missing-file interval.
	ok, _, callErr := replaceFile.Call(uintptr(unsafe.Pointer(to)), uintptr(unsafe.Pointer(from)), 0, 0, 0, 0)
	if ok == 0 {
		return "", callErr
	}
	return "", nil
}
