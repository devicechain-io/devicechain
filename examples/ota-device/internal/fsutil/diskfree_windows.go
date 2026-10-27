// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build windows

package fsutil

import "golang.org/x/sys/windows"

// FreeBytes is the space available to this process on the volume holding dir.
func FreeBytes(dir string) (uint64, error) {
	p, err := windows.UTF16PtrFromString(dir)
	if err != nil {
		return 0, err
	}
	var avail, total, free uint64
	if err := windows.GetDiskFreeSpaceEx(p, &avail, &total, &free); err != nil {
		return 0, err
	}
	return avail, nil
}
