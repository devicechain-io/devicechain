// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"errors"

	"github.com/rs/zerolog/log"
)

// livenessFailure is why a process stopped being live. A struct rather than the bare
// error so the latch can be a typed atomic pointer: CompareAndSwap on an
// atomic.Value holding errors of different concrete types panics.
type livenessFailure struct {
	reason error
}

// MarkNotLive records that this process has reached a state only a restart can clear,
// and from then on the liveness probe (/healthz) answers 503 so Kubernetes restarts
// the pod.
//
// It is for the case where the process keeps running but can no longer do its job,
// and nothing inside it will ever fix that. The NATS manager is the first caller: a
// broker connection that the client has closed permanently, without this process
// asking, does not come back. The usual cause is a broker credential that changed
// under a running pod, and a restart re-reads the mounted credential.
//
// 🔴 DO NOT CALL IT FOR ANYTHING A RETRY CAN FIX. Liveness failing restarts the pod;
// a dependency that is merely down (a broker restarting, a database failing over)
// restarts it into the same wait, which makes the outage worse. Those states belong
// in readiness or in the component's own retry loop.
//
// Contrast FailNow, which ends the process at once with a non-zero status. MarkNotLive
// leaves the decision to the kubelet, which lets it run from a callback that must not
// block (FailNow runs the whole teardown on the caller's goroutine) and costs one
// liveness window before the restart. Both are chosen on purpose: FailNow for a
// component that knows the process must go now, MarkNotLive for one that knows only
// that it cannot recover.
//
// It is a one-way latch and the FIRST reason wins; later calls change nothing and log
// nothing, so a condition reported from several places produces one ERROR, not a
// stream of them. A nil reason is replaced with one that says none was given, as
// FailNow does, because a nil here would otherwise read as live.
func (ms *Microservice) MarkNotLive(reason error) {
	if reason == nil {
		reason = errors.New("core: a component declared this process not live without saying why")
	}
	if !ms.notLive.CompareAndSwap(nil, &livenessFailure{reason: reason}) {
		return
	}
	log.Error().Err(reason).Msg("This process can no longer do its job and only a restart can clear it; " +
		"the liveness probe (/healthz) now fails so Kubernetes restarts the pod.")
}

// Live returns nil while this process is live, and the first reason passed to
// MarkNotLive once it is not.
func (ms *Microservice) Live() error {
	if f := ms.notLive.Load(); f != nil {
		return f.reason
	}
	return nil
}
