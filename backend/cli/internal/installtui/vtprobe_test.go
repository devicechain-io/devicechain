// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package installtui

import (
	"errors"
	"testing"
)

func TestVTProbe(t *testing.T) {
	const vt, base = 0x4, 0x3
	fail := errors.New("refused")
	for _, c := range []struct {
		name      string
		getErr    error
		mode      uint32
		setErrs   []error // per call; a call past the end succeeds
		want      bool
		wantCalls int
		finalMode uint32 // the mode the console is left in by the probe
		retry     bool   // the probe hands back a restore to try again on exit
	}{
		{name: "not a console", getErr: fail, mode: base, want: false, finalMode: base},
		{name: "already on", mode: base | vt, want: true, finalMode: base | vt},
		{name: "refused", mode: base, setErrs: []error{fail}, want: false, wantCalls: 1, finalMode: base},
		{name: "accepted and put back", mode: base, setErrs: []error{nil, nil}, want: true, wantCalls: 2, finalMode: base},
		// The mode took but could not be put back: the console is no longer in the state
		// a session would save as its baseline, so it is not offered the terminal UI, and
		// the original mode is kept to put back on exit.
		{name: "put back refused", mode: base, setErrs: []error{nil, fail}, want: false, wantCalls: 2, finalMode: base | vt, retry: true},
	} {
		t.Run(c.name, func(t *testing.T) {
			mode, calls := c.mode, 0
			set := func(m uint32) error {
				var err error
				if calls < len(c.setErrs) {
					err = c.setErrs[calls]
				}
				calls++
				if err == nil {
					mode = m
				}
				return err
			}
			got, restore := probeVT(vt, func() (uint32, error) { return mode, c.getErr }, set)
			if got != c.want || calls != c.wantCalls || mode != c.finalMode {
				t.Fatalf("probeVT = %v after %d calls, mode %#x; want %v, %d, %#x", got, calls, mode, c.want, c.wantCalls, c.finalMode)
			}
			if (restore != nil) != c.retry {
				t.Fatalf("restore on exit: %v, want %v", restore != nil, c.retry)
			}
			if restore != nil {
				restore()
				if mode != base {
					t.Fatalf("the restore on exit left mode %#x, want the original %#x", mode, uint32(base))
				}
			}
		})
	}
}
