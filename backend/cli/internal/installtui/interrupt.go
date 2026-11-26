// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package installtui

import "sync"

// interrupter turns every way an operator can say "stop" into the two outcomes dcctl has
// always had: the first one cancels the run, once; the second one ends the process.
//
// 🔴 IN RAW MODE CTRL+C IS A KEYPRESS, NOT A SIGNAL. The terminal no longer turns it into
// SIGINT, so cmd/root.go's signal context never hears it: without this the first Ctrl+C
// would do nothing at all, and an operator would reach for kill -9 — which SIGKILLs tofu
// mid-apply, the state loss the root signal handling exists to prevent. An OS signal
// (SIGTERM, or SIGINT from kill) still arrives as a signal, to the root context AND to
// the presenter's own channel, and both of those are the same press: it is counted once,
// here, so a keypress after a signal is the SECOND interrupt, not a first.
//
// The first interrupt never quits the program. The screen stays until the engine
// settles, because the graceful stop (tofu's, the claim release) is allowed to take a
// long time and the operator must be able to watch it.
type interrupter struct {
	mu sync.Mutex
	n  int
	// cancel cancels the run's context. Called once, on the first interrupt.
	cancel func()
	// force ends the process. Called once, on the second interrupt.
	force func()
}

// interrupt records one interrupt and reports whether it was the first.
func (i *interrupter) interrupt() (first bool) {
	i.mu.Lock()
	i.n++
	n := i.n
	i.mu.Unlock()
	switch n {
	case 1:
		i.cancel()
		return true
	case 2:
		i.force()
	}
	return false
}

// count is how many interrupts have been received.
func (i *interrupter) count() int {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.n
}
