// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"context"
	"testing"

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

// TestAnEmptyTenantIsRefusedBeforeAnythingIsPurged is the guard that cannot be walked back.
//
// 🔴 A STREAM PURGE WITH NO SUBJECT FILTER DELETES EVERY TENANT'S MESSAGES, and JetStream
// does it without complaint. An empty token is how that happens by accident — it builds a
// filter of `inst1..>`, or worse gets trimmed away entirely — so it is refused before any
// request is sent, and the bystander's messages are asserted still present afterwards
// rather than the refusal being taken on trust.
func TestAnEmptyTenantIsRefusedBeforeAnythingIsPurged(t *testing.T) {
	nc, js := purgeRig(t)
	suffix := streams.Suffixes()[0]
	name := seedStream(t, js, suffix, purgeVictim, purgeBystander)

	for _, empty := range []string{"", "   "} {
		_, err := PurgeTenant(context.Background(), nc, purgeInstance, empty)
		require.Errorf(t, err, "an empty tenant token must never reach a purge filter (%q)", empty)
	}
	_, err := PurgeTenant(context.Background(), nc, "", purgeVictim)
	require.Error(t, err, "every subject is rooted at the instance id; without one nothing matches")

	assert.Equal(t, uint64(2), streamMsgs(t, js, name),
		"the refusals must have happened BEFORE any request was sent — both tenants' messages "+
			"are still there, which is what proves nothing was purged on the way to the error")
}

// TestTheDeferralsNameWhatSurvives pins the sentences that go into the deletion record.
//
// A deferral's whole value is that a reader can act on it. One that named a stream without
// saying what it holds would still block completion — so nothing would fail — while the
// record told an auditor a purge is incomplete without telling them what is retained.
func TestTheDeferralsNameWhatSurvives(t *testing.T) {
	deferrals := TenantPurgeDeferrals()
	require.NotEmpty(t, deferrals,
		"the session streams, the orphaned consumers and the retained cache are all still "+
			"outstanding; an empty list here would let a purge claim total erasure")

	for _, d := range deferrals {
		assert.Greaterf(t, len(d), 80, "a deferral must describe the data, not label it: %q", d)
	}
	joined := ""
	for _, d := range deferrals {
		joined += d
	}
	for _, must := range []string{"$MQTT_sess", "$MQTT_qos2in", "consumer", "retained"} {
		assert.Containsf(t, joined, must,
			"nothing in the deferrals mentions %q, so that hole is invisible in the record", must)
	}
}
