// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package presence

import "sync"

// maxWatermarkedDevices bounds the regression detector's memory.
//
// 🔴 THE KEY IS DEVICE-CONTROLLED, SO AN UNBOUNDED MAP HERE IS A HEAP THE FLEET CAN
// GROW. Every distinct (tenant, device) the tap has ever seen would be one entry, and
// nothing ever removes a device that stops connecting. This is a diagnostic counter,
// not a correctness mechanism, so it gets a hard ceiling and no bookkeeping.
const maxWatermarkedDevices = 50_000

// sessionWatermarks remembers the highest session id this replica has emitted per
// device, purely so a backwards step can be COUNTED (see Metrics.RegressedSessions).
//
// It is deliberately not an ordering decision. The projection's stored session is the
// authority, and a queue group means this replica has seen only some of a device's
// transitions — so acting on this view would reject transitions the projection would
// have accepted. Reading it as advice and emitting anyway is the whole design.
//
// On overflow it CLEARS rather than evicting. An LRU would need per-entry bookkeeping
// to protect a counter, and the cost of clearing is that the next transition per
// device is not compared — which loses some observations of a hazard, not any
// correctness. Cheap, O(1), and impossible to get subtly wrong.
type sessionWatermarks struct {
	mu   sync.Mutex
	seen map[string]uint64
}

func newSessionWatermarks() *sessionWatermarks {
	return &sessionWatermarks{seen: make(map[string]uint64)}
}

// regressed records session for the device and reports whether it went BACKWARDS
// relative to the highest this replica had already emitted for it. A device seen for
// the first time never counts as regressed — there is nothing to compare against, and
// counting it would make the metric read as a fleet-wide clock problem on every
// restart.
func (w *sessionWatermarks) regressed(tenant, device string, session uint64) bool {
	if w == nil {
		return false
	}
	key := tenant + "/" + device
	w.mu.Lock()
	defer w.mu.Unlock()
	prior, known := w.seen[key]
	if len(w.seen) >= maxWatermarkedDevices && !known {
		w.seen = make(map[string]uint64)
		prior, known = 0, false
	}
	if !known || session > prior {
		w.seen[key] = session
	}
	return known && session < prior
}
