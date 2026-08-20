// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"context"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/nats-io/nats.go"
)

// TestAddConsumerSilentlyIgnoresAChangedFilterSubject pins the BROKER behaviour that
// reconcileFilterSubject exists to work around, because the whole design rests on it
// and it is not ours to control.
//
// 🔴 IT IS A TEST OF A DEPENDENCY, DELIBERATELY. consumerConfig's warning says a changed
// field makes AddConsumer reject an existing durable — which is true of every field but
// this one. If a future nats-server starts comparing FilterSubject on the create path,
// this test fails, and the failure is the useful signal: the workaround below it can then
// be deleted rather than left to rot as a permanent no-op nobody dares remove.
func TestAddConsumerSilentlyIgnoresAChangedFilterSubject(t *testing.T) {
	js := scratchJetStream(t, "filter-ignored")
	if _, err := js.AddStream(&nats.StreamConfig{
		Name:     "FILTERPIN",
		Subjects: []string{"test.*.responses", "test.*.responses.*"},
	}); err != nil {
		t.Fatalf("add stream: %v", err)
	}
	const durable = "pin"
	narrow := &nats.ConsumerConfig{Durable: durable, AckPolicy: nats.AckExplicitPolicy,
		FilterSubject: "test.*.responses"}
	if _, err := js.AddConsumer("FILTERPIN", narrow); err != nil {
		t.Fatalf("create durable: %v", err)
	}

	deeper := &nats.ConsumerConfig{Durable: durable, AckPolicy: nats.AckExplicitPolicy,
		FilterSubject: "test.*.responses.*"}
	info, err := js.AddConsumer("FILTERPIN", deeper)
	if err != nil {
		t.Fatalf("AddConsumer with a changed filter now REJECTS (%v) — the server compares "+
			"FilterSubject on create, so reconcileFilterSubject is obsolete and should be deleted", err)
	}
	if info.Config.FilterSubject != narrow.FilterSubject {
		t.Fatalf("AddConsumer now MOVES the filter (server reports %q) — reconcileFilterSubject "+
			"is obsolete and should be deleted", info.Config.FilterSubject)
	}

	// The half that makes it matter: the client refuses to bind, so the stale filter is
	// not merely cosmetic — the reader cannot start at all until something moves it.
	if _, err := js.PullSubscribe(deeper.FilterSubject, durable, nats.Bind("FILTERPIN", durable)); err == nil {
		t.Fatal("PullSubscribe bound against a consumer whose filter does not match; the stale " +
			"filter would then be silent rather than loud, which changes what this must guard")
	}
}

// TestReaderMovesAnExistingDurableOntoTheCurrentFilter is the positive half: a durable
// left behind at an older shape is moved by the reader's own bind path, and a message
// published to the NEW subject is delivered through it.
//
// 🔑 THE MESSAGE IS PUBLISHED BEFORE THE READER EXISTS, which is what makes this measure
// the reconcile rather than the subscription. It is captured by the stream (whose
// subjects ensureStream already reconciles) and is reachable only if the consumer's
// filter also moved — so a fetch of zero here is exactly the stranding this prevents.
func TestReaderMovesAnExistingDurableOntoTheCurrentFilter(t *testing.T) {
	js := scratchJetStream(t, "filter-moved")
	if _, err := js.AddStream(&nats.StreamConfig{
		Name:     "FILTERMOVE",
		Subjects: []string{"test.*.responses", "test.*.responses.*"},
	}); err != nil {
		t.Fatalf("add stream: %v", err)
	}
	const durable = "mover"
	// The stale durable is created THROUGH THE READER'S OWN PATH at the old subject
	// shape, not hand-built: an upgrade leaves behind a consumer that this same code
	// wrote, so every field except FilterSubject already matches. A hand-built fixture
	// differing in AckWait would make AddConsumer fail loudly above the reconcile and
	// the test would measure the loud path instead of the silent one.
	oldBuild := readerAt(js, "FILTERMOVE", durable, "test.*.responses")
	if err := oldBuild.bind(); err != nil {
		t.Fatalf("old build bind: %v", err)
	}
	if _, err := js.Publish("test.acme.responses.dev-1", []byte("answer")); err != nil {
		t.Fatalf("publish: %v", err)
	}

	newBuild := readerAt(js, "FILTERMOVE", durable, "test.*.responses.*")
	if err := newBuild.bind(); err != nil {
		t.Fatalf("new build bind against the stale durable: %v", err)
	}
	sub := newBuild.sub.Load()
	if sub == nil {
		t.Fatal("bind reported success but published no subscription")
	}
	msgs, err := sub.Fetch(1, nats.MaxWait(2*time.Second))
	if err != nil || len(msgs) != 1 {
		t.Fatalf("fetched %d messages (err %v) from the moved durable, want 1 — the consumer "+
			"is still filtering for the old subject shape", len(msgs), err)
	}
	if got := msgs[0].Subject; got != "test.acme.responses.dev-1" {
		t.Fatalf("delivered subject = %q, want the per-device subject", got)
	}
}

// readerAt builds the minimal natsReader bind() reads, standing in for a build whose
// StreamSubject resolved to the given filter.
func readerAt(js nats.JetStreamContext, stream, durable, subject string) *natsReader {
	return &natsReader{
		nmgr:    &NatsManager{js: js},
		stream:  stream,
		durable: durable,
		subject: subject,
	}
}

// scratchJetStream gives a test its own embedded server and a JetStream context on it.
func scratchJetStream(t *testing.T, area string) nats.JetStreamContext {
	t.Helper()
	srv := startEmbeddedServer(t)
	ms := testMicroservice(t, srv, uniqueArea(area))
	nmgr := NewNatsManager(ms, core.NewNoOpLifecycleCallbacks(), func(*NatsManager) error { return nil })
	ctx := context.Background()
	if err := nmgr.Initialize(ctx); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	if err := nmgr.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = nmgr.Stop(ctx) })
	return nmgr.js
}

// TestNewReaderSurvivesAnUpgradeFromTheOldSubjectShape is the END-TO-END upgrade proof,
// and it is the one that decides whether this change can ship as an in-place upgrade.
//
// It stages what a v0.11.0 cluster actually leaves behind — a stream and a durable built
// at the TENANT-SCOPED command-responses shape, under the exact names this build computes
// — and then does what the new binary does on startup: NewReader(suffix). Both halves of
// the declaration have to move for that to work, and they are reconciled in different
// places: ensureStream rewrites the stream's captured subjects, bind moves the consumer's
// filter. NewReader runs them in that order, which is required rather than incidental —
// a consumer's filter must overlap its stream's subjects, so moving the filter first
// would be rejected by the server.
//
// 🔴 WITHOUT THIS THE UPGRADE FAILS LOUDLY BUT UNHELPFULLY: AddConsumer reports success
// against the stale durable, PullSubscribe then refuses with "subject does not match
// consumer", and command-delivery cannot start at all. Every device response in the
// instance stops being recorded, and every outstanding command rides to TIMEOUT.
func TestNewReaderSurvivesAnUpgradeFromTheOldSubjectShape(t *testing.T) {
	srv := startEmbeddedServer(t)
	area := uniqueArea("upgrade")
	ms := testMicroservice(t, srv, area)
	nmgr := NewNatsManager(ms, core.NewNoOpLifecycleCallbacks(), func(*NatsManager) error { return nil })
	ctx := context.Background()
	if err := nmgr.Initialize(ctx); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	if err := nmgr.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = nmgr.Stop(ctx) })

	const suffix = SubjectCommandResponses
	instance := ms.InstanceId
	stream := StreamName(instance, suffix)
	durable := DurableName(instance, area, suffix)

	// The OLD shape, exactly as the previous release created it.
	oldSubject := WildcardSubject(instance, suffix)
	if _, err := nmgr.js.AddStream(&nats.StreamConfig{Name: stream, Subjects: []string{oldSubject}}); err != nil {
		t.Fatalf("staging the old stream: %v", err)
	}
	if _, err := nmgr.js.AddConsumer(stream, &nats.ConsumerConfig{
		Durable: durable, AckPolicy: nats.AckExplicitPolicy,
		AckWait: AckWait, MaxDeliver: MaxDeliver, MaxAckPending: readerMaxAckPending,
		FilterSubject: oldSubject,
	}); err != nil {
		t.Fatalf("staging the old durable: %v", err)
	}

	// Premise check: the staged filter really is the old shape, or this test proves
	// nothing about an upgrade.
	if info, err := nmgr.js.ConsumerInfo(stream, durable); err != nil {
		t.Fatalf("consumer info: %v", err)
	} else if info.Config.FilterSubject != oldSubject {
		t.Fatalf("staged filter = %q, want the old %q", info.Config.FilterSubject, oldSubject)
	}

	// The new binary starts.
	reader, err := nmgr.NewReader(suffix)
	if err != nil {
		t.Fatalf("the upgraded build could not open its response reader: %v", err)
	}

	// And a device's response, published at the NEW per-device subject, is delivered.
	writer, err := nmgr.NewWriter(suffix)
	if err != nil {
		t.Fatalf("new writer: %v", err)
	}
	tctx := core.WithTenant(ctx, "acme")
	if err := writer.WriteToDevice(tctx, "sensor-001", Message{Value: []byte(`{"commandToken":"c1"}`)}); err != nil {
		t.Fatalf("write: %v", err)
	}
	readCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	msg, err := reader.ReadMessage(readCtx)
	if err != nil {
		t.Fatalf("the upgraded reader received nothing: %v", err)
	}
	if got, ok := ParseDeviceFromScopedSubject(msg.Subject, suffix); !ok || got != "sensor-001" {
		t.Fatalf("delivered subject %q parsed to %q ok=%v, want sensor-001", msg.Subject, got, ok)
	}
}
