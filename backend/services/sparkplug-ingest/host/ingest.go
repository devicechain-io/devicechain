// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package host

import (
	"context"
	"hash/fnv"
	"sort"
	"strconv"
	"time"

	esmodel "github.com/devicechain-io/dc-event-sources/model"
	esproto "github.com/devicechain-io/dc-event-sources/proto"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/rs/zerolog/log"
)

// IngestPolicy is the per-source parameters the shared ingester needs on every
// call: the event Source stamp and how (or whether) to auto-register a device the
// source has not seen before. It is fixed per connection (a Client holds its own).
type IngestPolicy struct {
	// Source is stamped onto every emitted UnresolvedEvent (carried through to each
	// resolved event), e.g. "sparkplug:{hostId}".
	Source string
	// DeviceTypeToken is the device type stamped on an auto-registered device.
	DeviceTypeToken string
	// AutoRegister enables creating a device on first sight; false drops an unknown
	// device (counted) instead.
	AutoRegister bool
}

// GraphQLClient is the narrow slice of svcclient.Client the registrar needs. host
// depends on the interface so the registrar is testable without a live
// device-management or a minted service token.
type GraphQLClient interface {
	Query(ctx context.Context, baseURL, tenant, query string, variables map[string]any, out any) error
}

// EventWriter is the durable JetStream write the emitter needs (satisfied by
// messaging.MessageWriter). host depends on the interface so the emitter is
// testable without a live NATS.
type EventWriter interface {
	WriteMessages(ctx context.Context, msgs ...messaging.Message) error
}

// IngestMetrics are the optional Prometheus counters the ingest path updates; any
// nil field is skipped so the ingester is usable in tests without a registry.
type IngestMetrics struct {
	MeasurementsEmitted prometheus.Counter // individual samples durably written
	PresenceEmitted     prometheus.Counter // presence StateChange events durably written
	DevicesRegistered   prometheus.Counter // devices auto-created on first sight
	UnknownDropped      prometheus.Counter // samples/presence dropped: unknown device, auto-register off
}

// resolveOutcome distinguishes how a device external id resolved, so the ingester
// can meter registrations and drops without the registrar owning a metrics handle.
type resolveOutcome int

const (
	resolveDropped resolveOutcome = iota // unknown device + auto-register off — do not ingest
	resolveFound                         // the device already existed
	resolveCreated                       // the device was auto-registered on this call
)

// The device-management GraphQL operations. Field names are pinned to the schema
// (DeviceCreateRequest.token/externalId/deviceTypeToken); the graphql-go fork
// rejects an unknown field sent through a variable, so a typo here fails the call
// loudly rather than silently creating a half-formed device.
const (
	lookupByExternalId = `query($externalIds: [String!]!) {
  devicesByExternalId(externalIds: $externalIds) { token externalId }
}`
	createDeviceMutation = `mutation($request: DeviceCreateRequest) {
  createDevice(request: $request) { token }
}`
)

// Registrar resolves a Sparkplug external id to a DeviceChain device token over the
// cross-service GraphQL client (ADR-044), auto-registering the device when the
// source's policy allows. A resolved token is cached per (tenant, external id) so
// steady-state DATA messages never re-hit device-management — only the first sight
// of a device does.
type Registrar struct {
	client GraphQLClient
	url    string

	cache *tokenCache
}

// NewRegistrar binds a registrar to a GraphQL client and device-management's
// endpoint URL.
func NewRegistrar(client GraphQLClient, graphqlURL string) *Registrar {
	return &Registrar{client: client, url: graphqlURL, cache: newTokenCache()}
}

// Resolve maps tenant + external id to a device token. It returns the token and how
// it resolved (found / created / dropped) on success, or an error for a RETRYABLE
// failure (device-management unreachable, a transient create failure). A
// resolveDropped outcome (unknown device, auto-register off) is a definitive skip,
// not an error — the caller counts it and moves on.
func (r *Registrar) Resolve(ctx context.Context, tenant, externalId string, policy IngestPolicy) (string, resolveOutcome, error) {
	if tok, ok := r.cache.get(tenant, externalId); ok {
		return tok, resolveFound, nil
	}
	tok, found, err := r.lookup(ctx, tenant, externalId)
	if err != nil {
		return "", resolveDropped, err
	}
	if found {
		r.cache.put(tenant, externalId, tok)
		return tok, resolveFound, nil
	}
	if !policy.AutoRegister {
		return "", resolveDropped, nil
	}

	token := DeriveDeviceToken(externalId)
	// TODO (follow-up): a persistently-failing create (e.g. a deviceTypeToken that
	// passes the grammar check but names no existing type) is retried by the caller on
	// every message from that device, spending the full bounded budget each time. It is
	// visible (device-management errors + IngestFailures), but a short per-externalId
	// negative backoff here would keep one config mistake from throttling a source.
	if err := r.create(ctx, tenant, token, externalId, policy.DeviceTypeToken); err != nil {
		// A create can fail because a concurrent burst already registered this device
		// (the deterministic token collides on the unique index). Re-look-up: if the
		// device now exists, use the winner. Only a create failure that ALSO leaves no
		// device behind (a bad device type, or device-management down) is returned for
		// the caller to retry.
		if again, found2, lerr := r.lookup(ctx, tenant, externalId); lerr == nil && found2 {
			r.cache.put(tenant, externalId, again)
			return again, resolveFound, nil
		}
		return "", resolveDropped, err
	}
	r.cache.put(tenant, externalId, token)
	return token, resolveCreated, nil
}

// lookup asks device-management for a device carrying externalId, returning its
// token and whether one was found.
func (r *Registrar) lookup(ctx context.Context, tenant, externalId string) (string, bool, error) {
	var out struct {
		DevicesByExternalId []struct {
			Token string `json:"token"`
		} `json:"devicesByExternalId"`
	}
	vars := map[string]any{"externalIds": []string{externalId}}
	if err := r.client.Query(ctx, r.url, tenant, lookupByExternalId, vars, &out); err != nil {
		return "", false, err
	}
	if len(out.DevicesByExternalId) == 0 {
		return "", false, nil
	}
	return out.DevicesByExternalId[0].Token, true, nil
}

// create registers a device with the derived token, the raw external id, and the
// source's device type.
func (r *Registrar) create(ctx context.Context, tenant, token, externalId, deviceTypeToken string) error {
	var out struct {
		CreateDevice struct {
			Token string `json:"token"`
		} `json:"createDevice"`
	}
	vars := map[string]any{"request": map[string]any{
		"token":           token,
		"externalId":      externalId,
		"deviceTypeToken": deviceTypeToken,
	}}
	return r.client.Query(ctx, r.url, tenant, createDeviceMutation, vars, &out)
}

// assertedActiveQuery asks device-state for every ASSERTED + active device the
// calling tenant owns (ADR-067 SP4b). The response is tenant-scoped by the caller's
// token, so a source only ever sees its own tenant's asserted-online devices.
const assertedActiveQuery = `query {
  assertedActiveDeviceStates { externalId sessionId }
}`

// AssertedDevice is one asserted-active device the failover reconciliation must
// account for: its external id (the Sparkplug "{group}/{node}[/{device}]" identity to
// probe) and the presence SessionId last applied to it (the projection's ordering
// epoch, which floors the adapter's epoch generator).
type AssertedDevice struct {
	ExternalId string
	SessionId  uint64
}

// Reconciler reads the device-state presence projection — the authoritative source of
// which devices the platform believes are online (ADR-067 SP4b) — over the same
// cross-service GraphQL client the registrar uses (a state:read-scoped service token).
// It is the failover repopulation input: a newly-elected leader enumerates the
// asserted-active devices, floors its epoch generator from them, and probes each.
type Reconciler struct {
	client GraphQLClient
	url    string
}

// NewReconciler binds a reconciler to a GraphQL client and device-state's endpoint URL.
func NewReconciler(client GraphQLClient, deviceStateURL string) *Reconciler {
	return &Reconciler{client: client, url: deviceStateURL}
}

// AssertedActive returns the tenant's asserted-active devices and the maximum
// SessionId among them (0 when there are none) — the floor input for the adapter's
// epoch generator so a fresh emission always supersedes any stored session. A device
// whose external id is null is skipped: it cannot be reconciled against a Sparkplug
// topic (and only a Sparkplug producer sets one at GA).
func (r *Reconciler) AssertedActive(ctx context.Context, tenant string) ([]AssertedDevice, uint64, error) {
	var out struct {
		AssertedActiveDeviceStates []struct {
			ExternalId *string `json:"externalId"`
			SessionId  string  `json:"sessionId"`
		} `json:"assertedActiveDeviceStates"`
	}
	if err := r.client.Query(ctx, r.url, tenant, assertedActiveQuery, nil, &out); err != nil {
		return nil, 0, err
	}
	devices := make([]AssertedDevice, 0, len(out.AssertedActiveDeviceStates))
	var max uint64
	for _, d := range out.AssertedActiveDeviceStates {
		if d.ExternalId == nil || *d.ExternalId == "" {
			continue
		}
		session, err := strconv.ParseUint(d.SessionId, 10, 64)
		if err != nil {
			// A malformed sessionId would poison the floor; skip the row and let the
			// probe still cover its external id (fail toward reconciling, not toward a
			// bad epoch floor).
			log.Warn().Str("tenant", tenant).Str("externalId", *d.ExternalId).Str("sessionId", d.SessionId).
				Msg("Skipping an asserted device with an unparseable sessionId during reconciliation.")
			continue
		}
		if session > max {
			max = session
		}
		devices = append(devices, AssertedDevice{ExternalId: *d.ExternalId, SessionId: session})
	}
	return devices, max, nil
}

// Emitter builds an UnresolvedEvent from a device's samples and writes it durably
// to the shared inbound-events stream, reusing the event-sources wire contract so
// the device-management resolver ingests it unchanged.
type Emitter struct {
	writer EventWriter
	now    func() time.Time
}

// NewEmitter binds an emitter to a durable message writer and a clock (nil ⇒
// time.Now).
func NewEmitter(writer EventWriter, now func() time.Time) *Emitter {
	if now == nil {
		now = time.Now
	}
	return &Emitter{writer: writer, now: now}
}

// Emit writes the samples for one device as a measurements UnresolvedEvent under
// the connection's tenant. The tenant flows through core.WithTenant into the
// message subject (never from the Sparkplug topic — the SP3a connection-scoped
// invariant). DedupID is empty and AltId is nil: an external-broker source has no
// DeviceChain capture sequence, so this is the HTTP-ingest at-least-once posture;
// producer-stable exactly-once identity is SP4/M12 work.
func (e *Emitter) Emit(ctx context.Context, tenant, source, deviceToken string, samples []Sample) error {
	entries := make([]esmodel.UnresolvedMeasurementsEntry, 0, len(samples))
	var latest int64
	for _, s := range samples {
		if s.Time > latest {
			latest = s.Time
		}
		occurred := time.UnixMilli(s.Time).UTC().Format(time.RFC3339Nano)
		entries = append(entries, esmodel.UnresolvedMeasurementsEntry{
			// Format 'f', not 'g': 'g' switches to exponent notation for large
			// magnitudes (1e6 → "1e+06"), which the resolver's Int-declared metric
			// validation (strconv.ParseInt) rejects — dead-lettering the WHOLE event,
			// every sibling sample with it. 'f' with -1 precision still yields the
			// shortest round-tripping decimal, but never an exponent, so an integer
			// like 12345678 arrives as "12345678" and parses as both Int and Double.
			Measurements: map[string]string{s.Name: strconv.FormatFloat(s.Value, 'f', -1, 64)},
			OccurredTime: &occurred,
		})
	}
	if latest == 0 {
		latest = e.now().UnixMilli() // never emit a year-0/1970 event time
	}
	ev := &esmodel.UnresolvedEvent{
		Source:        source,
		Device:        deviceToken,
		EventType:     esmodel.Measurement,
		OccurredTime:  time.UnixMilli(latest).UTC(),
		ProcessedTime: e.now().UTC(),
		Payload:       &esmodel.UnresolvedMeasurementsPayload{Entries: entries},
	}
	encoded, err := esproto.MarshalUnresolvedEvent(ev)
	if err != nil {
		return err
	}
	tctx := core.WithTenant(ctx, tenant)
	return e.writer.WriteMessages(tctx, messaging.Message{
		Key:     []byte(deviceToken),
		Value:   encoded,
		DedupID: measurementDedupID(tenant, deviceToken, latest, samples),
	})
}

// EmitPresence writes one presence transition as a StateChange UnresolvedEvent
// (ADR-067) under the connection's tenant. OccurredTime is the receipt-clock time
// the session machine stamped (never the Sparkplug payload ts). SessionId rides the
// wire as a string (an epoch-sized UnixNano would lose precision through a JSON hop).
// The DedupID makes a retry — or a failover re-derivation — of the same (device,
// session, state) transition idempotent at JetStream (M12).
func (e *Emitter) EmitPresence(ctx context.Context, tenant, source, deviceToken string, ev PresenceEvent) error {
	state := esmodel.PresenceDisconnected
	if ev.Connected {
		state = esmodel.PresenceConnected
	}
	occurred := ev.OccurredAt.UTC()
	occStr := occurred.Format(time.RFC3339Nano)
	uev := &esmodel.UnresolvedEvent{
		Source:        source,
		Device:        deviceToken,
		EventType:     esmodel.StateChange,
		OccurredTime:  occurred,
		ProcessedTime: e.now().UTC(),
		Payload: &esmodel.UnresolvedStateChangePayload{
			State:        state,
			Reason:       ev.Reason,
			SessionId:    strconv.FormatUint(ev.SessionId, 10),
			OccurredTime: &occStr,
		},
	}
	encoded, err := esproto.MarshalUnresolvedEvent(uev)
	if err != nil {
		return err
	}
	tctx := core.WithTenant(ctx, tenant)
	return e.writer.WriteMessages(tctx, messaging.Message{
		Key:     []byte(deviceToken),
		Value:   encoded,
		DedupID: presenceDedupID(tenant, deviceToken, ev),
	})
}

// dedupID builds a compact, deterministic JetStream Nats-Msg-Id from its parts. It
// is a fixed-width fnv-64a over NUL-separated parts (base36 ≈ 13 chars), so a
// device-controlled input can never inflate the id — the InboundEvents dedup window
// holds these in memory for its full window, so id size is a memory cost. Stable for
// a retry of the same logical event, distinct for genuinely different content.
func dedupID(parts ...string) string {
	h := fnv.New64a()
	for _, p := range parts {
		_, _ = h.Write([]byte(p))
		_, _ = h.Write([]byte{0})
	}
	return "sp" + strconv.FormatUint(h.Sum64(), 36)
}

// presenceDedupID keys a StateChange on (tenant, device, session, state): a given
// session's CONNECTED and DISCONNECTED are each emitted once, so a retry or a
// failover re-derivation dedups, while a genuinely new session (new epoch) or the
// opposite transition is distinct.
func presenceDedupID(tenant, deviceToken string, ev PresenceEvent) string {
	state := "0"
	if ev.Connected {
		state = "1"
	}
	return dedupID("sc", tenant, deviceToken, strconv.FormatUint(ev.SessionId, 10), state)
}

// measurementDedupID keys a measurement batch on (tenant, device, occurred-time, and
// the batch's sorted name=value=time content), so an emit-retry of the identical
// batch dedups but two distinct batches sharing an occurred-time stay distinct (the
// content hash keeps them apart — the SP3b B2 collision moved to the id layer would
// otherwise silently drop a distinct reading).
func measurementDedupID(tenant, deviceToken string, occurredMillis int64, samples []Sample) string {
	pairs := make([]string, 0, len(samples))
	for _, s := range samples {
		pairs = append(pairs, s.Name+"="+strconv.FormatFloat(s.Value, 'f', -1, 64)+"@"+strconv.FormatInt(s.Time, 10))
	}
	sort.Strings(pairs)
	parts := append([]string{"m", tenant, deviceToken, strconv.FormatInt(occurredMillis, 10)}, pairs...)
	return dedupID(parts...)
}

// Ingester turns an accepted Sparkplug message's samples into durable DeviceChain
// telemetry: resolve (and if needed register) the device, then emit. It is shared
// across all sources; the per-connection tenant and IngestPolicy are passed in on
// every call. It owns the ingest metrics so the registrar and emitter stay pure.
//
// NOTE (ADR-023 governance, deferred): this ingress path is NOT yet behind the
// per-tenant ingest rate gate that event-sources applies before InboundEvents, so a
// misbehaving/compromised customer broker can exceed a tenant's ingest ceiling. It
// is an OPT-IN source an operator deliberately connects to (not open-internet device
// ingest), which bounds the exposure, but a per-tenant limiter here is owed —
// tracked as a follow-up. Any limiter added here MUST stay label-free per tenant (no
// per-tenant metric labels — the ADR-023 cardinality lesson), as these counters do.
type Ingester struct {
	registrar *Registrar
	emitter   *Emitter
	metrics   IngestMetrics
}

// NewIngester assembles the ingest pipeline.
func NewIngester(registrar *Registrar, emitter *Emitter, metrics IngestMetrics) *Ingester {
	return &Ingester{registrar: registrar, emitter: emitter, metrics: metrics}
}

// Ingest resolves the device for externalId and emits its samples. It returns nil
// when the message was fully handled — whether emitted OR definitively dropped
// (unknown device, auto-register off) — and an error ONLY for a retryable failure
// (device-management or NATS unreachable), which the caller may retry within the
// session.
func (ing *Ingester) Ingest(ctx context.Context, tenant string, policy IngestPolicy, externalId string, samples []Sample) error {
	token, outcome, err := ing.registrar.Resolve(ctx, tenant, externalId, policy)
	if err != nil {
		return err
	}
	if outcome == resolveDropped {
		incr(ing.metrics.UnknownDropped, len(samples))
		log.Debug().Str("tenant", tenant).Str("externalId", externalId).Int("samples", len(samples)).
			Msg("Dropping Sparkplug samples for an unregistered device (auto-registration is off for this source).")
		return nil
	}
	if outcome == resolveCreated {
		incr(ing.metrics.DevicesRegistered, 1)
		log.Info().Str("tenant", tenant).Str("externalId", externalId).Str("token", token).
			Msg("Auto-registered a Sparkplug device.")
	}
	if err := ing.emitter.Emit(ctx, tenant, policy.Source, token, samples); err != nil {
		return err
	}
	incr(ing.metrics.MeasurementsEmitted, len(samples))
	return nil
}

// IngestPresence resolves each presence event's device (auto-registering it under the
// source's policy — ASSERTED-on-first-sight happens in the projection when the
// StateChange lands) and emits a StateChange. It returns nil when every event was
// fully handled (emitted or definitively dropped) and an error for a retryable
// failure. The whole slice is re-processed on a retry; the DedupID makes an
// already-emitted event idempotent, so re-running is safe.
func (ing *Ingester) IngestPresence(ctx context.Context, tenant string, policy IngestPolicy, events []PresenceEvent) error {
	for _, ev := range events {
		token, outcome, err := ing.registrar.Resolve(ctx, tenant, ev.ExternalId, policy)
		if err != nil {
			return err
		}
		if outcome == resolveDropped {
			incr(ing.metrics.UnknownDropped, 1)
			log.Debug().Str("tenant", tenant).Str("externalId", ev.ExternalId).
				Msg("Dropping Sparkplug presence for an unregistered device (auto-registration is off for this source).")
			continue
		}
		if outcome == resolveCreated {
			incr(ing.metrics.DevicesRegistered, 1)
		}
		if err := ing.emitter.EmitPresence(ctx, tenant, policy.Source, token, ev); err != nil {
			return err
		}
		incr(ing.metrics.PresenceEmitted, 1)
	}
	return nil
}

// incr adds n to a counter, tolerating a nil counter (tests) and a zero/negative n.
func incr(c prometheus.Counter, n int) {
	if c != nil && n > 0 {
		c.Add(float64(n))
	}
}
