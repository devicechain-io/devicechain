// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package fsutil holds the two durable-write helpers every state file here uses.
package fsutil

import (
	"os"
	"path/filepath"
	"runtime"
)

// ExeSuffix is the executable suffix of the platform: ".exe" on Windows, otherwise empty.
func ExeSuffix() string {
	if runtime.GOOS == "windows" {
		return ".exe"
	}
	return ""
}

// WriteFileAtomic writes data to path so that a reader sees either the old content or the new,
// never a mixture: it writes a sibling temporary file, syncs it, renames it over the target and
// syncs the directory.
func WriteFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	cleanup := func() { _ = os.Remove(name) }
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Chmod(perm); err != nil && runtime.GOOS != "windows" {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return err
	}
	if err := os.Rename(name, path); err != nil {
		cleanup()
		return err
	}
	return SyncDir(dir)
}

// SyncDir flushes a directory's entries to disk. Windows has no such call, and a directory cannot
// be opened for sync there, so it is a no-op on that platform.
func SyncDir(dir string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

// RemoveStaleTemps deletes the temporary files an interrupted WriteFileAtomic leaves beside the
// named files (<name>.tmp*). Each is a complete copy of a state file that was never renamed into
// place, so it is never state, only litter.
func RemoveStaleTemps(dir string, names ...string) {
	for _, n := range names {
		m, _ := filepath.Glob(filepath.Join(dir, n+".tmp*"))
		for _, f := range m {
			_ = os.Remove(f)
		}
	}
}
