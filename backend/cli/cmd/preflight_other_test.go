// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build !linux

package cmd

import (
	"errors"
	"testing"
)

func TestCheckHostDisk(t *testing.T) {
	gi := uint64(1 << 30)
	cases := []struct {
		free         uint64
		err          error
		fails, warns int
	}{
		{100 * gi, nil, 0, 0},
		{30 * gi, nil, 0, 1},
		{5 * gi, nil, 1, 0},
		{0, errors.New("boom"), 0, 1},
	}
	for _, c := range cases {
		d := &doctor{}
		checkHostDisk(d, t.TempDir(), func(string) (uint64, error) { return c.free, c.err })
		if d.fails != c.fails || d.warns != c.warns {
			t.Errorf("free=%d err=%v: fails=%d warns=%d, want %d/%d", c.free/gi, c.err, d.fails, d.warns, c.fails, c.warns)
		}
	}
}

func TestFreeBytesReadsARealVolume(t *testing.T) {
	if b, err := freeBytes(t.TempDir()); err != nil || b == 0 {
		t.Fatalf("freeBytes = %d, %v", b, err)
	}
}
