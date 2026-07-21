// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package cmdreceiver is a device-plane MQTT command receiver for the ADR-064
// load-test command round-trip harness (L2d-3). It is the first Go client in the
// repo that RECEIVES device commands: the sim otherwise only PUBLISHES telemetry
// over HTTP ingress, so there was no device-side "listen for a command, act on it,
// answer it" path to reuse.
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
	"sync"
	"time"

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

	mu         sync.Mutex
	subscribed bool
	raw        int            // total command frames received, INCLUDING redeliveries
	distinct   map[string]int // command token → times seen (dedup key)
	malformed  int            // frames that did not decode as a command envelope
	connLosses int            // OnConnectionLost callbacks (a blip; auto-reconnect recovers)
	respondErr error          // first response-publish error, if any
}

// Receiver manages a bounded cohort of per-device MQTT connections and their
// receive accounting.
type Receiver struct {
	instanceId string
	tenant     string
	broker     string      // e.g. ssl://127.0.0.1:1883
	tlsConfig  *tls.Config // non-nil ⇒ TLS; required for an ssl://‑scheme broker

	mu      sync.Mutex
	devices map[string]*deviceState
}

// New builds a receiver for one instance/tenant against the MQTT broker (the NATS
// MQTT gateway, e.g. a port-forward of dc-nats:1883). The NATS MQTT listener
// terminates server-side TLS, so an ssl://-scheme broker needs a non-nil tlsConfig;
// pass nil for a plaintext tcp:// broker. It opens no connection until Subscribe.
func New(instanceId, tenant, broker string, tlsConfig *tls.Config) *Receiver {
	return &Receiver{
		instanceId: instanceId,
		tenant:     tenant,
		broker:     broker,
		tlsConfig:  tlsConfig,
		devices:    make(map[string]*deviceState),
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

	opts := mqtt.NewClientOptions()
	opts.AddBroker(r.broker)
	// The MQTT client id must be TENANT-QUALIFIED, not the bare device token. NATS
	// keys a persistent MQTT session by (account, client id) and every tenant on an
	// instance shares one NATS account, while device tokens are deterministic and
	// identical across runs (harness-cmd-probe-001…). A bare token would make a fresh
	// tenant's run resume the PRIOR tenant's persisted session — whose stored
	// subscription names the old tenant's subject, now a permission violation under
	// the new JWT — and let two runs fight via MQTT session takeover. Qualifying with
	// the tenant makes each run's session id unique. CleanSession false + auto-
	// reconnect keeps the session so a brief blip does not lose a QoS-1 command (the
	// broker queues it) — the receiver is load-bearing, so it favors delivery.
	opts.SetClientID(r.tenant + "." + deviceToken)
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

	ds.client = mqtt.NewClient(opts)
	r.mu.Lock()
	r.devices[deviceToken] = ds
	r.mu.Unlock()

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
		return nil
	case <-time.After(subscribeTimeout):
		return fmt.Errorf("device %q: subscribe to %q not acked within %s (blind)", deviceToken, ds.commandTopic, subscribeTimeout)
	case <-ctx.Done():
		return ctx.Err()
	}
}

// onConnect subscribes the device to its command topic on every (re)connect and
// signals the first SUBACK result on ds.ready exactly once.
func (r *Receiver) onConnect(ds *deviceState) mqtt.OnConnectHandler {
	return func(c mqtt.Client) {
		tok := c.Subscribe(ds.commandTopic, 1, r.onMessage(ds))
		var suberr error
		if !tok.WaitTimeout(subscribeTimeout) {
			suberr = fmt.Errorf("SUBACK timed out")
		} else {
			suberr = tok.Error()
		}
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
	ds.mu.Lock()
	ds.raw++
	ds.distinct[env.Token]++
	ds.mu.Unlock()
	return env.Token, true
}

// respond publishes a success response for a command token on the device's own
// connection (its JWT grants PUB to command-responses).
func (r *Receiver) respond(ds *deviceState, commandToken string) {
	payload, err := json.Marshal(responseEnvelope{CommandToken: commandToken, Success: true})
	if err != nil {
		r.recordRespondErr(ds, err)
		return
	}
	tok := ds.client.Publish(ds.responseTopic, 1, false, payload)
	if !tok.WaitTimeout(publishTimeout) {
		r.recordRespondErr(ds, fmt.Errorf("response publish timed out for command %q", commandToken))
		return
	}
	if perr := tok.Error(); perr != nil {
		r.recordRespondErr(ds, fmt.Errorf("response publish failed for command %q: %w", commandToken, perr))
	}
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
	ConnLosses int    `json:"connectionLosses"`
	RespondErr string `json:"respondError,omitempty"`
}

// Report is the whole cohort's receive evidence.
type Report struct {
	Broker        string                  `json:"broker"`
	Devices       map[string]DeviceReport `json:"devices"`
	TotalRaw      int                     `json:"totalRawReceived"`
	TotalDistinct int                     `json:"totalDistinctReceived"`
	Blind         []string                `json:"blindDevices,omitempty"` // subscribed==false
}

// Report snapshots the cohort's receive evidence.
func (r *Receiver) Report() Report {
	r.mu.Lock()
	defer r.mu.Unlock()
	rep := Report{Broker: r.broker, Devices: make(map[string]DeviceReport, len(r.devices))}
	for tok, ds := range r.devices {
		ds.mu.Lock()
		dr := DeviceReport{
			Token:      tok,
			Subscribed: ds.subscribed,
			Raw:        ds.raw,
			Distinct:   len(ds.distinct),
			Malformed:  ds.malformed,
			ConnLosses: ds.connLosses,
		}
		if ds.respondErr != nil {
			dr.RespondErr = ds.respondErr.Error()
		}
		ds.mu.Unlock()
		rep.Devices[tok] = dr
		rep.TotalRaw += dr.Raw
		rep.TotalDistinct += dr.Distinct
		if !dr.Subscribed {
			rep.Blind = append(rep.Blind, tok)
		}
	}
	return rep
}
