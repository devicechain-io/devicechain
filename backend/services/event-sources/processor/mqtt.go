// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/devicechain-io/dc-event-sources/model"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/rs/zerolog/log"

	mqtt "github.com/eclipse/paho.mqtt.golang"
)

const (
	TYPE_MQTT           = "mqtt"
	DECODE_WORKER_COUNT = 5
	// DECODE_CHANNEL_DEPTH bounds the in-flight decode queue. On the capture-stream
	// source it is one of the stages a message taken from the stream can sit in UNACKED,
	// and so be redelivered after a crash — bounded loss became bounded REDELIVERY. The
	// whole bound there is the reader's fetch batch, plus this queue, plus one message per
	// decode worker, plus the one the submitter is placing, plus CAPTURE_PUBLISH_WINDOW
	// awaiting their PubAck, plus POISON_ROUTE_LIMIT being routed to failed-decode; the
	// capture dedup id stores each redelivered one once.
	DECODE_CHANNEL_DEPTH = 100
	// subscribeTimeout bounds the wait for a SUBACK, on the first connection and on
	// every reconnect. Generous, because the only thing that expires it is a broker that
	// has accepted the connection and then stopped answering — the point of the bound is
	// that the source fails loudly rather than hanging for the life of the pod.
	subscribeTimeout = 30 * time.Second
)

// GatewayTopic is the MQTT topic filter the NATS-gateway event source subscribes
// to for an instance: the device EVENTS shape, and nothing else.
//
// NOTE (ADR-030 amendment): the gateway no longer subscribes over MQTT at all —
// it consumes the capture stream (GatewayJetStreamSource) — so nothing in
// production calls this any more. It is retained deliberately, as the topic half
// of the subject/topic mapping that the broker-interop pin (slice I6) asserts:
// the property that a device's MQTT publish and our JetStream subject filter
// describe the same messages is now load-bearing for ingest and is exactly what a
// nats-server upgrade could break silently. Delete it only together with that pin.
//
// It matches the grant natsauth mints for a device (both are built from
// messaging's one declaration), which is the property that matters: the gateway
// ingests exactly what a device can be authorized to publish, so no subject the
// platform publishes internally can arrive here as telemetry.
//
// It lives in this package, rather than being assembled at the one call site in
// main.go, so the string that actually ships is the string under test — main.go
// is package main and its wiring is not reachable from a test.
func GatewayTopic(instanceId string) string {
	return messaging.SubjectToMqttTopic(messaging.DeviceEventsWildcard(instanceId))
}

// externalClientIDPrefix starts every external-broker source's MQTT client id, so a broker
// operator can still recognise the platform's sessions (the id used to be exactly this).
const externalClientIDPrefix = "devicechain"

// externalClientIDSeparator joins the parts of an external source's client id.
//
// 🔴 ":" AND NOT ".", AND THE DIFFERENCE IS A REFUSED CONNECTION. nats-server refuses a
// client id containing "." (or "*", ">" or whitespace) with CONNACK "identifier rejected",
// so a "."-joined id would fail every source whose broker is a NATS server — including the
// platform's own broker reached by an address the gateway branch does not recognise. ":" is
// legal there, and it is outside the token grammar, which is what makes the split
// unambiguous (messaging's deviceClientIDSeparator is ":" for the same two reasons).
const externalClientIDSeparator = ":"

// reducedClientIDPart is the shape of a source id clientIDPart suffixed: '-' and 8
// lowercase hex digits at the end.
var reducedClientIDPart = regexp.MustCompile(`-[0-9a-f]{8}$`)

// errEmptyClientIDPart is what ExternalMqttClientID returns when a part is empty.
var errEmptyClientIDPart = errors.New("an external MQTT source's client id needs every part")

// ExternalMqttClientID is the MQTT client id an external-broker source connects with:
// devicechain:<instance>:<source>:<replica>. A broker keeps ONE session per client id and
// closes the older connection when a second arrives, so the id must differ for every
// instance, every source and every pod. It used to be the literal "devicechain" for all of
// them, and two pods (which every rolling update creates), two sources or two instances on
// one broker took the session from each other in a loop, losing what arrived meanwhile.
//
// The separator cannot occur inside a part: instanceId and replica must already be in the
// token grammar (letters, digits, '-', '_'), and the source id is reduced to it by
// clientIDPart. Two distinct inputs therefore meet on one id only if two source ids that
// clientIDPart had to suffix reduce alike AND share the first 8 hex digits of their
// sha256: a 32-bit hash collision between two ids of one instance's configuration, not
// anything an id can be written to do.
//
// It refuses an empty part rather than producing an id that collides with a sibling's.
func ExternalMqttClientID(instanceId, sourceId, replica string) (string, error) {
	for _, part := range []struct{ name, value string }{
		{"instance id", instanceId}, {"source id", sourceId}, {"replica", replica},
	} {
		if part.value == "" {
			return "", fmt.Errorf("%w: the %s is empty", errEmptyClientIDPart, part.name)
		}
	}
	for _, part := range []struct{ name, value string }{{"instance id", instanceId}, {"replica", replica}} {
		if err := core.ValidateToken(part.value); err != nil {
			return "", fmt.Errorf("refusing to build an external MQTT client id from an invalid %s: %w", part.name, err)
		}
	}
	return strings.Join([]string{externalClientIDPrefix, instanceId, clientIDPart(sourceId), replica},
		externalClientIDSeparator), nil
}

// clientIDPart reduces a source id to the token grammar. When that changes the id, it
// appends '-' and the first 8 hex digits of sha256(original), so "a:b" and "a-b" (both
// "a-b" after reduction) stay distinct. An id already in the grammar is returned unchanged,
// so the common case ("mqtt1") stays readable in the broker's logs, UNLESS it already ends
// in that suffix's shape: such an id is suffixed in turn, by its own hash. Without that, an
// id written as "a-b-" plus the first 8 hex digits of sha256("a:b") would come out exactly
// as "a:b" does, and the two sources would take one session from each other. So every
// output in the suffixed shape carries the hash of the id it came from.
func clientIDPart(s string) string {
	if core.ValidateToken(s) == nil && !reducedClientIDPart.MatchString(s) {
		return s
	}
	out := make([]rune, 0, len(s))
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			out = append(out, r)
		default:
			out = append(out, '-')
		}
	}
	sum := sha256.Sum256([]byte(s))
	return string(out) + "-" + hex.EncodeToString(sum[:])[:8]
}

type MqttEventSource struct {
	Id         string
	BrokerHost string
	BrokerPort int
	Topic      string

	// opts is the paho configuration, built by the constructor and connected with by
	// ExecuteInitialize. ClientID reads the id from HERE, not from a copy kept beside it,
	// so the id the source reports is the id paho connects with: an owned source refuses a
	// term built with any other id than its own, and that check would watch nothing if it
	// read a field paho never sees.
	opts *mqtt.ClientOptions

	// tlsConfig, when non-nil, dials the broker over TLS (ssl://) and verifies its
	// certificate — the client side of the ADR-025 TLS'd MQTT gateway. nil leaves
	// the connection plaintext (tcp://).
	tlsConfig *tls.Config

	// username/password present the shared service credential when broker auth is
	// enabled (ADR-025). event-sources connects to the MQTT gateway as a trusted
	// service, not a device: presenting the static service login authenticates it
	// statically (exempt from the device callout). Empty = no auth (pre-cutover).
	username string
	password string

	Client  mqtt.Client
	Decoder Decoder

	messages chan rawMessage
	// sendMu makes "is the channel still open?" and the send onto it one step. paho can
	// still be running a message callback when ExecuteStop closes the channel: Disconnect
	// returns on its own quiesce timer, not when paho's workers have stopped, and an owned
	// source's ownership gate still answers yes until the lease is released, which is AFTER
	// Stop. Without this, that callback panics the process with a send on a closed channel,
	// and an owned source stops on every handover, not only at process exit. A send holds
	// the read lock; the close takes the write lock and sets closed.
	sendMu  sync.RWMutex
	closed  bool
	workers []*DecodeWorker
	// workersDone is joined by ExecuteStop, so a stopped source has finished handing on
	// every message it had already taken. An owned source releases its lease after Stop,
	// and a message of this term must not be published after the next owner has begun.
	workersDone sync.WaitGroup

	lifecycle core.LifecycleManager
	received  func(string, []byte)
	decoded   func(string, string, *model.UnresolvedEvent, interface{}, uint64) error
	failed    func(string, string, []byte, error) error
	// allow meters an inbound message against its tenant's ingest rate limit
	// before it is queued for decode; a false return sheds the message. nil
	// disables metering (used by tests that exercise decoding in isolation).
	allow RateGate
	// readings charges a decoded message's readings against its tenant's ingest ceiling,
	// in the decode worker (see ReadingGate). Never nil (the constructor refuses one).
	readings ReadingGate
	// admit reports whether the ingest pipeline is accepting events (see
	// HttpEventSource.admit). Asked after the tenant's own ceiling. Never nil.
	admit func(source string) error
	// owns reports whether this pod still owns the source (see OwnedMqttSource), and is
	// asked FIRST for every message: a message delivered to a pod that has lost the source
	// is dropped, because the pod that took it over is reading the same messages and this
	// path carries no id to store a duplicate once. Never nil.
	owns func() bool

	// fail ends the process for a cause found after startup: a broker that refuses
	// this source's subscription on a reconnect. It must not block its caller, which is
	// one of paho's callback goroutines. Required — see NewMqttEventSource.
	fail func(error)
	// ready carries the FIRST connection's subscribe result to ExecuteStart. Buffered,
	// so onConnect never blocks on a Start that has already given up waiting.
	ready chan error
	// connections counts OnConnect calls. The first connection's result goes to ready;
	// a later one uses the count to tell whether a newer connection has superseded it
	// while its SUBSCRIBE was outstanding. See classifyResubscribe.
	connections atomic.Uint64
	// stopping is set before this source disconnects on purpose, so a re-subscribe that
	// errors because WE closed the connection is dropped without a word. Belt and braces:
	// paho's Disconnect fails an outstanding SUBSCRIBE with a token error, which
	// classifyResubscribe already retries, and a fail that races Stop lands in a
	// lifecycle that is already stopping and is ignored there. What the flag buys is that
	// onConnect does not depend on either of those staying true.
	stopping atomic.Bool
}

// errNoFailHook is what NewMqttEventSource returns for a nil fail. A source with no way
// to end the process would have to treat a refused re-subscribe as a log line, which is
// the silent connected-and-idle state this hook exists to rule out.
var errNoFailHook = errors.New("an MQTT event source needs a way to end the process " +
	"when a reconnect's subscription is refused; none was given")

// errNoAdmit is what the HTTP and external-MQTT constructors return for a nil admit. A
// source built without asking whether the pipeline is accepting would publish into a
// stream that is refusing, and the call site would not show that the question was left out.
var errNoAdmit = errors.New("an event source needs to ask whether the ingest pipeline is accepting " +
	"events (backpressure); none was given")

// errNoReadingGate is what the HTTP and external-MQTT constructors return for a nil reading
// gate. A source built without it would charge every message one unit however many readings
// it carried, and the call site would not show that the charge was left out. The admit check
// comes first, so a constructor given neither reports errNoAdmit.
var errNoReadingGate = errors.New("an event source needs a reading gate to charge each decoded " +
	"message's readings against its tenant's ingest ceiling; none was given")

// errNoClientID is what NewMqttEventSource returns for an empty client id.
var errNoClientID = errors.New("an MQTT event source needs its own client id: a broker keeps one " +
	"session per id, so a shared or empty one is taken over by every other connection that uses it")

// errNoOwnerGate is what NewMqttEventSource returns for a nil owns. A source that could not
// ask whether it still owns its broker would go on storing what a pod that took it over is
// storing too.
var errNoOwnerGate = errors.New("an MQTT event source needs to ask whether this pod still owns it; " +
	"none was given")

// Create a new MQTT event source based on the given configuration. tlsConfig is
// non-nil when the broker terminates TLS on the MQTT gateway (ADR-025), in which
// case the client dials ssl:// and verifies the server; nil dials plaintext.
// username/password present the shared service credential when broker auth is on
// (empty = anonymous). clientID is the MQTT client id it connects with (see
// ExternalMqttClientID); empty is refused. owns is asked for every message whether this
// pod still owns the source (see OwnedMqttSource); nil is refused. fail ends the process
// when a reconnect's subscription is refused (see onConnect); it must return promptly,
// and nil is refused.
func NewMqttEventSource(id string, clientID string, config map[string]string, tlsConfig *tls.Config,
	username, password string, decoder Decoder,
	received func(string, []byte),
	decoded func(string, string, *model.UnresolvedEvent, interface{}, uint64) error,
	failed func(string, string, []byte, error) error,
	allow RateGate, readings ReadingGate, admit func(source string) error, owns func() bool,
	fail func(error)) (*MqttEventSource, error) {
	if fail == nil {
		return nil, errNoFailHook
	}
	if admit == nil {
		return nil, errNoAdmit
	}
	if readings == nil {
		return nil, errNoReadingGate
	}
	if clientID == "" {
		return nil, errNoClientID
	}
	if owns == nil {
		return nil, errNoOwnerGate
	}
	port, err := strconv.Atoi(config["port"])
	if err != nil {
		return nil, err
	}

	es := &MqttEventSource{
		Id:         id,
		BrokerHost: config["host"],
		BrokerPort: port,
		Topic:      config["topic"],
		tlsConfig:  tlsConfig,
		username:   username,
		password:   password,
		Decoder:    decoder,
		fail:       fail,
		ready:      make(chan error, 1),
	}

	es.lifecycle = core.NewLifecycleManager("mqtt-event-source", es, core.NewNoOpLifecycleCallbacks())
	es.received = received
	es.decoded = decoded
	es.failed = failed
	es.allow = allow
	es.readings = readings
	es.admit = admit
	es.owns = owns
	es.opts = es.clientOptions(clientID)
	return es, nil
}

// clientOptions is the paho configuration the source connects with.
func (es *MqttEventSource) clientOptions(clientID string) *mqtt.ClientOptions {
	opts := mqtt.NewClientOptions()
	// ssl:// + a verified TLS config when the gateway terminates TLS (ADR-025),
	// otherwise plaintext tcp://.
	scheme := "tcp"
	if es.tlsConfig != nil {
		scheme = "ssl"
		opts.SetTLSConfig(es.tlsConfig)
	}
	opts.AddBroker(fmt.Sprintf("%s://%s:%d", scheme, es.BrokerHost, es.BrokerPort))
	opts.SetClientID(clientID)
	// Present the service credential when broker auth is enabled (ADR-025) so the
	// gateway authenticates this connection statically rather than routing it
	// through the device callout.
	if es.username != "" {
		opts.SetUsername(es.username)
		opts.SetPassword(es.password)
	}
	opts.SetDefaultPublishHandler(es.onMessage)
	// Every connection subscribes, the first included; see onConnect.
	opts.OnConnect = es.onConnect
	opts.OnConnectionLost = es.onConnectionLost
	return opts
}

// ClientID is the MQTT client id this source connects with, read from the options paho
// is given.
func (es *MqttEventSource) ClientID() string { return es.opts.ClientID }

// Owns asks the ownership gate the source was built with, the one every message is gated
// on, whether this pod still owns the source. OwnedMqttSource asks it of the source its
// factory builds, to refuse a factory that wired any gate but the term's.
func (es *MqttEventSource) Owns() bool { return es.owns() }

// tenantFromTopic derives the tenant from an inbound MQTT topic of the form
// "{instanceId}/{tenant}/..." (ADR-006/ADR-048): the tenant is the second of at
// least three non-empty slash-separated segments. Only the second segment is read,
// so this is agnostic to the (instance-id) prefix. Parsed directly (no whole-string
// rewrite) since this runs per inbound message.
func tenantFromTopic(topic string) (string, bool) {
	parts := strings.SplitN(topic, "/", 3)
	if len(parts) < 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return "", false
	}
	return parts[1], true
}

// commandPlaneSuffixes are the device-plane topics that carry COMMAND traffic
// rather than device events: the platform's downlink to devices, and a device's
// response to a command. Both are consumed by command-delivery over its own
// JetStream durable.
//
// The gateway source no longer needs this: its subscription is now the device
// EVENTS shape (messaging.DeviceEventsWildcard), which command topics cannot
// match. It is kept for the EXTERNAL-broker source, whose topic is operator-
// configured and defaults to the permissive "+/#" — there, this service genuinely
// has not been told the topic shape, so a denylist of what is definitely not an
// event is the only exclusion available.
//
// Treat it as a second line of defence, not the mechanism. A denylist can only
// exclude what it has been told to name: this one named the two command suffixes
// and stayed silent about the other thirteen internal ones, which is how the
// gateway ended up re-ingesting its own traffic. The subscription is what closes
// that class; this only narrows the one case where a narrow subscription is not
// available.
//
// The names come from core, not from literals here: command-delivery owns these
// subjects, and a copy of the strings in this file would let a rename there turn
// this recognition off silently, with every test still passing.
var commandPlaneSuffixes = map[string]struct{}{
	messaging.SubjectDeviceCommands:   {},
	messaging.SubjectCommandResponses: {},
	// The reserved configuration channels share the tree too; neither is telemetry.
	messaging.SubjectDeviceDesired: {},
	messaging.SubjectDeviceReports: {},
}

// deviceFromTopic returns the device token an events topic addresses, for the
// documented shape "{instanceId}/{tenant}/devices/{token}/events". The broker grant
// confines a device to its OWN such topic, so this token is authorized rather than
// merely asserted — which is what makes it worth checking the payload against.
//
// Returns "" for any other topic shape, which means "the transport carried no device
// identity", not "no check needed" — the caller treats it as nothing to compare.
// The segment literals come from core (messaging.SegmentDevices / SegmentEvents),
// which is the same declaration the broker grant and the gateway subscription are
// built from — so this parser cannot recognise a shape the subscription no longer
// delivers, or miss one it does.
func deviceFromTopic(topic string) string {
	parts := strings.Split(topic, "/")
	if len(parts) != messaging.DeviceEventsSegmentCount ||
		parts[messaging.DeviceEventsDevicesIndex] != messaging.SegmentDevices ||
		parts[messaging.DeviceEventsEventsIndex] != messaging.SegmentEvents {
		return ""
	}
	return parts[messaging.DeviceEventsTokenIndex]
}

// isCommandPlane reports whether a topic addresses command traffic rather than a
// device event, by matching the segment immediately after {instanceId}/{tenant}.
func isCommandPlane(topic string) bool {
	parts := strings.Split(topic, "/")
	if len(parts) < 3 {
		return false
	}
	_, found := commandPlaneSuffixes[parts[2]]
	return found
}

// Called when message is received from topic.
func (es *MqttEventSource) onMessage(client mqtt.Client, msg mqtt.Message) {
	// Record the arrival by topic and SIZE, never by content. Nothing here has
	// decoded the body yet, so this line cannot tell what the body holds — under
	// deviceAuthMode=required an inbound event normally carries the device's
	// credential, and an operator raising the log level to diagnose an ingest
	// problem is not choosing to copy that into a log pipeline. Topic and byte
	// count are what distinguishes "nothing is arriving" from "messages are
	// arriving and being dropped further down", which is what this line is read
	// for; every drop below reports its own reason.
	if log.Debug().Enabled() {
		log.Debug().Str("topic", msg.Topic()).Int("bytes", len(msg.Payload())).
			Msg("Received MQTT message")
	}
	// First, before anything is metered or counted: a pod that no longer owns this source
	// drops what it is still being delivered, because the pod that has taken the source
	// over receives the same messages, and nothing on this path could store the second
	// copy once. The gate counts the drop itself.
	if !es.owns() {
		return
	}
	// Derive the per-message tenant from the topic up front; a message whose topic
	// carries no tenant cannot be published to a tenant-scoped subject, so it is
	// dropped (fail-closed) rather than decoded and published unscoped.
	tenant, ok := tenantFromTopic(msg.Topic())
	if !ok {
		log.Warn().Msg(fmt.Sprintf("Dropping message with no parseable tenant in MQTT topic %q", msg.Topic()))
		return
	}
	// Validate the tenant token grammar before it is used as a rate-limiter key
	// (fail-closed, mirroring the HTTP path): tenantFromTopic only checks the
	// segment is non-empty, so without this an arbitrary or oversized topic
	// segment could seed an unbounded set of limiter buckets.
	if err := core.ValidateToken(tenant); err != nil {
		log.Warn().Msg(fmt.Sprintf("Dropping message with invalid tenant %q in MQTT topic: %v", tenant, err))
		return
	}

	// Command traffic shares this topic tree but is not telemetry. Drop it BEFORE
	// the rate limiter, because metering it is the actual harm: the gate below
	// spends a tenant's ingest budget, so every command the platform sent and every
	// response a device returned was counted against that tenant's telemetry
	// ceiling — a busy command session could rate-limit the tenant's real events.
	// It also counted as an inbound device event in the RED metrics, and then failed
	// to decode (a command envelope is not an event), so the failure counter rose
	// too. None of that is a device's doing and none of it is an event.
	if isCommandPlane(msg.Topic()) {
		return
	}

	// Meter against the tenant's own ingest ceiling first, before the shared backpressure
	// gate and before enqueue, so a tenant over its limit is shed as its own overage and
	// spends no decode CPU. MQTT has no per-message acknowledgement back to the
	// publisher, so an over-limit message is simply dropped (the HTTP path returns 429
	// instead). A token spent on a message the gate below then drops costs nothing here:
	// this transport has no retry to charge twice.
	if es.allow != nil && !es.allow(es.Id, tenant, time.Time{}, false, OriginAuthenticated) {
		return
	}

	// Drop while the pipeline is applying backpressure. 🔴 THE PUBLISHER IS NOT TOLD: paho
	// acknowledged the message to the external broker before this callback ran (auto-ack,
	// clean session), so this protocol has no lever to make the device retry. The drop is
	// counted by admit (total_msg_backpressured), which is all that can be done on a broker
	// the platform does not own. The platform's own broker keeps such messages instead, in
	// the capture stream (see GatewayJetStreamSource).
	if es.admit(es.Id) != nil {
		return
	}

	// Count the arrival only once it clears the gate, so a shed message is not
	// counted as both inbound and rate-limited (matches the HTTP path, which
	// accounts after the gate). A message the reading stage sheds after decode WAS
	// received: it is counted as inbound and on the reading-shed counters, never on the
	// message stage's rate-limited counter.
	es.sendMu.RLock()
	defer es.sendMu.RUnlock()
	if es.closed {
		// Stop has closed the channel under a callback paho was still running; see sendMu.
		return
	}
	es.received(es.Id, msg.Payload())
	es.messages <- rawMessage{
		tenant:  tenant,
		payload: msg.Payload(),
		device:  deviceFromTopic(msg.Topic()),
		origin:  OriginAuthenticated,
	}
}

// onConnect subscribes on EVERY connection, the first included. paho runs it on a
// goroutine of its own each time a connection comes up.
//
// 🔴 SUBSCRIBING ONCE, IN START, WAS THE DEFECT. paho reconnects on its own after a
// broker restart or a network drop, and it does so with a clean session (paho's default,
// which this source keeps), so the broker holds no subscription for the new connection.
// paho's ResumeSubs does not help: it re-sends only SUBSCRIBEs still in flight, not ones
// already acknowledged. A source that subscribed once was therefore connected, reported
// nothing wrong, and ingested nothing from the first reconnect until the pod restarted.
//
// 🔴 CONFIRMED, not merely awaited, on every connection. paho reports a refused
// subscription (SUBACK 0x80) as a successful token with a nil Error(), so a wait that
// only reads the token logs "subscribed" over a source that will never receive anything;
// see SubscribeMqttConfirmed.
//
// What each outcome does:
//   - The FIRST connection's result goes to ExecuteStart, which fails the start on any
//     error — the behaviour this source has always had.
//   - A later connection's error is classified by classifyResubscribe. A refusal, or a
//     SUBACK that never came on a connection nothing has replaced, ends the process
//     through fail. A connection that went away under the SUBSCRIBE is left to paho,
//     which reconnects and calls this again.
func (es *MqttEventSource) onConnect(client mqtt.Client) {
	generation := es.connections.Add(1)
	err := messaging.SubscribeMqttConfirmed(client, es.Topic, 1, es.onMessage, subscribeTimeout)
	if generation == 1 {
		es.ready <- err
		return
	}
	if err == nil {
		log.Info().Str("source", es.Id).Str("topic", es.Topic).
			Msg("MQTT event source reconnected and re-subscribed.")
		return
	}
	if es.stopping.Load() {
		// Stop disconnected us under the SUBSCRIBE. Not the broker's doing, and the
		// process is already on its way down.
		return
	}
	superseded := es.connections.Load() != generation
	if classifyResubscribe(err, superseded) == resubscribeRetry {
		log.Warn().Err(err).Str("source", es.Id).Str("topic", es.Topic).
			Msg("MQTT event source lost its connection while re-subscribing; the client " +
				"reconnects on its own and subscribes again on the next connection.")
		return
	}
	es.fail(fmt.Errorf("MQTT event source %q reconnected to %s:%d but could not re-subscribe, "+
		"so it would stay connected and ingest nothing: %w", es.Id, es.BrokerHost, es.BrokerPort, err))
}

// resubscribeAction is what onConnect does with a failed re-subscribe.
type resubscribeAction int

const (
	// resubscribeRetry leaves it to paho, which reconnects and fires OnConnect again.
	resubscribeRetry resubscribeAction = iota
	// resubscribeFatal ends the process.
	resubscribeFatal
)

// classifyResubscribe decides a failed re-subscribe from the ERROR and from whether a
// newer connection has come up since the SUBSCRIBE was sent (superseded) — never from
// the client's current connection status.
//
// 🔴 THE STATUS IS A RACE, WHICH IS WHY IT IS NOT CONSULTED. When a connection drops,
// paho fails the outstanding SUBSCRIBE and starts reconnecting, and its first reconnect
// attempt does not wait. So by the time this goroutine wakes with its error, paho may
// already be connected again, and "is the connection open?" answers yes for a
// connection that is not the one the SUBSCRIBE went out on. Read that way, a benign drop
// would end the process — and any connection that evicts this one (another client using
// its id, or a broker restart) would end the process with it.
//
//   - A REFUSAL is fatal whatever else has happened. It is the broker's answer about
//     this credential and this filter, and a newer connection will be given the same one.
//     It is the same condition that fails the source at startup, so it gets the same
//     result: the process ends, the pod restarts, the next start meets the same refusal
//     and fails with the reason in its log — a visible crash loop rather than a pod that
//     reports Ready and ingests nothing. Logging and retrying instead would need a retry
//     loop paho does not provide, with no health signal to report it.
//   - A MISSING SUBACK is fatal only if nothing has replaced the connection it was
//     asked on: that broker took the connection and stopped answering, which is the
//     condition subscribeTimeout exists to turn into a failure. If a newer connection
//     has come up, that connection is subscribing for itself. That second case is
//     DEFENSIVE, not a path paho v1.5.1 takes: on connection loss its internalConnLost
//     runs cleanUpSubscribe before reconnecting, so a SUBSCRIBE outstanding on a lost
//     connection ends as a token error (the default case below), not as a timeout. It
//     is kept so that a paho that stops doing that retries rather than ending the
//     process over a connection already replaced; only the classify table exercises it.
//   - Anything else is paho's token error: the connection went away under the
//     SUBSCRIBE. paho reconnects, and the next OnConnect subscribes again.
func classifyResubscribe(err error, superseded bool) resubscribeAction {
	switch {
	case errors.Is(err, messaging.ErrSubscriptionRefused):
		return resubscribeFatal
	case errors.Is(err, messaging.ErrSubscriptionUnacknowledged):
		if superseded {
			return resubscribeRetry
		}
		return resubscribeFatal
	default:
		return resubscribeRetry
	}
}

// Called when connection is lost. paho reconnects on its own and onConnect
// re-subscribes; this is logged at Warn because ingest from this source stops until then.
func (es *MqttEventSource) onConnectionLost(client mqtt.Client, err error) {
	log.Warn().Err(err).Str("source", es.Id).
		Msg("MQTT event source connection lost; reconnecting.")
}

// Initialize event source
func (es *MqttEventSource) Initialize(ctx context.Context) error {
	return es.lifecycle.Initialize(ctx)
}

// Initialize event source (as called by lifecycle manager)
func (es *MqttEventSource) ExecuteInitialize(ctx context.Context) error {
	// Built, not connected. The connection is made in ExecuteStart, AFTER the decode
	// workers exist: the first connection subscribes at once, and a message delivered
	// on it would otherwise find no channel to go to.
	es.Client = mqtt.NewClient(es.opts)
	log.Info().Str("source", es.Id).Msg("MQTT event source initialized; it connects when started.")
	return nil
}

// Start event source
func (es *MqttEventSource) Start(ctx context.Context) error {
	return es.lifecycle.Start(ctx)
}

// Initialize pool of workers for decoding raw messages.
func (es *MqttEventSource) initializeDecodeWorkers() {
	// Make channels and workers for distributed processing.
	es.messages = make(chan rawMessage, DECODE_CHANNEL_DEPTH)
	es.workers = make([]*DecodeWorker, 0)
	for w := 1; w <= DECODE_WORKER_COUNT; w++ {
		worker := NewDecodeWorker(w, es.Id, es.Decoder, es.messages, es.readings, Inline(es.decoded), es.failed)
		es.workers = append(es.workers, worker)
		es.workersDone.Add(1)
		go func() {
			defer es.workersDone.Done()
			worker.Process()
		}()
	}
}

// Start event source (as called by lifecycle manager)
func (es *MqttEventSource) ExecuteStart(ctx context.Context) error {
	// Initialize pool of workers for decoding raw messages. First, so the channel
	// exists before the first connection can deliver anything.
	es.initializeDecodeWorkers()

	// The connect is waited for under ctx, not with a bare Wait: an owned source starts
	// a term under a context its lease cancels, and a start that outlived the lease would
	// connect a second reader beside the pod that took the source over.
	var err error
	token := es.Client.Connect()
	select {
	case <-token.Done():
		err = token.Error()
	case <-ctx.Done():
		err = ctx.Err()
	}
	if err == nil {
		// The first connection subscribes in onConnect, like every later one; wait for its
		// confirmed result. It is bounded by subscribeTimeout, and a refusal or any other
		// error fails the start, as it always has.
		select {
		case err = <-es.ready:
		case <-ctx.Done():
			err = ctx.Err()
		}
	}
	if err != nil {
		// Do not leave a live, auto-reconnecting client behind a start that failed: its
		// next connection would subscribe again and report into a process that is
		// already going down. stopping first, so that onConnect stays quiet about it.
		es.stopping.Store(true)
		es.Client.Disconnect(250)
		return fmt.Errorf("this event source would ingest nothing: %w", err)
	}
	log.Info().Msg(fmt.Sprintf("MQTT event source subscribed to topic '%s'.", es.Topic))
	return nil
}

// Stop event source
func (es *MqttEventSource) Stop(ctx context.Context) error {
	return es.lifecycle.Stop(ctx)
}

// Stop event source (as called by lifecycle manager)
func (es *MqttEventSource) ExecuteStop(ctx context.Context) error {
	// Quiesce inbound traffic before tearing down the channel: unsubscribe and
	// disconnect the broker client, then close the channel the decode workers drain.
	// The disconnect does not guarantee that paho has stopped calling onMessage (it
	// returns on its own quiesce timer), so the close is made under sendMu, which a
	// late callback finds closed and drops.
	if es.Client != nil {
		// Before the disconnect: a re-subscribe in flight fails when we close the
		// connection, and that is not something to end the process over.
		es.stopping.Store(true)
		// Bounded: a broker that has stopped answering must not hold the stop, and with it
		// an owned source's lease, for the life of the pod.
		if token := es.Client.Unsubscribe(es.Topic); !token.WaitTimeout(subscribeTimeout) {
			log.Warn().Str("source", es.Id).
				Msg("MQTT event source's broker did not answer the unsubscribe on stop; disconnecting.")
		} else if token.Error() != nil {
			log.Warn().Err(token.Error()).Msg("MQTT event source failed to unsubscribe on stop.")
		}
		es.Client.Disconnect(250)
	}
	es.sendMu.Lock()
	es.closed = true
	if es.messages != nil {
		close(es.messages)
	}
	es.sendMu.Unlock()
	// Join the decode workers: they finish what was already queued, then return.
	es.workersDone.Wait()
	return nil
}

// Terminate microservice
func (es *MqttEventSource) Terminate(ctx context.Context) error {
	return es.lifecycle.Terminate(ctx)
}

// Terminate event source (as called by lifecycle manager)
func (es *MqttEventSource) ExecuteTerminate(ctx context.Context) error {
	return nil
}
