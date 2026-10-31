// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build windows

package cmd

import (
	"syscall"
	"unsafe"
)

// freeBytes returns the bytes available to the caller on the volume holding path.
func freeBytes(path string) (uint64, error) {
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}
	proc := syscall.NewLazyDLL("kernel32.dll").NewProc("GetDiskFreeSpaceExW")
	var avail, total, free uint64
	r, _, callErr := proc.Call(uintptr(unsafe.Pointer(p)),
		uintptr(unsafe.Pointer(&avail)), uintptr(unsafe.Pointer(&total)), uintptr(unsafe.Pointer(&free)))
	if r == 0 {
		return 0, callErr
	}
	return avail, nil
}
