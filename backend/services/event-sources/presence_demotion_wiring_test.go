// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"testing"

	"github.com/devicechain-io/dc-event-sources/presence"
	"github.com/stretchr/testify/require"
)

// TestOnlyInstanceWideEvidenceStartsADrain. Releasing a fleet writes two durable events
// per device to undo, so the evidence has to be about the INSTANCE, not about this
// replica. A written `enabled: false` and a missing system-account credential are
// configuration — every replica reads the same values and reaches the same conclusion.
//
// An unreachable broker is here too, and it is the one worth stating: the dial retries for
// systemAccountConnectWait before it reports failure, so this reason means the system
// account was unreachable for that whole window, which is a broker that is down or a
// credential the broker refuses — both instance-wide. The MQTT gateway is in that same
// broker, so an unreachable one also means no device is connected through it.
//
// A failed subscription on a CONNECTED conn stays replica-local: its peers may be reading
// advisories perfectly well.
func TestOnlyInstanceWideEvidenceStartsADrain(t *testing.T) {
	for _, r := range []presence.TapOffReason{presence.TapOffDisabled, presence.TapOffNoSystemCredential,
		presence.TapOffBrokerUnreachable} {
		require.True(t, reasonIsInstanceWide(r), "reason %s should start a drain", r)
	}
	for _, r := range []presence.TapOffReason{presence.TapOffNoGatewaySource, presence.TapOffNoServiceAuth,
		presence.TapOffSubscribeFailed} {
		require.False(t, reasonIsInstanceWide(r), "reason %s must not start a drain", r)
	}
	// Every declared reason is classified — a new one added without a decision here would
	// silently fall on the do-nothing side, which is the safe direction but an unexamined one.
	for _, r := range presence.AllTapOffReasons() {
		_ = reasonIsInstanceWide(r)
	}
}

// TestDrainPreconditionsAreCheckedNotInferred is the cross-product the ordered bail switch
// makes necessary.
//
// 🔴 `!cfg.IsEnabled()` RETURNS BEFORE GatewaySourceId OR ServiceAuth ARE EVER READ, so
// arriving on that branch says NOTHING about whether they are set. An implementation that
// inferred them from the branch would work on the no-service-auth path (which by
// definition has service auth missing) and be silently wrong on the disabled path — where
// the drain would start, fail every pass, and log an error loop instead of pointing the
// operator at the manual door.
func TestDrainPreconditionsAreCheckedNotInferred(t *testing.T) {
	const (
		src    = "mqtt1"
		secret = "s3cret"
		um     = "user-management"
		ds     = "device-state"
		port   = uint32(8080)
	)
	require.True(t, drainEndpointsReady(src, secret, um, port, ds, port), "the fully-configured case must be ready")

	// Each field is independently load-bearing: the drain emits under the source name,
	// mints a token with the secret, enumerates tenants through user-management and reads
	// the projection from device-state. BOTH endpoints are a host AND a port — a zero port
	// renders "http://host:0/graphql", which fails every call rather than being absent.
	cases := map[string]bool{
		"no gateway source":       drainEndpointsReady("", secret, um, port, ds, port),
		"no service secret":       drainEndpointsReady(src, "", um, port, ds, port),
		"no user-management host": drainEndpointsReady(src, secret, "", port, ds, port),
		"no user-management port": drainEndpointsReady(src, secret, um, 0, ds, port),
		"no device-state host":    drainEndpointsReady(src, secret, um, port, "", port),
		"no device-state port":    drainEndpointsReady(src, secret, um, port, ds, 0),
		"nothing configured":      drainEndpointsReady("", "", "", 0, "", 0),
		"only the source":         drainEndpointsReady(src, "", "", 0, "", 0),
		"everything but source":   drainEndpointsReady("", secret, um, port, ds, port),
	}
	for name, ready := range cases {
		require.False(t, ready, "%s: the drain reported itself ready without what it needs", name)
	}
}
