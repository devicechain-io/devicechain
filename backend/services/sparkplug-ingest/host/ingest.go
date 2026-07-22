// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package host

import (
	"context"
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
	DevicesRegistered   prometheus.Counter // devices auto-created on first sight
	UnknownDropped      prometheus.Counter // samples dropped: unknown device, auto-register off
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
		Key:   []byte(deviceToken),
		Value: encoded,
	})
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

// incr adds n to a counter, tolerating a nil counter (tests) and a zero/negative n.
func incr(c prometheus.Counter, n int) {
	if c != nil && n > 0 {
		c.Add(float64(n))
	}
}
