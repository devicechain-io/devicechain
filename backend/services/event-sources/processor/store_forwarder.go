// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"time"

	"github.com/rs/zerolog/log"

	core "github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
)

// StoreForwarder forwards one kind of device publish that is not an event (command responses,
// device reports) from the MQTT gateway's message store into the platform stream its direct
// copy lands in, with infrastructure.nats.mqttFailClosed on.
//
// The direct copy of such a publish lands in that stream already, through the broker's subject
// space; but it is published there with no reply, so a stream that cannot take it loses it
// while the device has been PUBACKed on the store copy. With the fail-closed store in force the
// readers of that stream skip the QoS 1 direct copies the store holds (messaging, see
// mqtt_failclosed_view.go), and this puts the store copy there in their place: the same
// payload on the same subject, published and acknowledged by the stream before the store copy
// is acked, under a dedup id (messaging.MqttMessageStoreDedupID) so a redelivery of it is
// stored once. Responses and reports are a few per command or per device, not telemetry, so it
// publishes one message at a time on the reader's goroutine.
type StoreForwarder struct {
	suffix    string
	readPacer *core.ReadPacer
	reader    messaging.MessageReader
	writer    messaging.MessageWriter

	cancel  context.CancelFunc
	drained chan struct{}
	// failUntil is when the writer may be tried again after a failed publish: until then a
	// message is left unacked at once rather than waiting out another publish timeout, so a
	// fetched batch clears while the stream refuses. Read goroutine only.
	failUntil time.Time
}

// storeForwarderBackoff is how long a failed publish holds the next ones back.
var storeForwarderBackoff = 2 * time.Second

// NewStoreForwarder builds the forwarder of the device-scoped stream suffix, reading reader
// (the store durable of the same kind) and writing writer (a writer to suffix).
//
// ms is the microservice a read loop that gives up is reported to (nil in tests, where the loop
// stops and logs instead), as for the gateway source.
func NewStoreForwarder(ms *core.Microservice, suffix string, reader messaging.MessageReader, writer messaging.MessageWriter) *StoreForwarder {
	return &StoreForwarder{suffix: suffix, reader: reader, writer: writer,
		readPacer: core.NewReadPacer(ms, "mqtt message store "+suffix)}
}

// Start starts the read loop.
func (f *StoreForwarder) Start() {
	ctx, cancel := context.WithCancel(context.Background())
	f.cancel = cancel
	f.drained = make(chan struct{})
	go f.readLoop(ctx, f.drained)
	log.Info().Str("stream", f.suffix).Msg("Forwarding device publishes from the MQTT message store.")
}

// Stop stops the read loop and waits for it, up to ctx. A message it had not acked is left
// for redelivery.
func (f *StoreForwarder) Stop(ctx context.Context) {
	if f.cancel == nil {
		return
	}
	f.cancel()
	select {
	case <-f.drained:
	case <-ctx.Done():
		log.Warn().Str("stream", f.suffix).
			Msg("The MQTT message store forwarder did not stop before the shutdown deadline; what it holds is left for redelivery.")
	}
}

func (f *StoreForwarder) readLoop(ctx context.Context, drained chan struct{}) {
	defer close(drained)
	messaging.RunConsumer(ctx, f.reader, f.readPacer, func(msg messaging.Message) bool {
		f.forward(ctx, msg)
		return true
	})
}

// forward publishes one store message to its stream and acks it once the stream has it.
func (f *StoreForwarder) forward(ctx context.Context, msg messaging.Message) {
	// Unreachable for a message the durable's filter delivered, as on the capture path; kept
	// for the same reason (see GatewayJetStreamSource.handle). ACKED, not left: a message
	// that cannot be addressed is dropped the same way on every delivery (the ACK TRAP).
	tenant, ok := tenantFromSubject(msg.Subject)
	if !ok || core.ValidateToken(tenant) != nil {
		log.Warn().Str("subject", msg.Subject).Msg("Dropping a message store copy with no valid tenant.")
		ackDrop(msg, "no valid tenant")
		return
	}
	device, ok := messaging.ParseDeviceFromScopedSubject(msg.Subject, f.suffix)
	if !ok {
		log.Warn().Str("subject", msg.Subject).Msg("Dropping a message store copy with no parseable device.")
		ackDrop(msg, "no parseable device")
		return
	}
	if time.Now().Before(f.failUntil) {
		return // left unacked: redelivered after AckWait, once the stream takes writes again
	}
	out := messaging.Message{
		Key:     []byte(device),
		Value:   msg.Value,
		DedupID: msg.DedupID, // named by the reader over the store
		Headers: msg.Headers,
	}
	if err := f.writer.WriteToDevice(core.WithTenant(ctx, tenant), device, out); err != nil {
		f.failUntil = time.Now().Add(storeForwarderBackoff)
		// LEFT UNACKED, as on the capture path: AckWait paces the retry, so a downstream
		// outage shorter than MaxDeliver x AckWait is ridden out with nothing lost.
		log.Warn().Err(err).Str("stream", f.suffix).Str("tenant", tenant).Uint64("storeSeq", msg.StreamSeq).
			Int("attempt", msg.NumDelivered).
			Msg("A message store copy was not forwarded; leaving it unacked for AckWait-paced redelivery.")
		return
	}
	if err := msg.Ack(); err != nil {
		log.Warn().Err(err).Str("stream", f.suffix).Uint64("storeSeq", msg.StreamSeq).
			Msg("Failed to ack a forwarded message store copy; it will redeliver and be deduped.")
	}
}
