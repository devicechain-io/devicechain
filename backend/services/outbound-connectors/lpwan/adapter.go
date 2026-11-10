// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package lpwan

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/devicechain-io/dc-microservice/cmddownlink"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/payloadencode"
	"github.com/devicechain-io/dc-microservice/transport"

	"github.com/devicechain-io/dc-outbound-connectors/connectorspec"
)

// Result labels for lpwan_downlinks_total. A fixed enum: never a per-tenant or per-device value.
const (
	ResultQueued          = "queued"           // the network server queued the downlink; the command is answered success
	ResultQueuedConfirmed = "queued_confirmed" // a confirmed downlink was queued; the command waits for the acknowledgement
	ResultPendingFailed   = "pending_failed"   // a confirmed downlink was queued but could not be tracked; answered unanswered
	ResultRefusedParams   = "refused_params"   // the command's parameters are not a valid downlink
	ResultRefusedExpired  = "refused_expired"  // the command expired before it could be sent
	ResultRefusedNoRoute  = "refused_no_route" // no Dc-Source: the integration cannot be named
	ResultRefusedNoLink   = "refused_no_link"  // the integration has no (or an ambiguous) network-server link
	ResultRefusedNoDevEUI = "refused_no_deveui"
	ResultRefusedConfirm  = "refused_confirmed" // a confirmed downlink on a build with no acknowledgement feed
	ResultRefusedRate     = "refused_rate"      // the tenant is over its outbound ceiling
	ResultUnsent          = "unsent"            // the network server (or the egress guard) refused it, nothing queued
	ResultUnanswered      = "unanswered"        // the request may have been queued
	ResultNotServed       = "not_served"        // another transport's command
	ResultPoison          = "poison"
	ResultTenantGone      = "tenant_gone"
	ResultClaimLost       = "claim_lost"
	ResultClaimFailed     = "claim_failed"
	ResultLookupFailed    = "lookup_failed" // a transient read failure; left unacked
	ResultResponseFailed  = "response_failed"
)

// Results is every result label, so a metric can create its children at 0.
var Results = []string{
	ResultQueued, ResultQueuedConfirmed, ResultPendingFailed, ResultRefusedParams, ResultRefusedExpired, ResultRefusedNoRoute, ResultRefusedNoLink,
	ResultRefusedNoDevEUI, ResultRefusedConfirm, ResultRefusedRate, ResultUnsent, ResultUnanswered,
	ResultNotServed, ResultPoison, ResultTenantGone, ResultClaimLost, ResultClaimFailed,
	ResultLookupFailed, ResultResponseFailed,
}

// DefaultAckWait is how long a confirmed downlink waits for the device's acknowledgement when its
// command carries no expiry. A class A device may only listen after its next uplink, which can be
// hours away, so the wait is long; the command's own expiry, when it has one, ends it sooner.
const DefaultAckWait = 24 * time.Hour

// Reason is the machine-readable code carried in a refusal's stored response, in the same
// {"reason": ...} document other adapters use.
type Reason string

const (
	ReasonInvalidParams Reason = "INVALID_PARAMETERS"
	ReasonExpired       Reason = "EXPIRED"
	ReasonNoRoute       Reason = "NO_ROUTE"
	ReasonNoLink        Reason = "NO_NETWORK_SERVER_LINK"
	ReasonAmbiguousLink Reason = "AMBIGUOUS_NETWORK_SERVER_LINK"
	ReasonNoDevEUI      Reason = "NO_DEVEUI"
	ReasonNoDeviceID    Reason = "NO_DEVICE_ID"
	ReasonFailed        Reason = "DOWNLINK_FAILED"
	// ReasonFailedMaybeSent: the network server reported the downlink failed and it cannot be shown
	// that it was never transmitted.
	ReasonFailedMaybeSent Reason = "DOWNLINK_FAILED_MAYBE_SENT"
	ReasonUnsupported     Reason = "UNSUPPORTED"
	ReasonRateLimited     Reason = "RATE_LIMITED"
	ReasonNetworkServer   Reason = "NETWORK_SERVER_REFUSED"
	ReasonEgressBlocked   Reason = "EGRESS_BLOCKED"
	ReasonLinkUnreadable  Reason = "NETWORK_SERVER_LINK_INVALID"
	ReasonUntracked       Reason = "ACK_UNTRACKED"
	// ReasonNotAcknowledged: the network server reported the confirmed downlink unacknowledged.
	ReasonNotAcknowledged Reason = "NOT_ACKNOWLEDGED"
	// ReasonNoAcknowledgement: the wait for an outcome ran out with no report.
	ReasonNoAcknowledgement Reason = "NO_ACKNOWLEDGEMENT"
)

// Errors a Links or Devices implementation returns to name a definite absence. Any other error is
// a transient read failure: the message is left unacked and redelivers.
var (
	ErrNoLink    = errors.New("lpwan: the integration has no network-server link")
	ErrAmbiguous = errors.New("lpwan: more than one network-server link names the integration")
	ErrBadLink   = errors.New("lpwan: the network-server link is not usable")
	ErrNoDevEUI  = errors.New("lpwan: the device has no DevEUI")
	// ErrNoDeviceID: the device has no tts.device-id alias, which The Things Stack addresses it by.
	ErrNoDeviceID = errors.New("lpwan: the device has no tts.device-id")
)

// Links finds the published network-server link (a lorawan_downlink connector, with its credential
// resolved) for an inbound integration of a tenant.
type Links interface {
	Resolve(ctx context.Context, tenant, integration string) (connectorspec.LoRaWANDownlinkTarget, error)
}

// Devices finds the name a network server addresses a device by: its DevEUI (ChirpStack) or its
// tts.device-id (The Things Stack).
type Devices interface {
	Encoder(ctx context.Context, tenant, deviceToken, commandKey string) (*payloadencode.Encoder, error)
	DevEUI(ctx context.Context, tenant, deviceToken string) (string, error)
	TTSDeviceID(ctx context.Context, tenant, deviceToken string) (string, error)
}

// Claimer confirms a dispatch on the live path (cmddownlink.Claimer.ClaimDispatch).
type Claimer interface {
	ClaimDispatch(ctx context.Context, tenant, commandToken, dispatchNonce string) (string, bool, error)
}

// Metrics counts results; a nil Metrics counts nothing.
type Metrics interface{ Count(result string) }

// Options are the adapter's tuning and optional collaborators.
type Options struct {
	// Workers is the number of concurrent downlinks. At least 1.
	Workers int
	// Rate, when set, charges one outbound call per downlink to the tenant's egress ceiling. A
	// downlink over the ceiling is answered UNSENT (safe to retry), never waited for or dropped.
	Rate *core.TenantRateLimiter
	// TenantDeleted answers the lifecycle question for a tenant; nil admits every tenant.
	TenantDeleted func(string) bool
	// ReadPacer paces retries after a failing read. Required.
	ReadPacer *core.ReadPacer
	// SendTimeout bounds one network-server call. Defaults to 10 s.
	SendTimeout time.Duration
	// Pending, when set, is where a CONFIRMED downlink waits for its acknowledgement. Without it the
	// adapter refuses a confirmed downlink (unconfirmed ones are unaffected).
	Pending PendingStore
	// AckWait is how long a confirmed downlink waits for the device's acknowledgement before the
	// command is answered as unanswered, capped by the command's own expiry. Defaults to
	// DefaultAckWait.
	AckWait time.Duration
	// Now is the clock (tests).
	Now func() time.Time
	// ResponseAttempts and ResponseBackoff bound the local retry of the outcome publish.
	ResponseAttempts int
	ResponseBackoff  time.Duration
}

// Adapter consumes device-commands for devices that report through an inbound integration and
// delivers them through the integration's network server.
type Adapter struct {
	reader    messaging.MessageReader
	responses cmddownlink.ResponseWriter
	claimer   Claimer
	links     Links
	devices   Devices
	sender    Enqueuer
	metrics   Metrics
	opts      Options

	ctx      context.Context
	cancel   context.CancelFunc
	messages chan messaging.Message
	readerWG sync.WaitGroup
	workerWG sync.WaitGroup
	stopOnce sync.Once
}

// New builds the adapter. Every collaborator except metrics is required: a nil one is a startup
// bug, not a mode.
func New(reader messaging.MessageReader, responses cmddownlink.ResponseWriter, claimer Claimer, links Links,
	devices Devices, sender Enqueuer, metrics Metrics, opts Options) *Adapter {
	if reader == nil || responses == nil || claimer == nil || links == nil || devices == nil || sender == nil ||
		opts.ReadPacer == nil {
		panic("lpwan: New needs a reader, response writer, claimer, links, devices, sender and read pacer")
	}
	if opts.Workers < 1 {
		opts.Workers = 1
	}
	if opts.SendTimeout <= 0 {
		opts.SendTimeout = 10 * time.Second
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.AckWait <= 0 {
		opts.AckWait = DefaultAckWait
	}
	if opts.ResponseAttempts < 1 {
		opts.ResponseAttempts = 3
	}
	if opts.ResponseBackoff <= 0 {
		opts.ResponseBackoff = 200 * time.Millisecond
	}
	return &Adapter{reader: reader, responses: responses, claimer: claimer, links: links, devices: devices,
		sender: sender, metrics: metrics, opts: opts, ctx: context.Background()}
}

func (a *Adapter) count(result string) {
	if a.metrics != nil {
		a.metrics.Count(result)
	}
}

// Start launches the workers and the read loop.
func (a *Adapter) Start() {
	a.ctx, a.cancel = context.WithCancel(context.Background())
	a.messages = make(chan messaging.Message, a.opts.Workers)
	for i := 0; i < a.opts.Workers; i++ {
		a.workerWG.Add(1)
		go func() {
			defer a.workerWG.Done()
			for msg := range a.messages {
				messaging.Process(msg, func(m messaging.Message) { a.handle(a.ctx, m) })
			}
		}()
	}
	a.readerWG.Add(1)
	go func() {
		defer a.readerWG.Done()
		messaging.RunConsumer(a.ctx, a.reader, a.opts.ReadPacer, func(msg messaging.Message) bool {
			select {
			case a.messages <- msg:
				return true
			case <-a.ctx.Done():
				return false
			}
		})
	}()
}

// Stop ends the read loop and lets in-flight downlinks finish. It is idempotent.
func (a *Adapter) Stop() {
	a.stopOnce.Do(func() {
		if a.cancel != nil {
			a.cancel()
		}
		a.readerWG.Wait()
		if a.messages != nil {
			close(a.messages)
		}
		a.workerWG.Wait()
	})
}

// job is one command being worked.
type job struct {
	tenant, device, token string
	nonce                 string
	payload               []byte
	source                string
	expires               time.Time
}

func ack(msg messaging.Message) {
	if err := msg.Ack(); err != nil {
		log.Debug().Err(err).Msg("Failed to ack a command message (it will redeliver, bounded by MaxDeliver).")
	}
}

// handle runs one command through the gates in order. The first gate that matches decides.
//
//  1. parse tenant + envelope (poison: ack)
//  2. tenant deleted: ack
//  3. transport is not "integration": ack, not served. An ABSENT header is also not served here: with
//     no Dc-Source the integration cannot be named, and nothing else tells this adapter the device
//     is its own.
//  4. no Dc-Source: UNSENT
//  5. expired: UNSENT
//  6. parameters: UNSENT
//  7. confirmed: UNSENT on a build with no acknowledgement feed (no PendingStore)
//  8. network-server link: UNSENT when absent or ambiguous; unacked when unreadable now
//  9. DevEUI: UNSENT when absent; unacked when unreadable now
//  10. tenant rate: UNSENT over the ceiling
//  11. claim (confirmCommandDispatch): lost -> ack, nothing sent; failed -> unacked, nothing sent
//  12. enqueue on the network server, then answer the outcome and ack. A CONFIRMED downlink the
//     network server accepted is not answered here: its pending record is stored and the command
//     stays SENT until the acknowledgement feed (or the record's deadline) answers it.
//
// Everything before the claim is a read or a refusal, so a redelivery repeats it harmlessly; the
// network call is made only by the replica that won the claim, once per dispatch nonce.
func (a *Adapter) handle(ctx context.Context, msg messaging.Message) {
	_, tenant, ok := messaging.TenantContextFromSubject(context.Background(), msg.Subject)
	if !ok || core.ValidateToken(tenant) != nil {
		log.Warn().Str("subject", clipLog(msg.Subject)).Msg("Dropping a command with no valid tenant in its subject.")
		a.count(ResultPoison)
		ack(msg)
		return
	}
	var env cmddownlink.DeliveryEnvelope
	if err := json.Unmarshal(msg.Value, &env); err != nil || env.Token == "" || env.DeviceToken == "" {
		log.Warn().Str("tenant", tenant).Msg("Dropping an undecodable command envelope.")
		a.count(ResultPoison)
		ack(msg)
		return
	}
	if a.opts.TenantDeleted != nil && a.opts.TenantDeleted(tenant) {
		a.count(ResultTenantGone)
		ack(msg)
		return
	}
	if msg.Headers[cmddownlink.HeaderTransport] != string(transport.Integration) {
		a.count(ResultNotServed)
		ack(msg)
		return
	}
	j := &job{tenant: tenant, device: env.DeviceToken, token: env.Token, nonce: env.DispatchNonce,
		source: msg.Headers[cmddownlink.HeaderSource]}
	if env.Payload != nil {
		j.payload = *env.Payload
	}
	if h := msg.Headers[cmddownlink.HeaderCommandExpires]; h != "" {
		if t, err := time.Parse(time.RFC3339, h); err == nil {
			j.expires = t
		}
	}

	integration, ok := integrationOf(j.source)
	if !ok {
		a.refuse(msg, j, ResultRefusedNoRoute, ReasonNoRoute,
			"the command carries no integration for the device, so it cannot be routed to a network server")
		return
	}
	if a.expired(j) {
		a.refuse(msg, j, ResultRefusedExpired, ReasonExpired, "the command expired before it could be sent")
		return
	}
	encoder, err := a.devices.Encoder(ctx, tenant, j.device, env.Name)
	if err != nil {
		a.refuse(msg, j, ResultRefusedParams, ReasonInvalidParams, "the command definition could not be read")
		return
	}
	var params Params
	if encoder == nil {
		params, err = ParseParams(j.payload)
	} else {
		var encoded payloadencode.Result
		encoded, err = encoder.Encode(j.payload)
		var encodeErr *payloadencode.EncodeError
		if errors.As(err, &encodeErr) {
			err = &ParamError{Param: encodeErr.Param, Reason: encodeErr.Reason}
		}
		params = Params{FPort: encoded.FPort, Confirmed: encoded.Confirmed, Data: encoded.Data}
		if err == nil && len(params.Data) > MaxPayloadBytes {
			err = refuse("(payload)", "encoded payload exceeds 242 bytes")
		}
	}
	if err != nil {
		a.refuse(msg, j, ResultRefusedParams, ReasonInvalidParams, err.Error())
		return
	}
	if params.Confirmed && a.opts.Pending == nil {
		a.refuse(msg, j, ResultRefusedConfirm, ReasonUnsupported,
			"confirmed downlinks are not available in this build: it has no acknowledgement feed; send the command unconfirmed")
		return
	}
	target, err := a.links.Resolve(ctx, tenant, integration)
	switch {
	case errors.Is(err, ErrNoLink):
		a.refuse(msg, j, ResultRefusedNoLink, ReasonNoLink,
			"no lorawan_downlink connector names this device's integration; create and publish one")
		return
	case errors.Is(err, ErrAmbiguous):
		a.refuse(msg, j, ResultRefusedNoLink, ReasonAmbiguousLink,
			"more than one lorawan_downlink connector names this device's integration; keep one")
		return
	case errors.Is(err, ErrBadLink):
		a.refuse(msg, j, ResultRefusedNoLink, ReasonLinkUnreadable,
			"the network-server link for this device's integration is not usable: "+clipText(err.Error()))
		return
	case err != nil:
		a.lookupFailed(ctx, j, "link", err)
		return
	}
	// The name the network server addresses the device by: read from the device, never the command.
	var ident, pendEUI, deviceRef string
	if target.NetworkServer == connectorspec.NetworkServerTTSV3 {
		id, err := a.devices.TTSDeviceID(ctx, tenant, j.device)
		switch {
		case errors.Is(err, ErrNoDeviceID):
			a.refuse(msg, j, ResultRefusedNoDevEUI, ReasonNoDeviceID, "the device has no tts.device-id alias")
			return
		case err != nil:
			a.lookupFailed(ctx, j, "deviceId", err)
			return
		}
		ident, deviceRef = id, target.ApplicationID+"/"+id
	} else {
		eui, err := a.devices.DevEUI(ctx, tenant, j.device)
		switch {
		case errors.Is(err, ErrNoDevEUI):
			a.refuse(msg, j, ResultRefusedNoDevEUI, ReasonNoDevEUI, "the device has no lorawan.deveui alias")
			return
		case err != nil:
			a.lookupFailed(ctx, j, "devEUI", err)
			return
		}
		ident, pendEUI = eui, eui
	}
	if a.opts.Rate != nil && !a.opts.Rate.Allow(tenant) {
		a.refuse(msg, j, ResultRefusedRate, ReasonRateLimited, "the tenant is over its outbound call ceiling; retry shortly")
		return
	}

	nonce, won, err := a.claimer.ClaimDispatch(ctx, tenant, j.token, j.nonce)
	if err != nil {
		if ctx.Err() == nil {
			a.count(ResultClaimFailed)
			log.Debug().Err(err).Str("tenant", tenant).Str("command", j.token).
				Msg("Could not confirm a command's dispatch; not sending it, leaving it unacked to retry on redelivery.")
		}
		return
	}
	if !won {
		a.count(ResultClaimLost)
		ack(msg)
		return
	}
	j.nonce = nonce

	// Expiry again, now that the lookups and the claim have taken their time: a downlink queued after
	// the platform gave up would actuate late. The row is claimed, so the answer quotes the claim's
	// nonce. The send is also bounded by the expiry, and the network server is told it.
	if a.expired(j) {
		a.count(ResultRefusedExpired)
		a.finish(msg, j, cmddownlink.ResponseEnvelope{
			CommandToken: j.token, Success: false, Reach: cmddownlink.ReachUnsent, DispatchNonce: j.nonce,
			Error: strPtr("the command expired before it could be sent"), Payload: reasonPayload(ReasonExpired),
		})
		return
	}
	sendCtx, cancel := context.WithTimeout(ctx, a.opts.SendTimeout)
	if !j.expires.IsZero() {
		var c2 context.CancelFunc
		sendCtx, c2 = context.WithDeadline(sendCtx, j.expires)
		defer c2()
	}
	queueID, err := a.sender.Enqueue(sendCtx, target, ident, params, j.expires)
	cancel()
	if err != nil {
		var se *SendError
		if !errors.As(err, &se) {
			// An implementation that did not classify: it may have reached the wire.
			se = &SendError{Reach: cmddownlink.ReachUnanswered, Text: "the downlink could not be completed"}
		}
		reason, result := ReasonNetworkServer, ResultUnsent
		if se.Reach == cmddownlink.ReachUnanswered {
			result = ResultUnanswered
		}
		if se.Blocked {
			reason = ReasonEgressBlocked
		}
		a.count(result)
		a.finish(msg, j, cmddownlink.ResponseEnvelope{
			CommandToken: j.token, Success: false, Reach: se.Reach, DispatchNonce: j.nonce,
			Error: strPtr(se.Text), Payload: reasonPayload(reason),
		})
		return
	}
	if params.Confirmed {
		a.awaitAck(ctx, msg, j, integration, pendEUI, deviceRef, queueID)
		return
	}
	a.count(ResultQueued)
	body, _ := json.Marshal(map[string]string{"stage": "queued", "queueItemId": queueID})
	a.finish(msg, j, cmddownlink.ResponseEnvelope{
		CommandToken: j.token, Success: true, DispatchNonce: j.nonce, Payload: strPtr(string(body)),
	})
}

// awaitAck stores the pending record of a confirmed downlink the network server accepted and acks
// the command message WITHOUT answering the command: it stays SENT, and the acknowledgement feed
// (or the record's deadline) settles it. If the record cannot be stored the downlink is already
// queued and nothing could ever close the command, so it is answered UNANSWERED now (TIMEOUT:
// the device may act) rather than left to the expiry sweep.
func (a *Adapter) awaitAck(ctx context.Context, msg messaging.Message, j *job, integration, devEUI, deviceRef, queueID string) {
	deadline := a.opts.Now().Add(a.opts.AckWait)
	if !j.expires.IsZero() && j.expires.Before(deadline) {
		deadline = j.expires
	}
	err := a.opts.Pending.Add(ctx, Pending{
		Tenant: j.tenant, Integration: integration, DeviceToken: j.device, DevEUI: devEUI, DeviceRef: deviceRef,
		CommandToken: j.token, DispatchNonce: j.nonce, QueueItemID: strings.ToLower(queueID), DeadlineAt: deadline,
	})
	if err != nil {
		a.count(ResultPendingFailed)
		log.Warn().Err(err).Str("tenant", j.tenant).Str("command", j.token).
			Msg("A confirmed LoRaWAN downlink was queued but its acknowledgement could not be tracked; answering the command as unanswered.")
		a.finish(msg, j, cmddownlink.ResponseEnvelope{
			CommandToken: j.token, Success: false, Reach: cmddownlink.ReachUnanswered, DispatchNonce: j.nonce,
			Error:   strPtr("the network server queued the confirmed downlink but the platform could not track its acknowledgement"),
			Payload: reasonPayload(ReasonUntracked),
		})
		return
	}
	a.count(ResultQueuedConfirmed)
	ack(msg)
}

// refuse answers a command UNSENT before any claim, quoting the envelope's own nonce, and acks.
// If the answer cannot be published nothing was sealed: the message stays unacked and is refused
// again on redelivery.
func (a *Adapter) refuse(msg messaging.Message, j *job, result string, reason Reason, text string) {
	a.count(result)
	a.finish(msg, j, cmddownlink.ResponseEnvelope{
		CommandToken: j.token, Success: false, Reach: cmddownlink.ReachUnsent, DispatchNonce: j.nonce,
		Error: strPtr(text), Payload: reasonPayload(reason),
	})
}

// finish publishes the outcome and acks only if it was published.
func (a *Adapter) finish(msg messaging.Message, j *job, env cmddownlink.ResponseEnvelope) {
	if err := cmddownlink.PublishResponse(a.responses, j.tenant, j.device, env, a.opts.ResponseAttempts, a.opts.ResponseBackoff); err != nil {
		a.count(ResultResponseFailed)
		log.Warn().Err(err).Str("tenant", j.tenant).Str("command", j.token).
			Msg("Could not publish a LoRaWAN downlink outcome after local retries; the command will TIMEOUT.")
		// Left unacked on purpose. Before the claim the redelivery refuses again; after it, the
		// redelivery loses the claim (the dispatch nonce has rotated) and is acked without sending
		// anything, so the downlink is never repeated.
		return
	}
	ack(msg)
}

func (a *Adapter) lookupFailed(ctx context.Context, j *job, what string, err error) {
	if ctx.Err() != nil {
		return
	}
	a.count(ResultLookupFailed)
	log.Warn().Err(err).Str("tenant", j.tenant).Str("command", j.token).Str("lookup", what).
		Msg("Could not read what a LoRaWAN downlink needs; leaving the command unacked to retry on redelivery.")
}

func (a *Adapter) expired(j *job) bool {
	return !j.expires.IsZero() && !a.opts.Now().Before(j.expires)
}

// integrationOf reads the integration token out of the projected source "integration:<token>".
func integrationOf(source string) (string, bool) {
	rest, ok := strings.CutPrefix(source, string(transport.Integration)+":")
	if !ok || rest == "" || strings.ContainsAny(rest, " \t\r\n") || len(rest) > 128 {
		return "", false
	}
	return rest, true
}

func reasonPayload(r Reason) *string {
	p := fmt.Sprintf(`{"reason":%q}`, string(r))
	return &p
}

func strPtr(s string) *string { return &s }

func clipText(s string) string {
	if len(s) > 200 {
		return strings.ToValidUTF8(s[:200], "") + "..."
	}
	return s
}

func clipLog(s string) string { return clipText(s) }
