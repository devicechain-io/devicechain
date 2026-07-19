// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"context"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	nats "github.com/nats-io/nats.go"
)

// The command round trip has never been exercised. Its consumer, its stream, and
// its subjects were each individually correct, and two ADRs described the whole as
// shipped — which is what happens when every part is reviewed and the seam between
// them never runs. These tests drive the real writer against a real JetStream
// server so the shapes are proven to agree rather than argued to.
//
// What is deliberately NOT covered here: the MQTT hop. nats-server maps topic
// "a/b/c" to subject "a.b.c" with no prefix, which is its behavior and not ours.
// What IS ours — and what silently breaks — is whether the subject we PUBLISH to
// falls inside the stream we CREATE and the filter a device is GRANTED. That is
// what runs below.

// deviceSubscriber binds a JetStream consumer to exactly the subject a device is
// granted, which is how a device sees its commands. Using the granted subject as
// the filter is the point: it makes the test fail if authorization and addressing
// ever disagree, rather than testing addressing against itself.
func deviceSubscriber(t *testing.T, js nats.JetStreamContext, filter string) chan *nats.Msg {
	t.Helper()
	received := make(chan *nats.Msg, 8)
	sub, err := js.Subscribe(filter, func(m *nats.Msg) {
		received <- m
		_ = m.Ack()
	}, nats.DeliverAll())
	if err != nil {
		t.Fatalf("subscribe %q: %v", filter, err)
	}
	t.Cleanup(func() { _ = sub.Unsubscribe() })
	return received
}

func TestCommandReachesOnlyItsOwnDevice(t *testing.T) {
	nmgr, js, cleanup := newRoundTripManager(t)
	defer cleanup()

	const tenant = "acme"
	writer, err := nmgr.NewWriter(SubjectDeviceCommands)
	if err != nil {
		t.Fatalf("new writer: %v", err)
	}

	// The subjects each device is granted. natsauth builds these with
	// DeviceScopedSubject, and its own tests assert the grant equals this shape —
	// so filtering on them here chains authorization to delivery.
	mine := DeviceScopedSubject(nmgr.Microservice.InstanceId, tenant, SubjectDeviceCommands, "sensor-001")
	theirs := DeviceScopedSubject(nmgr.Microservice.InstanceId, tenant, SubjectDeviceCommands, "sensor-002")

	minebox := deviceSubscriber(t, js, mine)
	theirbox := deviceSubscriber(t, js, theirs)

	ctx := core.WithTenant(context.Background(), tenant)
	if err := writer.WriteToDevice(ctx, "sensor-001", Message{Value: []byte(`{"name":"reboot"}`)}); err != nil {
		t.Fatalf("write to device: %v", err)
	}

	// The addressed device receives it — which also proves the published subject
	// falls inside the stream ensureStream created. If those two disagreed, nothing
	// would arrive and there would be no error anywhere.
	select {
	case m := <-minebox:
		if string(m.Data) != `{"name":"reboot"}` {
			t.Fatalf("unexpected payload %q", m.Data)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the addressed device received no command; publish subject and stream/filter disagree")
	}

	// No other device sees it. This is the property the whole per-device cutover
	// exists to provide, and before it every device in the tenant received this.
	select {
	case m := <-theirbox:
		t.Fatalf("another device received a command addressed elsewhere: %q on %q", m.Data, m.Subject)
	case <-time.After(500 * time.Millisecond):
	}
}

// A tenant-scoped suffix must still round-trip unchanged — the per-device shape is
// an addition, not a replacement, and command RESPONSES deliberately stay
// tenant-scoped (one subject, one consumer, correlated by command token).
func TestCommandResponseRoundTripsTenantScoped(t *testing.T) {
	nmgr, js, cleanup := newRoundTripManager(t)
	defer cleanup()

	const tenant = "acme"
	writer, err := nmgr.NewWriter(SubjectCommandResponses)
	if err != nil {
		t.Fatalf("new writer: %v", err)
	}

	box := deviceSubscriber(t, js, StreamSubject(nmgr.Microservice.InstanceId, SubjectCommandResponses))

	ctx := core.WithTenant(context.Background(), tenant)
	body := []byte(`{"commandToken":"cmd-1","success":true}`)
	if err := writer.WriteMessages(ctx, Message{Value: body}); err != nil {
		t.Fatalf("write: %v", err)
	}

	select {
	case m := <-box:
		if string(m.Data) != string(body) {
			t.Fatalf("unexpected payload %q", m.Data)
		}
		// The consumer derives the tenant from the SUBJECT, never the payload, so a
		// device cannot answer for another tenant.
		if got, ok := ParseTenantFromSubject(m.Subject); !ok || got != tenant {
			t.Fatalf("tenant from subject = %q ok=%v, want %q", got, ok, tenant)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("command response did not round-trip")
	}
}

// WriteMessages must refuse a per-device suffix rather than publish to a subject no
// stream captures. Without this the message vanishes: no error, no delivery, and
// nothing to indicate the addressing was wrong.
func TestTenantWideWriteRefusedForPerDeviceSuffix(t *testing.T) {
	nmgr, _, cleanup := newRoundTripManager(t)
	defer cleanup()

	writer, err := nmgr.NewWriter(SubjectDeviceCommands)
	if err != nil {
		t.Fatalf("new writer: %v", err)
	}

	ctx := core.WithTenant(context.Background(), "acme")
	if err := writer.WriteMessages(ctx, Message{Value: []byte(`{}`)}); err == nil {
		t.Fatal("a tenant-wide write to a per-device suffix must be refused")
	}
}

// A device token carrying subject metacharacters would shift subject levels and let
// one device address another's. The grant refuses such a token; so must the write.
func TestWriteToDeviceRejectsUnsafeToken(t *testing.T) {
	nmgr, _, cleanup := newRoundTripManager(t)
	defer cleanup()

	writer, err := nmgr.NewWriter(SubjectDeviceCommands)
	if err != nil {
		t.Fatalf("new writer: %v", err)
	}

	ctx := core.WithTenant(context.Background(), "acme")
	for _, bad := range []string{"", "a.b", "*", ">", "x.*"} {
		if err := writer.WriteToDevice(ctx, bad, Message{Value: []byte(`{}`)}); err == nil {
			t.Errorf("device token %q must be refused, not spliced into a subject", bad)
		}
	}
}

// newRoundTripManager is newTestManager plus the JetStream context the test also
// subscribes through, so the subscriber sees exactly the server the writer wrote to.
func newRoundTripManager(t *testing.T) (*NatsManager, nats.JetStreamContext, func()) {
	t.Helper()
	nmgr, cleanup := newTestManager(t)
	return nmgr, nmgr.js, cleanup
}
