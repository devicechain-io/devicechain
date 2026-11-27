// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package installtui

import "time"

// fakeKeys is an input clock the test moves by hand, stamped as the stdin gate's reader
// stamps it: "read" is a key read from the terminal, whatever the event loop is doing.
type fakeKeys struct {
	at    time.Duration
	clock *inputClock
}

func newFakeKeys() *fakeKeys {
	f := &fakeKeys{at: time.Hour}
	f.clock = &inputClock{readerStamps: true, now: func() time.Duration { return f.at }}
	return f
}

// advance moves the clock on by d.
func (f *fakeKeys) advance(d time.Duration) { f.at += d }

// read records n keys read now by the terminal's reader.
func (f *fakeKeys) read() { f.clock.stamp() }
