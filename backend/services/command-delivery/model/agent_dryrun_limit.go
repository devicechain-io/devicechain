// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"sync"
	"time"

	"github.com/devicechain-io/dc-microservice/actiongate"
)

// DryRunsPerMinute bounds an agent's command-proposal dry runs, per agent client and,
// independently, per person, in each replica. A platform default.
//
// A dry run stores nothing and reserves nothing, so it is not an actuation and the actuation
// limits (which are durable and cluster-wide, counted in the ledger under locks) do not apply.
// What it does cost is a policy decision and a device-management read per call, under the
// agent's own token. This budget bounds THAT, per replica: with N replicas an agent can make
// at most N times this many, which bounds the work without a shared counter on a path that
// writes nothing.
const DryRunsPerMinute = 60

// dryRunLimiter is a fixed one-minute window per key. It is in-process on purpose (see
// DryRunsPerMinute) and bounded: windows that have ended are dropped as it is used.
type dryRunLimiter struct {
	mu      sync.Mutex
	windows map[string]dryRunWindow
}

type dryRunWindow struct {
	start time.Time
	count int
}

// allow charges one dry run to every key and reports whether all of them were under the
// limit. A refused call charges nothing.
func (l *dryRunLimiter) allow(now time.Time, keys ...string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.windows == nil {
		l.windows = map[string]dryRunWindow{}
	}
	if len(l.windows) > 4096 {
		for k, w := range l.windows {
			if now.Sub(w.start) >= time.Minute {
				delete(l.windows, k)
			}
		}
	}
	for _, k := range keys {
		w := l.windows[k]
		if now.Sub(w.start) < time.Minute && w.count >= DryRunsPerMinute {
			return false
		}
	}
	for _, k := range keys {
		w := l.windows[k]
		if now.Sub(w.start) >= time.Minute {
			w = dryRunWindow{start: now}
		}
		w.count++
		l.windows[k] = w
	}
	return true
}

func errDryRunRateExceeded() *actiongate.Error {
	e := actiongate.Refusal("DRY_RUN_RATE_EXCEEDED", "too many command-proposal dry runs; wait a minute and try again", false)
	e.Retryable = true
	return e
}
