// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// A crashed create without a record stops reserving its port after this age.
const controlClaimMaxAge = 24 * time.Hour

// Claims have monotonically increasing generations. Release creates a separate
// .released marker, leaving the exclusive claim in place permanently. These small
// tombstones prevent an allocator with an old directory listing from reusing a
// generation, including after expiry. No platform-specific file lock is used.
func claimControlPort(state, port, name string) (func() error, bool, error) {
	dir := filepath.Join(state, ".control-claims", port)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, false, fmt.Errorf("create control claim directory: %w", err)
	}
	for {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return nil, false, fmt.Errorf("list control claims: %w", err)
		}
		var generation uint64
		for _, entry := range entries {
			ext := filepath.Ext(entry.Name())
			n, err := strconv.ParseUint(strings.TrimSuffix(entry.Name(), ext), 10, 63)
			if err != nil || (ext != ".claim" && ext != ".released") || entry.IsDir() {
				return nil, false, fmt.Errorf("invalid control claim %q", entry.Name())
			}
			if n >= generation {
				generation = n + 1
			}
			if ext == ".released" {
				continue
			}
			path := filepath.Join(dir, entry.Name())
			released := strings.TrimSuffix(path, ".claim") + ".released"
			if _, err := os.Stat(released); err == nil {
				continue
			} else if !os.IsNotExist(err) {
				return nil, false, err
			}
			info, err := os.Stat(path)
			if err != nil {
				return nil, false, err
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				return nil, false, err
			}
			owner := string(raw)
			// An empty, newly created file is also taken while its owner is
			// being written. Saved records reserve their ports independently,
			// even after their claims expire or are released.
			if owner != name && time.Since(info.ModTime()) < controlClaimMaxAge {
				return nil, false, nil
			}
		}
		path := filepath.Join(dir, fmt.Sprintf("%020d.claim", generation))
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if os.IsExist(err) {
			continue // Another process won; inspect its claim before retrying.
		}
		if err != nil {
			return nil, false, fmt.Errorf("reserve sim control port: %w", err)
		}
		release := func() error {
			return os.WriteFile(strings.TrimSuffix(path, ".claim")+".released", nil, 0o600)
		}
		_, writeErr := f.WriteString(name)
		if err := errors.Join(writeErr, f.Close()); err != nil {
			return nil, false, errors.Join(err, release())
		}
		return release, true, nil
	}
}
