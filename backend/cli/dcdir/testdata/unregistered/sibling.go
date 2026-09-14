// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package unregistered is the negative control for the config-directory guard: the
// two spellings that create a directory under ~/.devicechain without going through
// dcdir's registry. It is under testdata/, so the Go tool never builds it and the
// live guard never walks it.
package unregistered

import (
	"os"
	"path/filepath"
)

// joinedElement is how the real defect was written: a sibling named as an element,
// in a package that has no reason to know what else lives beside it.
func joinedElement() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".devicechain", "mystery"), nil
}

// joinedSubpath is the same thing spelled with the separator inside the literal,
// which a guard matching only the bare element would miss.
func joinedSubpath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".devicechain/mystery"), nil
}

var _, _ = joinedElement, joinedSubpath
