// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	nats "github.com/nats-io/nats.go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/devicechain-io/dc-microservice/streams"
)

const (
	purgeInstance  = "inst1"
	purgeVictim    = "victim-tenant"
	purgeBystander = "bystander-tenant"
)

// purgeRig starts an in-process JetStream server and returns a connection to it.
//
// A real server rather than a fake, because everything this file is about happens inside
// JetStream: whether a subject filter matches, what a purge counts, and what the server
// says about a stream that is not there. A fake would answer whatever it was written to
// answer, which is the same as asserting the code against itself.
func purgeRig(t *testing.T) (*nats.Conn, nats.JetStreamContext) {
	t.Helper()
	srv := runTestServer(t)
	nc, err := nats.Connect(srv.ClientURL())
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	js, err := nc.JetStream()
	require.NoError(t, err)
	return nc, js
}

// seedStream creates the stream a suffix would have and publishes one message per tenant
// on the concrete subject a producer would use.
func seedStream(t *testing.T, js nats.JetStreamContext, suffix string, tenants ...string) string {
	t.Helper()
	name := StreamName(purgeInstance, suffix)
	_, err := js.AddStream(&nats.StreamConfig{
		Name:     name,
		Subjects: []string{StreamSubject(purgeInstance, suffix)},
		Storage:  nats.MemoryStorage,
	})
	require.NoErrorf(t, err, "creating %s", name)
	for _, tenant := range tenants {
		subject := ConcreteSubjectFor(purgeInstance, tenant, suffix, "dev-1")
		_, err := js.Publish(subject, []byte("payload for "+tenant))
		require.NoErrorf(t, err, "publishing %s", subject)
	}
	return name
}

func streamMsgs(t *testing.T, js nats.JetStreamContext, name string) uint64 {
	t.Helper()
	info, err := js.StreamInfo(name)
	require.NoError(t, err)
	return info.State.Msgs
}

// TestPurgeTenantReachesEveryDeclaredStreamShape is the assertion the whole file exists
// for, and it runs over the REAL inventory rather than a chosen example.
//
// 🔑 THE SHAPES ARE THE POINT. A tenant's subject is `{inst}.{tenant}.{suffix}` on most
// streams, `{inst}.{tenant}.{suffix}.{device}` on the per-device ones, and
// `{inst}.{tenant}.devices.{device}.events` on the capture stream — where the suffix
// naming the stream appears nowhere in the subject at all. A filter built for the first
// shape matches NOTHING on the third, and a purge that matches nothing reports success.
// So every declared suffix is seeded and every one is checked.
//
// The bystander is the control. A purge with a broken filter — or no filter — satisfies
// "the victim's messages are gone" completely.
func TestPurgeTenantReachesEveryDeclaredStreamShape(t *testing.T) {
	nc, js := purgeRig(t)

	seeded := map[string]string{}
	for _, suffix := range streams.Suffixes() {
		seeded[suffix] = seedStream(t, js, suffix, purgeVictim, purgeBystander)
	}
	require.NotEmpty(t, seeded, "the declared inventory is empty, so this test measures nothing")

	// The seed has to be proven before its absence can mean anything.
	for suffix, name := range seeded {
		require.Equalf(t, uint64(2), streamMsgs(t, js, name),
			"%s did not receive both tenants' messages — the erasure assertions would be vacuous",
			suffix)
	}

	res, err := PurgeTenant(context.Background(), nc, purgeInstance, purgeVictim)
	require.NoError(t, err)

	assert.Equal(t, int64(len(seeded)), res.Messages,
		"exactly one message per declared stream belonged to the victim, so a smaller total means "+
			"a shape whose filter matched nothing")

	for suffix, name := range seeded {
		assert.Equalf(t, uint64(1), streamMsgs(t, js, name),
			"%s (%s) should hold only the bystander's message", suffix, streams.ShapeOf(suffix))
		assert.Containsf(t, res.PerStream, name,
			"%s contributed nothing to the report, so its filter matched no messages — for the "+
				"%s shape that is what a wrongly-built subject looks like", suffix, streams.ShapeOf(suffix))
	}
}

// TestPurgeTenantIsIdempotent covers the Store contract: it is called on every pass until
// the purge completes, so the second call has to succeed and report nothing.
func TestPurgeTenantIsIdempotent(t *testing.T) {
	nc, js := purgeRig(t)
	suffix := streams.Suffixes()[0]
	seedStream(t, js, suffix, purgeVictim)

	first, err := PurgeTenant(context.Background(), nc, purgeInstance, purgeVictim)
	require.NoError(t, err)
	require.NotZero(t, first.Messages)

	second, err := PurgeTenant(context.Background(), nc, purgeInstance, purgeVictim)
	require.NoError(t, err, "a repeat pass is the normal case, not an error")
	assert.Zero(t, second.Messages)
	assert.Empty(t, second.PerStream)
}

// TestPurgingAnAbsentStreamIsNotAnError pins the lazy-creation case.
//
// Streams are created on first use — platform ones by ensureStream, the MQTT gateway's by
// nats-server itself — so an instance that has never used MQTT has no gateway streams, and
// an area that never started has none of its own. Treating that as a failure would block
// every purge on a partial deployment, which is most of them.
func TestPurgingAnAbsentStreamIsNotAnError(t *testing.T) {
	nc, _ := purgeRig(t)

	res, err := PurgeTenant(context.Background(), nc, purgeInstance, purgeVictim)

	require.NoError(t, err, "no stream exists at all; there is nothing to fail on")
	assert.Zero(t, res.Messages)
}

// TestAHostileTenantTokenIsRefusedBeforeAnythingIsPurged is the guard that cannot be
// walked back.
//
// 🔴 A PURGE THAT MATCHES TOO MUCH DELETES OTHER TENANTS' MESSAGES, and JetStream does it
// without complaint or undo. The failure mode is not a filter that breaks — it is one that
// WIDENS. So the token is refused before any filter is built, and the bystander's messages
// are asserted still present afterwards rather than the refusal being taken on trust.
func TestAHostileTenantTokenIsRefusedBeforeAnythingIsPurged(t *testing.T) {
	nc, js := purgeRig(t)
	suffix := streams.Suffixes()[0]
	name := seedStream(t, js, suffix, purgeVictim, purgeBystander)

	// 🔴 EVERY ONE OF THESE WIDENS THE FILTER RATHER THAN BREAKING IT. `>` matches the rest
	// of the subject, `*` matches a token, a `.` adds a level, and an empty token collapses
	// the tenant level entirely — so each would purge messages belonging to tenants that
	// were never deleted, irrecoverably. ADR-042's grammar stops them reaching storage;
	// this asserts they are also stopped at the point of use, because "unreachable" is a
	// claim about every write path that has ever existed.
	for _, hostile := range []string{"", "   ", ">", "*", "acme.>", "acme.*", "acme evil"} {
		_, err := PurgeTenant(context.Background(), nc, purgeInstance, hostile)
		require.Errorf(t, err, "%q must never reach a purge filter", hostile)
	}
	_, err := PurgeTenant(context.Background(), nc, "", purgeVictim)
	require.Error(t, err, "every subject is rooted at the instance id; without one nothing matches")

	assert.Equal(t, uint64(2), streamMsgs(t, js, name),
		"the refusals must have happened BEFORE any request was sent — both tenants' messages "+
			"are still there, which is what proves nothing was purged on the way to the error")

	// And the control: a well-formed token is not caught by the same guard, or the guard
	// would be refusing every purge and this test would pass over a store that never works.
	res, err := PurgeTenant(context.Background(), nc, purgeInstance, purgeVictim)
	require.NoError(t, err)
	assert.NotZero(t, res.Messages, "a legitimate token must still purge")
}

// TestAnUnattributableSessionDoesNotBlockThePurge is the regression test for the defect
// that nearly shipped in this slice, and it is the most important one in the file.
//
// 🔴 A SESSION RECORD THAT NO TENANT OWNS MUST NOT STOP A PURGE COMPLETING. An earlier draft
// reported one as a deferral, which is what Outcome.Clean() reads, which means the broker
// store could never go clean and no purge could ever finish. That is not a corner case:
// nats-server writes a session record on EVERY connect, clean sessions included, and clears
// it only at disconnect — so the edge agent's permanently-connected uplink holds one at all
// times, and every tenant's purge on every ADR-068 instance would have hung on it forever,
// reintroducing the exact condition this slice exists to remove.
//
// The count is still expected, because the assumption behind leaving it alone (that a
// non-device-shaped record cannot be inherited by a device) is worth watching.
func TestAnUnattributableSessionDoesNotBlockThePurge(t *testing.T) {
	nc, js := purgeRig(t)
	seedStream(t, js, streams.Suffixes()[0], purgeVictim, purgeBystander)
	_, err := js.AddStream(&nats.StreamConfig{
		Name:     MqttSessionStore,
		Subjects: []string{mqttSessSubjectPrefix + ">"},
		Storage:  nats.MemoryStorage,
	})
	require.NoError(t, err)
	// The uplink's real shape (edge/dc-edge-agent/agent/uplink.go), not an invented one.
	rec, err := json.Marshal(mqttSessionRecord{ID: "dc-edge-agent-" + purgeInstance + "-site-42"})
	require.NoError(t, err)
	_, err = js.Publish(mqttSessSubjectPrefix+"abcd1234", rec)
	require.NoError(t, err)

	res, err := PurgeTenant(context.Background(), nc, purgeInstance, purgeVictim)
	require.NoError(t, err)
	require.NotZero(t, res.Messages, "the purge must have done something, or this proves nothing")
	assert.EqualValues(t, 1, res.Gateway.UnownedSessions,
		"the record must still be counted — it is the canary for a device session filed before "+
			"the client id was pinned")
}

// TestTheMqttGatewayStreamsArePurgedByTheirWrappedSubject covers the one leg that cannot
// go through ConcreteSubjectFor.
//
// 🔴 WITHOUT IT, NOTHING FAILS WHEN THIS LEG IS WRONG. The gateway's streams are created
// by nats-server, not by us, so no test that seeds the platform inventory ever brings them
// into existence — and a purge against an absent stream is tolerated by design. A typo in
// the hand-written prefix, or a server release that changes the wrapping, therefore purges
// nothing, reports success, and leaves every other test in this file green. That is the
// precise "matches nothing ≡ nothing to purge" failure the design exists to prevent,
// sitting on the leg the design could not cover.
//
// The prefixes are nats-server's (`$MQTT.msgs.` and `$MQTT.rmsgs.`, wrapping the original
// subject), so the streams are built here with those real shapes rather than ours.
func TestTheMqttGatewayStreamsArePurgedByTheirWrappedSubject(t *testing.T) {
	nc, js := purgeRig(t)

	for _, m := range mqttTenantStreams {
		_, err := js.AddStream(&nats.StreamConfig{
			Name:     m.stream,
			Subjects: []string{m.prefix + ">"},
			Storage:  nats.MemoryStorage,
		})
		require.NoErrorf(t, err, "creating %s", m.stream)

		// The subject a device's telemetry actually lands on, wrapped the way the gateway
		// wraps it.
		for _, tenant := range []string{purgeVictim, purgeBystander} {
			_, err := js.Publish(m.prefix+DeviceEventsSubject(purgeInstance, tenant, "dev-1"),
				[]byte("payload for "+tenant))
			require.NoError(t, err)
		}
		require.Equalf(t, uint64(2), streamMsgs(t, js, m.stream),
			"%s did not receive both tenants' messages — the assertions below would be vacuous",
			m.stream)
	}

	res, err := PurgeTenant(context.Background(), nc, purgeInstance, purgeVictim)
	require.NoError(t, err)

	for _, m := range mqttTenantStreams {
		assert.Containsf(t, res.PerStream, m.stream,
			"%s contributed nothing, so its filter matched no messages — which is exactly what a "+
				"wrong prefix looks like, and it would otherwise report success forever", m.stream)
		assert.Equalf(t, uint64(1), streamMsgs(t, js, m.stream),
			"%s should hold only the bystander's message", m.stream)
	}
}

// TestADisconnectedBrokerFailsThePassRatherThanHangingIt is a regression test for a stall
// that had no symptom.
//
// The manager connects with infinite reconnects, so a request issued while the broker is
// down is BUFFERED, not refused — the call blocks until the broker comes back. That call
// is on the purge coordinator's single goroutine, inside the advisory lock, so one broker
// outage stops every store for every purging tenant, with no error and no log line. It is
// reachable on the very first pass if the pod starts while the broker is down.
//
// 🔑 IT ASSERTS THE PROPERTY, NOT THE MECHANISM, because two guards can satisfy it and
// which one fires is a race: the client only learns it is disconnected when a ping fails,
// so IsConnected catches an outage it has already noticed and the deadline catches the
// rest. Both were verified separately by removing the other — deadline alone returns at
// PurgeTimeout, and with neither this test hangs and fails. The deadline is what carries
// the guarantee; IsConnected only makes the common case immediate.
func TestADisconnectedBrokerFailsThePassRatherThanHangingIt(t *testing.T) {
	srv := runTestServer(t)
	nc, err := nats.Connect(srv.ClientURL(), nats.MaxReconnects(-1), nats.RetryOnFailedConnect(true))
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	srv.Shutdown()

	done := make(chan error, 1)
	go func() {
		_, err := PurgeTenant(context.Background(), nc, purgeInstance, purgeVictim)
		done <- err
	}()

	select {
	case err := <-done:
		require.Error(t, err, "a purge against a broker that is not there has not purged anything")
	case <-time.After(PurgeTimeout + 10*time.Second):
		t.Fatal("PurgeTenant did not return with the broker down — it is waiting for a reconnect " +
			"that may never come, on the goroutine that runs every tenant's purge")
	}
}
