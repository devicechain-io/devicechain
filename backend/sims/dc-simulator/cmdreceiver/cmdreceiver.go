// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package cmdreceiver is a device-plane MQTT command receiver: the device side of
// the two-way command contract (ADR-043). It is the only Go client in the repo
// that RECEIVES device commands — the sim otherwise only PUBLISHES telemetry over
// HTTP ingress, which is one-way.
//
// It has TWO consumers, which is why it sits at the module root rather than
// inside either of them:
//
//   - loadtest (ADR-064 L2d-3) drives a bounded probe cohort and reconciles the
//     durable command status against this receiver's wire-level evidence.
//
//   - a SCENARIO whose manifest sets CommandFarEnd to the INTERNAL mode (widgetlab)
//     attaches one for its whole device set at bootstrap, so a command issued from a
//     dashboard's command-button widget completes QUEUED -> SENT -> SUCCESSFUL
//     instead of sitting at SENT until it expires. Without it the widget renders a
//     plausible round trip that never happens.
//
//     Only that mode. A scenario whose far end is a presentation client in another
//     process (the EXTERNAL mode) attaches none of this: a receiver running
//     alongside the real device would answer SUCCESSFUL for work it neither did nor
//     can observe, which is the same lie as the expiring command with the sign
//     flipped.
//
// A DeviceChain device receives commands over MQTT on the NATS built-in MQTT
// gateway. Each device is its own MQTT connection (MQTT 3.1.1 has no shared
// subscriptions, and a device credential's minted JWT grants SUB only to that one
// device's command subject), authenticated by the callout contract:
//
//	username = "{tenant}:{credentialId}"   password = ""   (empty ⇒ ACCESS_TOKEN,
//	                                                         credentialId is bearer)
//
// which is exactly the ACCESS_TOKEN credential material the sim already derives per
// device — so a receiver reuses it with no new provisioning. On a command frame the
// receiver decodes the envelope, records it (de-duped by the command token, since
// delivery is at-least-once and the same token may arrive more than once), and
// publishes a success response back on the tenant-scoped command-responses subject.
// That response is what drives the durable command QUEUED→SENT→SUCCESSFUL: the
// harness's authoritative round-trip proof is the durable status, and this receiver
// is the faithful two-way device (ADR-043) that makes SUCCESSFUL reachable, plus the
// wire-level witness that measures the at-least-once redelivery.
//
// It is fail-closed about its own blindness (the L2c lesson): a device that never
// got a confirmed SUBACK is reported as un-subscribed, so its silence is never read
// as clean — the harness surfaces it rather than treating a receiver that never
// attached as "no command arrived".
package cmdreceiver

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/devicechain-io/dc-microservice/messaging"
	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/rs/zerolog/log"
)

// deliveryEnvelope is the JSON command-delivery publishes on the per-device
// command subject (command-delivery/processor deliveryEnvelope). Mirrored here as a
// literal since the sim speaks only the wire, like emit.go mirrors the JsonEvent
// credential strings. Only the fields the receiver needs are declared.
type deliveryEnvelope struct {
	Token       string          `json:"token"`
	DeviceToken string          `json:"deviceToken"`
	Name        string          `json:"name"`
	Payload     json.RawMessage `json:"payload,omitempty"`
}

// responseEnvelope is the JSON a device publishes back on the tenant-scoped
// command-responses subject (command-delivery/processor responseEnvelope). The
// consumer derives the tenant from the subject and matches the command by
// CommandToken, so a bare success keyed by the delivery token is a complete reply.
type responseEnvelope struct {
	CommandToken string `json:"commandToken"`
	Success      bool   `json:"success"`
}

// Tuning — bounded waits so a broker that is unreachable or a subscription that is
// never acked fails a device fast (as blind) rather than hanging the whole run.
const (
	connectTimeout      = 15 * time.Second
	subscribeTimeout    = 15 * time.Second
	publishTimeout      = 5 * time.Second
	disconnectQuiesceMS = 250 // ms paho waits for in-flight work on Disconnect
	disconnectWait      = 5 * time.Second
	disconnectPoll      = 20 * time.Millisecond
)

// deviceState is one device's MQTT connection and its receive accounting. The
// counters are guarded by mu because paho invokes the message handler on its own
// goroutines.
type deviceState struct {
	token         string
	commandTopic  string
	responseTopic string
	client        mqtt.Client

	ready     chan error // buffered(1): the first SUBACK result (nil == subscribed)
	readyOnce sync.Once

	mu           sync.Mutex
	subscribed   bool
	disconnected bool           // deliberately disconnected by Disconnect, not a blip
	reconnects   int            // times this token was re-Subscribed after a Disconnect
	raw          int            // total command frames received, INCLUDING redeliveries
	distinct     map[string]int // command token → times seen (dedup key)
	malformed    int            // frames that did not decode as a command envelope
	// misrouted counts frames addressed to a DIFFERENT device. Kept apart from
	// malformed because they are opposite diagnoses: a malformed frame is a decoding
	// problem on this connection, while a misrouted one is a well-formed command that
	// reached the wrong subscriber — a dispatch defect, and the more serious of the two.
	misrouted        int
	firstMisroutedTo string
	connLosses       int   // OnConnectionLost callbacks (a blip; auto-reconnect recovers)
	responded        int   // response publishes that were ACKED by the broker
	respondErr       error // first response-publish error, if any
}

// newClient builds the paho client for one device. It is a FIELD rather than a direct
// call to mqtt.NewClient so the attach path can be driven without a broker — and the
// attach path is where the claim/release accounting lives, which is precisely the part
// that cannot be checked by calling releaseDevice directly. A mutation that stranded
// the claim on a failed connect survived every test until this seam existed.
type clientFactory func(*mqtt.ClientOptions) mqtt.Client

// Receiver manages a bounded cohort of per-device MQTT connections and their
// receive accounting.
type Receiver struct {
	instanceId string
	tenant     string
	broker     string      // e.g. ssl://127.0.0.1:1883
	tlsConfig  *tls.Config // non-nil ⇒ TLS; required for an ssl://‑scheme broker

	// How long Disconnect waits for the local client to stop reporting itself
	// connected, and how often it looks. Fields rather than the bare consts so a
	// test can reach the timeout branch without spending the real wait on it.
	disconnectWait time.Duration
	disconnectPoll time.Duration

	// newClient builds each device's paho client. Defaulted in New; substitutable so a
	// test can drive the attach path — and therefore the claim/release accounting on
	// it — with no broker.
	newClient clientFactory

	mu      sync.Mutex
	devices map[string]*deviceState
}

// New builds a receiver for one instance/tenant against the MQTT broker (the NATS
// MQTT gateway, e.g. a port-forward of dc-nats:1883). The NATS MQTT listener
// terminates server-side TLS, so an ssl://-scheme broker needs a non-nil tlsConfig;
// pass nil for a plaintext tcp:// broker. It opens no connection until Subscribe.
func New(instanceId, tenant, broker string, tlsConfig *tls.Config) *Receiver {
	return &Receiver{
		instanceId:     instanceId,
		tenant:         tenant,
		broker:         broker,
		tlsConfig:      tlsConfig,
		devices:        make(map[string]*deviceState),
		disconnectWait: disconnectWait,
		disconnectPoll: disconnectPoll,
		newClient:      mqtt.NewClient,
	}
}

// commandTopic is the MQTT topic a device subscribes to for its commands: the
// subject "{instance}.{tenant}.device-commands.{token}" with dots mapped to slashes.
func (r *Receiver) commandTopic(deviceToken string) string {
	return fmt.Sprintf("%s/%s/device-commands/%s", r.instanceId, r.tenant, deviceToken)
}

// responseTopic is the tenant-scoped MQTT topic a device publishes command
// responses to: "{instance}/{tenant}/command-responses".
func (r *Receiver) responseTopic() string {
	return fmt.Sprintf("%s/%s/command-responses", r.instanceId, r.tenant)
}

// Subscribe connects one device to the MQTT gateway and subscribes it to its own
// command topic, returning only once the SUBACK is confirmed (or an error if the
// connect or subscribe fails/times out). Reusing the device's ACCESS_TOKEN
// credential id: username "{tenant}:{credentialId}", empty password.
//
// It subscribes inside OnConnect so a later auto-reconnect re-establishes the
// subscription uniformly; the first SUBACK signals `ready`, which Subscribe waits
// on so a device that connects but never gets its subscription acked is failed as
// blind rather than silently listening to nothing.
func (r *Receiver) Subscribe(ctx context.Context, deviceToken, credentialId string) error {
	ds := &deviceState{
		token:         deviceToken,
		commandTopic:  r.commandTopic(deviceToken),
		responseTopic: r.responseTopic(),
		ready:         make(chan error, 1),
		distinct:      make(map[string]int),
	}

	// The platform requires a device's MQTT client id to be its own — the auth callout
	// refuses anything else — so build it from the shared definition rather than a
	// literal. One session per device is all this receiver wants, so it takes the plain
	// form rather than appending a discriminator.
	clientID, err := messaging.DeviceClientID(r.instanceId, r.tenant, deviceToken)
	if err != nil {
		return fmt.Errorf("cmdreceiver cannot subscribe device %q: %w", deviceToken, err)
	}

	opts := mqtt.NewClientOptions()
	opts.AddBroker(r.broker)
	// CleanSession false + auto-reconnect keeps the SUBSCRIPTION across a blip: the broker
	// re-arms it during CONNECT, before this client could resubscribe, so there is no
	// connected-but-unsubscribed window. The receiver is load-bearing, so it favors delivery.
	//
	// 🔴 IT DOES NOT MAKE THE BROKER QUEUE A COMMAND, AND AN EARLIER COMMENT HERE SAID IT DID.
	// A platform-published command reaches an MQTT device only through the live QoS-0 path — it
	// is published over NATS, so it never enters the broker's QoS-1 message store and the durable
	// consumer behind a persistent subscription never sees it. A command issued while this
	// receiver is disconnected is dropped by the broker, and command-delivery has already marked
	// it SENT. Keeping the session narrows the window; it does not remove it.
	opts.SetClientID(clientID)
	opts.SetUsername(fmt.Sprintf("%s:%s", r.tenant, credentialId))
	opts.SetPassword("")
	opts.SetCleanSession(false)
	opts.SetAutoReconnect(true)
	opts.SetConnectTimeout(connectTimeout)
	opts.SetOrderMatters(false)
	// The NATS MQTT gateway terminates TLS; paho does TLS only for an ssl://-scheme
	// broker, and then needs a config (a nil one verifies against the system roots,
	// which a dev/self-signed gateway cert fails). The harness supplies the config.
	if r.tlsConfig != nil {
		opts.SetTLSConfig(r.tlsConfig)
	}
	opts.OnConnect = r.onConnect(ds)
	opts.OnConnectionLost = r.onConnectionLost(ds)

	ds.client = r.newClient(opts)
	if err := r.claimDevice(deviceToken, ds); err != nil {
		return err
	}

	// 🔴 THE CLAIM IS TAKEN BEFORE THE CONNECT AND MUST BE RELEASED IF THE CONNECT
	// FAILS. It has to be taken first — the OnConnect handler fires on the paho
	// goroutine and needs the registration already in place — but every path below can
	// fail with nothing connected, and a claim left behind on a failed attempt turns
	// the NEXT Subscribe for that token into "device is already connected", which is
	// both false and a diagnosis pointing at the wrong thing entirely. A harness that
	// reconnects (the presence churn cohort does, R times per run) would abort mid-run
	// naming a session collision that never happened.
	claimed := true
	defer func() {
		if claimed {
			r.releaseDevice(deviceToken, ds)
		}
	}()

	tok := ds.client.Connect()
	if !tok.WaitTimeout(connectTimeout) {
		return fmt.Errorf("device %q: MQTT connect to %s timed out (is the broker port-forwarded?)", deviceToken, r.broker)
	}
	if err := tok.Error(); err != nil {
		return fmt.Errorf("device %q: MQTT connect to %s failed: %w", deviceToken, r.broker, err)
	}

	// Wait for the first SUBACK (sent by onConnect) so a confirmed subscription, not
	// merely a connection, gates the caller starting to drive.
	select {
	case err := <-ds.ready:
		if err != nil {
			return fmt.Errorf("device %q: subscribe to %q failed: %w", deviceToken, ds.commandTopic, err)
		}
		claimed = false // the device is genuinely attached; the claim stands
		return nil
	case <-time.After(subscribeTimeout):
		return fmt.Errorf("device %q: subscribe to %q not acked within %s (blind)", deviceToken, ds.commandTopic, subscribeTimeout)
	case <-ctx.Done():
		return ctx.Err()
	}
}

// releaseDevice undoes a claim whose connect or subscribe never completed. It removes
// the entry ONLY if it is still the one this attempt registered: a concurrent
// Subscribe that legitimately took the slot after this one gave up must not have its
// registration deleted by the loser's cleanup.
func (r *Receiver) releaseDevice(deviceToken string, ds *deviceState) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if cur, ok := r.devices[deviceToken]; ok && cur == ds {
		delete(r.devices, deviceToken)
	}
}

// claimDevice registers ds as the connection for deviceToken, and is the pure heart
// of that registration — no broker, no network — so its two rules are unit-testable.
//
// It REFUSES a second live session for one client id. Two live connections cannot
// share an id: the platform requires a device's client id to be its own, so the
// broker resolves a collision by kicking one session, and whichever one loses stops
// receiving with no error raised anywhere. Overwriting the map entry (what this code
// used to do) made that outcome silent.
//
// It carries the reconnect count FORWARD across a deliberate departure, so a churn
// cohort's re-attachments are visible in the report rather than being erased along
// with the prior device state each time it reconnects.
func (r *Receiver) claimDevice(deviceToken string, ds *deviceState) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if prior, exists := r.devices[deviceToken]; exists {
		prior.mu.Lock()
		live := !prior.disconnected
		ds.reconnects = prior.reconnects + 1
		prior.mu.Unlock()
		if live {
			return fmt.Errorf("device %q is already connected: Disconnect it before "+
				"resubscribing, since one client id is one MQTT session", deviceToken)
		}
	}
	r.devices[deviceToken] = ds
	return nil
}

// onConnect subscribes the device to its command topic on every (re)connect and
// signals the first SUBACK result on ds.ready exactly once.
func (r *Receiver) onConnect(ds *deviceState) mqtt.OnConnectHandler {
	return func(c mqtt.Client) {
		// 🔴 Read the SUBACK, do not merely wait for it. A device's minted JWT grants
		// SUB only to its own command subject, so the failure this receiver is most
		// likely to meet is a REFUSAL (0x80) rather than an error — and paho reports a
		// refusal as success. Waiting on the token and reading its Error() marks the
		// device subscribed, unblocks the caller, and then delivers nothing: the
		// harness reconciles wire evidence that never arrives against durable commands
		// that really were sent, and reads a broker ACL problem as a platform defect.
		suberr := messaging.SubscribeMqttConfirmed(c, ds.commandTopic, 1, r.onMessage(ds), subscribeTimeout)
		if suberr == nil {
			ds.mu.Lock()
			ds.subscribed = true
			ds.mu.Unlock()
		}
		ds.readyOnce.Do(func() { ds.ready <- suberr })
	}
}

// onConnectionLost records a connection blip. With auto-reconnect the client will
// reconnect and onConnect re-subscribes; a persistent loss shows as a device that
// is not connected at Close, which the report surfaces.
func (r *Receiver) onConnectionLost(ds *deviceState) mqtt.ConnectionLostHandler {
	return func(_ mqtt.Client, err error) {
		ds.mu.Lock()
		ds.connLosses++
		ds.mu.Unlock()
		log.Warn().Err(err).Str("device", ds.token).Msg("command receiver connection lost; auto-reconnecting")
	}
}

// onMessage decodes a received command, records it (deduped by command token), and
// publishes a success response so the durable command reaches SUCCESSFUL. It
// responds on EVERY receipt (not only the first): responding is idempotent
// server-side (a terminal command ignores a late response), so a redelivery
// harmlessly retries a response whose first publish failed.
func (r *Receiver) onMessage(ds *deviceState) mqtt.MessageHandler {
	return func(_ mqtt.Client, msg mqtt.Message) {
		token, ok := r.recordFrame(ds, msg.Payload())
		if !ok {
			log.Warn().Str("device", ds.token).Msg("received a malformed command envelope")
			return
		}
		r.respond(ds, token)
	}
}

// recordFrame decodes a received frame and records it in the device's accounting:
// a well-formed command bumps the raw counter and its token's distinct tally
// (returning the token so the caller can respond), a malformed frame bumps the
// malformed counter and returns ok=false. It is the pure heart of the receiver —
// no broker, no network — so the de-dup accounting is unit-testable and
// mutation-verifiable without a live gateway.
func (r *Receiver) recordFrame(ds *deviceState, payload []byte) (token string, ok bool) {
	var env deliveryEnvelope
	// A frame that does not decode, OR decodes with an empty token, is malformed: an
	// empty token is not a real command (every dispatched command carries its unique
	// token), and answering with CommandToken:"" would drive command-delivery's
	// response consumer through a retry-to-poison on a row that never matches.
	if err := json.Unmarshal(payload, &env); err != nil || env.Token == "" {
		ds.mu.Lock()
		ds.malformed++
		ds.mu.Unlock()
		return "", false
	}

	// 🔴 THE ENVELOPE SAYS WHO IT IS FOR, AND UNTIL NOW NOTHING READ IT.
	//
	// A device's JWT scopes SUBSCRIBE to its own command topic but grants PUBLISH on
	// the TENANT-WIDE command-responses subject, and the response carries only a
	// command token — no device identity — so command-delivery marks whatever token it
	// is handed. A receiver that answers a frame addressed to somebody else is
	// therefore not merely untidy: it stamps a terminal state on another device's
	// command, and the platform has no way to tell that the wrong device answered.
	//
	// That is exactly the failure a fleet-write oracle exists to catch. If dispatch
	// mis-routes device A's envelope onto device B's topic, B answering it drives A's
	// row to SUCCESSFUL while A never actuates — and every durable-state invariant
	// reads green. Refusing here is what keeps the receiver an honest witness: the
	// misrouted frame is counted and NOT answered, so the real device's command stays
	// visibly unfinished instead of being closed by a bystander.
	//
	// A frame carrying no deviceToken at all is accepted, deliberately. The dispatcher
	// always sets it, but an empty field is an ABSENT claim about addressing rather
	// than a wrong one, and refusing on absence would make this receiver unusable
	// against any future transport that omits it.
	if env.DeviceToken != "" && env.DeviceToken != ds.token {
		ds.mu.Lock()
		ds.misrouted++
		if ds.firstMisroutedTo == "" {
			ds.firstMisroutedTo = env.DeviceToken
		}
		ds.mu.Unlock()
		log.Warn().Str("device", ds.token).Str("addressedTo", env.DeviceToken).Str("command", env.Token).
			Msg("received a command addressed to another device; NOT answering it")
		return "", false
	}

	ds.mu.Lock()
	ds.raw++
	ds.distinct[env.Token]++
	ds.mu.Unlock()
	return env.Token, true
}

// respond publishes a success response for a command token on the device's own
// connection (its JWT grants PUB to command-responses), then records the outcome.
//
// Every path funnels through ONE recordResponse call so the meaning of the counters
// lives somewhere a test can reach: this function needs a live broker connection, so
// nothing here is unit-testable, and the accounting used to be inlined among the
// publish steps where only a comment claimed a failed publish is not a response.
func (r *Receiver) respond(ds *deviceState, commandToken string) {
	r.recordResponse(ds, r.publishResponse(ds, commandToken))
}

// publishResponse marshals and publishes one response, returning the first failure
// or nil once the broker has ACKED it. A timeout is a failure: an unacked QoS-1
// publish is not a delivered response.
func (r *Receiver) publishResponse(ds *deviceState, commandToken string) error {
	payload, err := json.Marshal(responseEnvelope{CommandToken: commandToken, Success: true})
	if err != nil {
		return err
	}
	tok := ds.client.Publish(ds.responseTopic, 1, false, payload)
	if !tok.WaitTimeout(publishTimeout) {
		return fmt.Errorf("response publish timed out for command %q", commandToken)
	}
	if perr := tok.Error(); perr != nil {
		return fmt.Errorf("response publish failed for command %q: %w", commandToken, perr)
	}
	return nil
}

// recordResponse records one response publish's OUTCOME: a nil error counts a
// broker-acked response, anything else records the failure and counts nothing.
//
// It is the pure heart of the response accounting — no broker, no network — the way
// recordFrame is for the receive accounting, and for the same reason: `responded`
// has to mean "the broker acked this", not "we tried". A count that included
// attempts would read as a healthy far end on a device whose every publish failed,
// which is precisely the reading these counters exist to make impossible, and while
// the decision was inlined in respond() nothing could check it.
func (r *Receiver) recordResponse(ds *deviceState, err error) {
	if err != nil {
		r.recordRespondErr(ds, err)
		return
	}
	ds.mu.Lock()
	ds.responded++
	ds.mu.Unlock()
}

func (r *Receiver) recordRespondErr(ds *deviceState, err error) {
	ds.mu.Lock()
	if ds.respondErr == nil {
		ds.respondErr = err
	}
	ds.mu.Unlock()
	log.Warn().Err(err).Str("device", ds.token).Msg("command response publish error")
}

// Distinct reports how many DISTINCT command tokens a device has received (the
// at-least-once redeliveries collapsed). Zero for an unknown device.
func (r *Receiver) Distinct(deviceToken string) int {
	r.mu.Lock()
	ds, ok := r.devices[deviceToken]
	r.mu.Unlock()
	if !ok {
		return 0
	}
	ds.mu.Lock()
	defer ds.mu.Unlock()
	return len(ds.distinct)
}

// Disconnect closes ONE device's MQTT connection deliberately, leaving the rest of
// the cohort connected. It exists for the presence oracle: broker-asserted presence
// is driven by the MQTT SESSION, so a device "leaving" has to be a real disconnect —
// a device that merely stops publishing is still connected and still present.
//
// An unknown token is an ERROR, not a silent no-op. The caller is asking for a
// departure it will then wait to observe, so a typo would surface later as "the
// departed device never went offline" — an environment mistake wearing the costume
// of a platform defect.
//
// Calling it twice is idempotent: the second departure is the same departure.
//
// 🔴 IT DOES NOT PROVE THE BROKER PROCESSED THE DISCONNECT, AND NOTHING EXPORTED BY
// paho CAN. Disconnect runs its teardown in a goroutine and returns once the quiesce
// timer expires whether or not the packet was sent, and the status it then exposes
// goes false at "disconnecting" — before the session is gone server-side. So this
// returns when the LOCAL client has stopped reporting itself connected, and a caller
// that needs the BROKER's view must wait on an observable (the device-state
// projection flipping) rather than on this returning. Reconnecting the same token on
// the strength of this call alone races the teardown.
//
// Auto-reconnect does not undo it: an intentional disconnect sets the status to
// disconnecting, and paho's connection-lost path returns early rather than
// scheduling a reconnect when the loss is the user's own request.
func (r *Receiver) Disconnect(deviceToken string) error {
	r.mu.Lock()
	ds, ok := r.devices[deviceToken]
	r.mu.Unlock()
	if !ok {
		return fmt.Errorf("cmdreceiver has no device %q to disconnect", deviceToken)
	}

	ds.mu.Lock()
	already := ds.disconnected
	ds.disconnected = true
	ds.mu.Unlock()
	if already || ds.client == nil {
		return nil
	}

	ds.client.Disconnect(disconnectQuiesceMS)
	deadline := time.Now().Add(r.disconnectWait)
	for ds.client.IsConnected() {
		if time.Now().After(deadline) {
			return fmt.Errorf("device %q still reports itself connected %s after DISCONNECT",
				deviceToken, r.disconnectWait)
		}
		time.Sleep(r.disconnectPoll)
	}
	return nil
}

// Close disconnects every device connection. Safe to call once at the end of a run.
func (r *Receiver) Close() {
	r.mu.Lock()
	devices := make([]*deviceState, 0, len(r.devices))
	for _, ds := range r.devices {
		devices = append(devices, ds)
	}
	r.mu.Unlock()
	for _, ds := range devices {
		if ds.client != nil {
			ds.client.Disconnect(disconnectQuiesceMS)
		}
	}
}

// --- report -------------------------------------------------------------------

// DeviceReport is one device's receive evidence. It is NON-GATING: the harness
// gate reads durable command status, and this corroborates it and measures the
// at-least-once redelivery.
type DeviceReport struct {
	Token      string `json:"token"`
	Subscribed bool   `json:"subscribed"`
	Raw        int    `json:"rawReceived"`
	Distinct   int    `json:"distinctReceived"`
	Malformed  int    `json:"malformed"`
	// Misrouted counts commands addressed to a DIFFERENT device that arrived on this
	// device's subscription, and MisroutedTo names the first such addressee. Any
	// non-zero value here is a dispatch defect, not a receiver one — and it is the one
	// piece of evidence that can tell "this device answered" apart from "some device
	// in the tenant answered", since a command response carries no device identity.
	Misrouted   int    `json:"misrouted,omitempty"`
	MisroutedTo string `json:"misroutedTo,omitempty"`
	ConnLosses  int    `json:"connectionLosses"`
	Responded   int    `json:"responded"`
	RespondErr  string `json:"respondError,omitempty"`
	// Disconnected separates a deliberate departure from a device that never
	// attached: both stop receiving, and only one of them is a problem.
	Disconnected bool `json:"disconnected,omitempty"`
	Reconnects   int  `json:"reconnects,omitempty"`
}

// Report is the whole cohort's receive evidence.
type Report struct {
	Broker         string                  `json:"broker"`
	Devices        map[string]DeviceReport `json:"devices"`
	TotalRaw       int                     `json:"totalRawReceived"`
	TotalDistinct  int                     `json:"totalDistinctReceived"`
	TotalResponded int                     `json:"totalResponded"`
	// TotalMisrouted is the cohort-wide count of commands that reached the wrong
	// device. A harness gates on this being zero: a single misroute means some
	// device's durable SUCCESSFUL was written by a device that is not it.
	TotalMisrouted int      `json:"totalMisrouted"`
	Blind          []string `json:"blindDevices,omitempty"` // subscribed==false
}

// Report snapshots the cohort's receive evidence.
func (r *Receiver) Report() Report {
	r.mu.Lock()
	defer r.mu.Unlock()
	rep := Report{Broker: r.broker, Devices: make(map[string]DeviceReport, len(r.devices))}
	for tok, ds := range r.devices {
		ds.mu.Lock()
		dr := DeviceReport{
			Token:        tok,
			Subscribed:   ds.subscribed,
			Raw:          ds.raw,
			Distinct:     len(ds.distinct),
			Malformed:    ds.malformed,
			Misrouted:    ds.misrouted,
			MisroutedTo:  ds.firstMisroutedTo,
			ConnLosses:   ds.connLosses,
			Responded:    ds.responded,
			Disconnected: ds.disconnected,
			Reconnects:   ds.reconnects,
		}
		if ds.respondErr != nil {
			dr.RespondErr = ds.respondErr.Error()
		}
		ds.mu.Unlock()
		rep.Devices[tok] = dr
		rep.TotalRaw += dr.Raw
		rep.TotalMisrouted += dr.Misrouted
		rep.TotalDistinct += dr.Distinct
		rep.TotalResponded += dr.Responded
		if !dr.Subscribed {
			rep.Blind = append(rep.Blind, tok)
		}
	}
	// Sorted because this report is now rendered on the sim's /status, which a
	// human and a script both read repeatedly: map iteration order would reshuffle
	// the blind list between two polls of an unchanged far end.
	sort.Strings(rep.Blind)
	return rep
}
