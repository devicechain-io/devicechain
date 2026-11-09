// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"github.com/devicechain-io/dc-event-sources/integration"
	"github.com/devicechain-io/dc-event-sources/model"
	"github.com/devicechain-io/dc-microservice/cmddownlink"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/eventtype"
	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/integrationcodec"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/sourcelimit"
	"github.com/devicechain-io/dc-microservice/transport"
	"github.com/rs/zerolog/log"
)

// The inbound integration route.
//
//	POST /{instanceId}/{tenant}/integrations/{integration}
//	POST /{instanceId}/{tenant}/integrations/{integration}/{messageType}
//	POST /integrations/{tenant}/{integration}[/{messageType}]   (the same, this instance implied)
//
// The second form is what the chart's front end forwards for the public
// POST /api/integrations/{tenant}/{integration}[/{messageType}].
//
// A network server or a webhook posts here under a bearer token that belongs to the integration
// the URL names. The body is read by the integration's REGISTERED type and never by what it looks
// like, each uplink in it is turned into an ordinary measurement event for the device its
// identifier names, and the event is published marked as the integration's so that
// device-management, which authenticates the token again, trusts the device the sender named.
//
// The order of the steps is the design, not a detail:
//
//  1. the tenant and integration names are checked against the token grammar;
//  2. the PRE-CHECK proves the bearer token, from memory when it can, charging a wrong token to
//     the integration's own bad-credential bucket before device-management is asked;
//  3. only then the tenant's message gate, so a sender that cannot authenticate never spends the
//     tenant's allowance;
//  4. the body, under a cap, then the codec, then each uplink's device;
//  5. the reading gate, the backpressure gate and the publish.
//
// 🔴 EVERY ANSWER IS A STATUS CODE A NETWORK SERVER ACTS ON. A tenant over its ceiling is
// answered 202 and counted, because a 429 loses the message all the same and The Things Stack
// can then suspend the webhook for a cooldown and drop traffic that was inside the ceiling.

// Results of an integration request (the result label of integration_requests_total). A request
// has exactly one, except precheck_stale, which marks a request answered from a last-known-good
// pre-check and is counted in addition to the result it ended in.
const (
	ResultAccepted            = "accepted"
	ResultIgnoredType         = "ignored_type"
	ResultBadEnvelope         = "bad_envelope"
	ResultUnsupportedEncoding = "unsupported_encoding"
	ResultUnauthorized        = "unauthorized"
	ResultBadAuthLimited      = "bad_auth_limited"
	ResultTooLarge            = "too_large"
	ResultShedAccepted        = "shed_accepted"
	ResultBackpressure        = "backpressure"
	ResultPrecheckStale       = "precheck_stale"
	ResultPrecheckUnavailable = "precheck_unavailable"
	ResultDisabled            = "disabled"
	ResultResolveUnavailable  = "resolve_unavailable"
	ResultPublishFailed       = "publish_failed"
	ResultInternalError       = "internal_error"
	// ResultSourceLimited: one source address spent its allowance for requests the pre-check
	// would have to ask device-management about. ResultPrecheckBudget: this replica's budget for
	// names nobody has vouched for was spent. Neither called device-management.
	ResultSourceLimited  = "source_limited"
	ResultPrecheckBudget = "precheck_budget"
)

// Results of one network-server downlink report (the result label of integration_downlink_reports_total).
const (
	// DownlinkReportPublished: the report named a device of the integration and was written to the
	// downlink-report stream for the adapter that holds the downlink.
	DownlinkReportPublished = "published"
	// DownlinkReportUnknownDevice: the device is not registered under this integration, so nothing was
	// published. A report is never accepted for a device the integration does not govern.
	DownlinkReportUnknownDevice = "unknown_device"
	// DownlinkReportNotRouted: the downlink feed is off (the LoRaWAN downlink is not enabled), so the
	// report was answered 204 and dropped unread, exactly as every such report was before.
	DownlinkReportNotRouted = "not_routed"
	// DownlinkReportRateLimited: the tenant sent reports faster than its allowance for them. The
	// request is answered 202 (a network server does not retry, and a 4xx can suspend its webhook) and
	// the report is dropped; the command it would have settled settles at its deadline.
	DownlinkReportRateLimited = "rate_limited"
	// DownlinkReportUntracked: the report names no downlink the platform can follow (a txack for a
	// MAC-command-only downlink has no queue item). Answered 204.
	DownlinkReportUntracked = "untracked"
)

// IntegrationDownlinkReportResults lists every downlink-report result.
var IntegrationDownlinkReportResults = []string{DownlinkReportPublished, DownlinkReportUnknownDevice, DownlinkReportNotRouted,
	DownlinkReportRateLimited, DownlinkReportUntracked}

// IntegrationRequestResults lists every request result, so a series can be exported at zero.
var IntegrationRequestResults = []string{
	ResultAccepted, ResultIgnoredType, ResultBadEnvelope, ResultUnsupportedEncoding, ResultUnauthorized,
	ResultBadAuthLimited, ResultTooLarge, ResultShedAccepted, ResultBackpressure, ResultPrecheckStale,
	ResultPrecheckUnavailable, ResultDisabled, ResultResolveUnavailable, ResultPublishFailed, ResultInternalError,
	ResultSourceLimited, ResultPrecheckBudget,
}

// Results of one uplink (the result label of integration_uplinks_total).
const (
	UplinkPublished         = "published"
	UplinkUnknownDevice     = "unknown_device"
	UplinkUndecoded         = "undecoded"
	UplinkTooManyReadings   = "too_many_readings"
	UplinkDuplicateSuppress = "duplicate_suppressed"
	UplinkShed              = "shed"
	// The results of an uplink for a device type with a payload decoder. None is reachable for a
	// type without one: Cut A accepts what it always has.
	UplinkTooLargeInput = "too_large_input"
	UplinkTooDeep       = "too_deep"
	UplinkBadFrame      = "bad_frame"
	UplinkBadMeta       = "bad_meta"
)

// IntegrationUplinkResults lists every uplink result.
var IntegrationUplinkResults = []string{
	UplinkPublished, UplinkUnknownDevice, UplinkUndecoded, UplinkTooManyReadings, UplinkDuplicateSuppress, UplinkShed,
	UplinkTooLargeInput, UplinkTooDeep, UplinkBadFrame, UplinkBadMeta,
}

// Results of a decoder-plan lookup (the result label of integration_decoder_lookups_total).
const (
	LookupPresent = "present"
	LookupAbsent  = "absent"
	// LookupUnsupported is a device-management that predates payload decoders: Cut A.
	LookupUnsupported = "unsupported"
	LookupError       = "error"
	// LookupStale marks a lookup answered from a remembered plan older than the TTL because
	// device-management could not be asked. It is counted in ADDITION to the result the plan
	// gave (present, absent or unsupported), as precheck_stale is for authentication.
	LookupStale = "stale"
)

// IntegrationDecoderLookupResults lists every lookup result.
var IntegrationDecoderLookupResults = []string{LookupPresent, LookupAbsent, LookupUnsupported, LookupError, LookupStale}

// IntegrationTypes are the type label values of integration_requests_total: the four registered
// types, and "unknown" for a request refused before its integration was known.
var IntegrationTypes = []string{
	string(integrationcodec.ChirpStackV4), string(integrationcodec.TTSV3), string(integrationcodec.GenericJSON),
	string(integrationcodec.GenericBinary), "unknown",
}

// IntegrationMetrics is how the route reports. Any nil field is skipped. None of them carries a
// tenant or an integration name: both are strings a sender chose before its credential was
// checked.
type IntegrationMetrics struct {
	Request func(integrationType, result string)
	Uplink  func(result string)
	// DownlinkReport counts a network-server downlink report by result.
	DownlinkReport       func(result string)
	FlattenSkipped       func(reason string, n int)
	UnregisteredOverflow func()
	// DecoderLookup counts a decoder-plan lookup by result. DecoderCharge adds the readings an
	// uplink for a type with a decoder was charged.
	DecoderLookup func(result string)
	DecoderCharge func(readings int)
}

func (m IntegrationMetrics) decoderLookup(result string) {
	if m.DecoderLookup != nil {
		m.DecoderLookup(result)
	}
}

func (m IntegrationMetrics) decoderCharge(n int) {
	if m.DecoderCharge != nil {
		m.DecoderCharge(n)
	}
}

func (m IntegrationMetrics) downlinkReport(result string) {
	if m.DownlinkReport != nil {
		m.DownlinkReport(result)
	}
}

func (m IntegrationMetrics) request(t, result string) {
	if m.Request != nil {
		m.Request(t, result)
	}
}

func (m IntegrationMetrics) uplink(result string) {
	if m.Uplink != nil {
		m.Uplink(result)
	}
}

// IntegrationRoute is everything the route needs besides the source's own gates.
type IntegrationRoute struct {
	// Auth is the pre-check, Devices the cached device lookup and Undecoded the batched marker.
	Auth *integration.Authenticator
	// Sources rations the requests the pre-check would have to ask device-management about, per
	// source address (TrustedProxies decides whose X-Forwarded-For is believed). Required.
	Sources        *sourcelimit.Limiter
	TrustedProxies []netip.Prefix
	Devices        *integration.Devices
	Undecoded      *integration.Undecoded
	// Decoders tells the route which device types have a payload decoder. Nil means none does
	// (Cut A for every uplink), which is also what a route built before decoders existed does.
	Decoders *integration.Decoders
	// Seal seals the bearer token the sender presented for the hop to device-management, bound to
	// the tenant and the integration (credential.IntegrationSealKind).
	Seal func(tenant, integrationToken, bearer string) ([]byte, error)
	// Publish writes one marked event to inbound-events and returns whether it was durably
	// published. It must keep the event's Source: the route has stamped it.
	// An event of type IntegrationUplink goes to its own stream, never to inbound-events.
	Publish func(source, tenant string, event *model.UnresolvedEvent, payload interface{}, dedupID string) error
	// PublishDownlink writes one network-server downlink report (a ChirpStack ack or txack) to the
	// downlink-report stream and returns whether it was durably published. Nil means the downlink feed
	// is OFF (the chart derives that from the outbound-connectors area): the reports are not even read,
	// and are answered 204 as they always were.
	PublishDownlink func(tenant string, report cmddownlink.DownlinkReport) error
	// DownlinkLimiter meters the reports a tenant may send, keyed by tenant. Required with
	// PublishDownlink. Reports are not metered with telemetry (shedding one would turn a delivered
	// command into a TIMEOUT), so they need a limit of their own, sized to commands: one tenant's flood
	// of them must not fill the shared stream and evict other tenants' acknowledgements.
	DownlinkLimiter *sourcelimit.Limiter
	Metrics         IntegrationMetrics
	// Now is the receipt clock, replaced in tests.
	Now func() time.Time
}

func (rt *IntegrationRoute) now() time.Time {
	if rt.Now != nil {
		return rt.Now()
	}
	return time.Now()
}

// Validate refuses a route built without a part of it. A route missing its pre-check or its
// seal would not fail visibly, it would authenticate or mark nothing.
func (rt *IntegrationRoute) Validate() error {
	switch {
	case rt == nil:
		return errors.New("no integration route")
	case rt.Sources == nil:
		return errors.New("an integration route needs a per-source limiter")
	case rt.Auth == nil, rt.Devices == nil, rt.Undecoded == nil:
		return errors.New("an integration route needs its pre-check, its device lookup and its undecoded recorder")
	case rt.Seal == nil, rt.Publish == nil:
		return errors.New("an integration route needs a seal and a publish")
	case rt.PublishDownlink != nil && rt.DownlinkLimiter == nil:
		return errors.New("an integration route that carries downlink reports needs a limiter for them")
	}
	return nil
}

// UseIntegrations enables the integration route on this source. Without it the route answers
// 503 saying integrations are not available, rather than 404, which would read as a wrong URL.
// It must be called before the source starts.
func (es *HttpEventSource) UseIntegrations(rt *IntegrationRoute) error {
	if err := rt.Validate(); err != nil {
		return err
	}
	es.integrations = rt
	return nil
}

// bearerOf reads the token from the Authorization header, and only from there: a token in the URL
// would land in the ingress access log.
func bearerOf(r *http.Request) (string, bool) {
	h := r.Header.Get("Authorization")
	const prefix = "bearer "
	if len(h) <= len(prefix) || !strings.EqualFold(h[:len(prefix)], prefix) {
		return "", false
	}
	tok := strings.TrimSpace(h[len(prefix):])
	// No token this platform issues is longer; a longer one is not worth hashing and asking about.
	if tok == "" || len(tok) > 512 {
		return "", false
	}
	return tok, true
}

func (es *HttpEventSource) handleIntegration(w http.ResponseWriter, r *http.Request) {
	rt := es.integrations
	if rt == nil {
		http.Error(w, "integrations are not available on this instance", http.StatusServiceUnavailable)
		return
	}
	m := rt.Metrics
	tenant, slug, messageType := r.PathValue("tenant"), r.PathValue("integration"), r.PathValue("messageType")
	if err := core.ValidateToken(tenant); err != nil {
		m.request("unknown", ResultBadEnvelope)
		http.Error(w, fmt.Sprintf("invalid tenant in path: %v", err), http.StatusBadRequest)
		return
	}
	if err := core.ValidateToken(slug); err != nil {
		m.request("unknown", ResultBadEnvelope)
		http.Error(w, fmt.Sprintf("invalid integration in path: %v", err), http.StatusBadRequest)
		return
	}
	if len(messageType) > 64 {
		m.request("unknown", ResultBadEnvelope)
		http.Error(w, "message type in path is too long", http.StatusBadRequest)
		return
	}

	// 2. The pre-check.
	bearer, ok := bearerOf(r)
	if !ok {
		m.request("unknown", ResultUnauthorized)
		w.Header().Set("WWW-Authenticate", "Bearer")
		http.Error(w, "an Authorization: Bearer header carrying the integration's token is required", http.StatusUnauthorized)
		return
	}
	// 🔴 A SOURCE IS CHARGED FOR EVERY REQUEST THAT WOULD MAKE THE PRE-CHECK ASK DEVICE-MANAGEMENT:
	// a spray of invented names or tokens is, a sender whose token was accepted a moment ago is
	// not. The network server's steady traffic therefore costs its address nothing, however many
	// uplinks it sends, and a stranger's guesses are bounded by address before they are bounded by
	// name. The address is the connection's peer unless the peer is a trusted proxy.
	if rt.Auth.WouldAsk(tenant, slug, bearer) {
		src := gqlcore.TrustedSourceAddr(r, rt.TrustedProxies)
		if !rt.Sources.Allow(sourcelimit.SourceKey(src)) {
			m.request("unknown", ResultSourceLimited)
			w.Header().Set("Retry-After", strconv.Itoa(max(1, int(rt.Sources.Every().Seconds()+0.999))))
			http.Error(w, "too many unauthenticated requests from this source; retry later", http.StatusTooManyRequests)
			return
		}
	}
	checked, err := rt.Auth.Check(r.Context(), tenant, slug, bearer)
	switch {
	case err == nil:
	case errors.Is(err, integration.ErrUnauthorized):
		m.request("unknown", ResultUnauthorized)
		w.Header().Set("WWW-Authenticate", "Bearer")
		http.Error(w, "integration authentication failed", http.StatusUnauthorized)
		return
	case errors.Is(err, integration.ErrBudget):
		m.request("unknown", ResultPrecheckBudget)
		w.Header().Set("Retry-After", "1")
		http.Error(w, "too many unrecognised integrations; retry later", http.StatusTooManyRequests)
		return
	case errors.Is(err, integration.ErrLimited):
		m.request("unknown", ResultBadAuthLimited)
		w.Header().Set("Retry-After", "1")
		http.Error(w, "too many failed authentications for this integration", http.StatusTooManyRequests)
		return
	default:
		log.Warn().Err(err).Msg("Integration pre-check unavailable; refusing the request")
		m.request("unknown", ResultPrecheckUnavailable)
		http.Error(w, "integration authentication is unavailable; retry later", http.StatusServiceUnavailable)
		return
	}
	desc := checked.Descriptor
	typ := string(desc.Type)
	if checked.Stale {
		m.request(typ, ResultPrecheckStale)
	}
	// A switched-off integration's token is valid. Refusing it is not a bad credential and costs
	// the sender's bucket nothing.
	if !desc.Enabled {
		m.request(typ, ResultDisabled)
		http.Error(w, "this integration is disabled", http.StatusForbidden)
		return
	}

	// 3. The tenant's message gate, with the integration as the origin: its token is proven.
	//
	// A ChirpStack downlink report (ack, txack) is not telemetry and is not shed with it: shedding is
	// answered 202, the network server never resends, and a dropped acknowledgement would turn a
	// delivered command into a TIMEOUT. Its volume follows the commands the tenant sent, it is
	// authenticated, and its stream is small and bounded.
	if rt.PublishDownlink != nil && isDownlinkEvent(desc.Type, r, messageType) {
		if !rt.DownlinkLimiter.Allow(tenant) {
			m.downlinkReport(DownlinkReportRateLimited)
			m.request(typ, ResultShedAccepted)
			w.WriteHeader(http.StatusAccepted)
			return
		}
	} else if es.allow != nil && !es.allow(es.Id, tenant, time.Time{}, false, OriginIntegration) {
		m.request(typ, ResultShedAccepted)
		w.WriteHeader(http.StatusAccepted)
		return
	}

	// 4. The body.
	r.Body = http.MaxBytesReader(w, r.Body, integrationcodec.MaxBodyBytes)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			m.request(typ, ResultTooLarge)
			http.Error(w, fmt.Sprintf("request body is over the limit of %d bytes", integrationcodec.MaxBodyBytes), http.StatusRequestEntityTooLarge)
			return
		}
		m.request(typ, ResultBadEnvelope)
		http.Error(w, "unable to read request body", http.StatusBadRequest)
		return
	}
	es.received(es.Id, body)
	receivedAt := rt.now().UTC()

	result, err := integrationcodec.Decode(desc, &integrationcodec.Request{
		Body: body, Header: r.Header, Query: r.URL.Query(), ContentType: r.Header.Get("Content-Type"),
		MessageType: messageType, ReceivedAt: receivedAt, DownlinkReports: rt.PublishDownlink != nil,
	})
	switch {
	case err == nil:
	case errors.Is(err, integrationcodec.ErrUnsupportedEncoding):
		m.request(typ, ResultUnsupportedEncoding)
		http.Error(w, "the body is in an encoding this platform does not read: set the sender's integration "+
			"encoding to JSON (ChirpStack: the HTTP integration's \"JSON\" encoding)", http.StatusUnsupportedMediaType)
		return
	case errors.Is(err, integrationcodec.ErrMalformed):
		m.request(typ, ResultBadEnvelope)
		es.failed(es.Id, tenant, body, err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	default:
		// A defect or a settings document the codec cannot use: the sender did nothing wrong.
		log.Error().Err(err).Str("integration", slug).Msg("Integration codec failed")
		m.request(typ, ResultInternalError)
		http.Error(w, "the integration cannot be read; retry later", http.StatusServiceUnavailable)
		return
	}
	if len(result.Downlinks) > 0 {
		es.handleDownlinkReports(w, r, rt, desc, tenant, slug, bearer, result.Downlinks)
		return
	}
	if result.Ignored {
		switch {
		case result.Untracked:
			m.downlinkReport(DownlinkReportUntracked)
		case rt.PublishDownlink == nil && isDownlinkEvent(desc.Type, r, messageType):
			m.downlinkReport(DownlinkReportNotRouted)
		}
		m.request(typ, ResultIgnoredType)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	// The message gate was charged once before the body was read, so a flood is shed having cost
	// no read. An array carries up to 100 uplinks, each a device lookup that can record a sighting,
	// so the rest are charged now, before any lookup: one charge cannot buy a hundred resolves.
	for i := 1; i < len(result.Uplinks); i++ {
		if es.allow != nil && !es.allow(es.Id, tenant, time.Time{}, false, OriginIntegration) {
			m.request(typ, ResultShedAccepted)
			w.WriteHeader(http.StatusAccepted)
			return
		}
	}

	// Each uplink's device and readings, all before anything is published, so a request that is
	// refused is refused whole.
	source := transport.Qualify(transport.Integration, slug)
	type outgoing struct {
		event    *model.UnresolvedEvent
		payload  interface{}
		readings int
		// identifier is the sender's name the device was resolved from; the resolver re-checks it.
		identifier string
		// decoder says the uplink is carried raw to a payload decoder.
		decoder bool
	}
	var out []outgoing
	seen := make(map[string]struct{}, len(result.Uplinks))
	for i := range result.Uplinks {
		up := &result.Uplinks[i]
		looked, err := rt.Devices.Resolve(r.Context(), tenant, slug, bearer, up.Scheme, up.Identifier)
		if err != nil {
			// Any error at all: an old schema, a service token that cannot be minted, a timeout.
			// None of it says the sender's token is wrong, and none of it may publish.
			log.Warn().Err(err).Str("integration", slug).Msg("Integration device lookup failed; refusing the request")
			m.request(typ, ResultResolveUnavailable)
			http.Error(w, "device lookup is unavailable; retry later", http.StatusServiceUnavailable)
			return
		}
		switch looked.Outcome {
		case integration.Resolved:
		case integration.Unregistered:
			m.uplink(UplinkUnknownDevice)
			if looked.Overflow && !looked.Cached && m.UnregisteredOverflow != nil {
				m.UnregisteredOverflow()
			}
			continue
		case integration.Disabled:
			rt.Auth.Forget(tenant, slug, bearer)
			m.request(typ, ResultDisabled)
			http.Error(w, "this integration is disabled", http.StatusForbidden)
			return
		case integration.Unauthenticated:
			// The token stopped matching between the pre-check and now (a rotation).
			rt.Auth.Forget(tenant, slug, bearer)
			rt.Devices.Forget(tenant, slug)
			m.request(typ, ResultUnauthorized)
			w.Header().Set("WWW-Authenticate", "Bearer")
			http.Error(w, "integration authentication failed", http.StatusUnauthorized)
			return
		default:
			m.request(typ, ResultResolveUnavailable)
			http.Error(w, "device lookup is unavailable; retry later", http.StatusServiceUnavailable)
			return
		}

		// 5.3. Whether the device's type has a payload decoder. Asked only for a device that
		// resolved, with the type token that lookup returned.
		var plan integration.DecoderPlan
		if rt.Decoders != nil && looked.DeviceTypeToken != "" {
			dl, err := rt.Decoders.Lookup(r.Context(), tenant, slug, bearer, looked.DeviceTypeToken)
			switch {
			case err == nil:
			case errors.Is(err, integration.ErrDecoderUnauthenticated):
				m.decoderLookup(LookupError)
				rt.Auth.Forget(tenant, slug, bearer)
				rt.Devices.Forget(tenant, slug)
				rt.Decoders.Forget(tenant)
				m.request(typ, ResultUnauthorized)
				w.Header().Set("WWW-Authenticate", "Bearer")
				http.Error(w, "integration authentication failed", http.StatusUnauthorized)
				return
			default:
				// Anything but a device-management that predates the query: not knowing whether
				// a decoder exists must not become flattening, which would store readings a
				// decoder was meant to produce.
				log.Warn().Err(err).Str("integration", slug).Msg("Integration decoder lookup failed; refusing the request")
				m.decoderLookup(LookupError)
				m.request(typ, ResultResolveUnavailable)
				http.Error(w, "device lookup is unavailable; retry later", http.StatusServiceUnavailable)
				return
			}
			if dl.Stale {
				m.decoderLookup(LookupStale)
			}
			switch {
			case dl.Unsupported:
				m.decoderLookup(LookupUnsupported)
			case dl.Plan.Present:
				m.decoderLookup(LookupPresent)
				plan = dl.Plan
			default:
				m.decoderLookup(LookupAbsent)
			}
		}
		if plan.Present {
			dedup := integrationDedupKey(looked.DeviceToken, up.AltId)
			if _, dup := seen[dedup]; dup {
				m.uplink(UplinkDuplicateSuppress)
				continue
			}
			carried, charge, refusal := buildUplinkCarrier(desc, up, plan)
			if refusal != nil {
				m.uplink(refusal.uplink)
				m.request(typ, ResultBadEnvelope)
				es.failed(es.Id, tenant, body, fmt.Errorf("%s: %w", refusal.uplink, refusal.err))
				http.Error(w, refusal.err.Error(), http.StatusBadRequest)
				return
			}
			seen[dedup] = struct{}{}
			altId := up.AltId
			out = append(out, outgoing{
				payload: carried, readings: charge,
				event: &model.UnresolvedEvent{
					AltId:         &altId,
					Device:        looked.DeviceToken,
					OccurredTime:  up.Time,
					ProcessedTime: receivedAt,
					EventType:     eventtype.IntegrationUplink,
					Source:        source,
				},
				identifier: up.Identifier,
				decoder:    true,
			})
			continue
		}

		var link *integrationcodec.Link
		if desc.LinkMetrics {
			link = up.Link
		}
		var flat integrationcodec.Flattened
		if up.Decoded != nil {
			flat, err = integrationcodec.Flatten(up.Decoded, link)
			var tooMany integrationcodec.ErrTooManyReadings
			if errors.As(err, &tooMany) {
				m.uplink(UplinkTooManyReadings)
				m.request(typ, ResultBadEnvelope)
				es.failed(es.Id, tenant, body, fmt.Errorf("%w: %v", model.ErrTooManyReadings, err))
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			if m.FlattenSkipped != nil {
				for reason, n := range flat.Skipped {
					m.FlattenSkipped(reason, n)
				}
			}
		}
		if flat.Leaves == 0 {
			// 202 and counted, and recorded on the integration for the operator to see: an
			// uplink the network server forwarded without decoding (no codec on its device
			// profile) is the commonest way to look connected and store nothing.
			m.uplink(UplinkUndecoded)
			rt.Undecoded.Note(tenant, slug, bearer, up.SampleHex())
			continue
		}
		dedup := integrationDedupKey(looked.DeviceToken, up.AltId)
		if _, dup := seen[dedup]; dup {
			m.uplink(UplinkDuplicateSuppress)
			continue
		}
		seen[dedup] = struct{}{}

		altId := up.AltId
		payload := &model.UnresolvedMeasurementsPayload{Entries: []model.UnresolvedMeasurementsEntry{{Measurements: flat.Readings}}}
		out = append(out, outgoing{
			payload: payload, readings: len(flat.Readings),
			event: &model.UnresolvedEvent{
				AltId:         &altId,
				Device:        looked.DeviceToken,
				OccurredTime:  up.Time,
				ProcessedTime: receivedAt,
				EventType:     eventtype.Measurement,
				// The producer's stamp: the resolver refuses an integration event whose source is not
				// this, and the device's transport is read from it (commands to it are refused).
				Source: source,
			},
			identifier: up.Identifier,
		})
	}

	if len(out) > 0 {
		sealed, err := rt.Seal(tenant, slug, bearer)
		if err != nil {
			log.Error().Err(err).Msg("Could not seal the integration token; refusing the request")
			m.request(typ, ResultPublishFailed)
			http.Error(w, "unable to accept event: downstream unavailable", http.StatusServiceUnavailable)
			return
		}
		// 5. The shared backpressure gate comes after the tenant's own, as on every transport.
		if err := es.admit(es.Id); err != nil {
			m.request(typ, ResultBackpressure)
			writeBackpressured(w)
			return
		}
		for _, o := range out {
			if !es.readings(es.Id, tenant, time.Time{}, false, OriginIntegration, o.readings) {
				// Over the tenant's reading ceiling: accepted and counted, never a 429.
				m.uplink(UplinkShed)
				continue
			}
			if o.decoder {
				m.decoderCharge(o.readings)
			}
			// Marked as the integration's, and only here: the resolver opens the sealed token.
			o.event.Integration = slug
			o.event.SealedIntegrationToken = sealed
			o.event.IntegrationIdentifier = o.identifier
			if err := rt.Publish(es.Id, tenant, o.event, o.payload, integrationDedupID(tenant, o.event.Device, *o.event.AltId)); err != nil {
				if errors.Is(err, messaging.ErrStreamBackpressure) {
					m.request(typ, ResultBackpressure)
					writeBackpressured(w)
					return
				}
				m.request(typ, ResultPublishFailed)
				http.Error(w, "unable to accept event: downstream unavailable", http.StatusServiceUnavailable)
				return
			}
			m.uplink(UplinkPublished)
		}
	}
	m.request(typ, ResultAccepted)
	w.WriteHeader(http.StatusAccepted)
}

// isDownlinkEvent reports whether the request names a downlink report: ChirpStack by its event query
// value, The Things Stack by a webhook path that starts with "downlink" (downlink-ack, downlink/nack,
// downlink_failed, ...). A The Things Stack webhook posted to its bare base URL names nothing, so its
// reports are shed with telemetry; the documentation says to give each message type its own path.
func isDownlinkEvent(t integrationcodec.Type, r *http.Request, messageType string) bool {
	switch t {
	case integrationcodec.ChirpStackV4:
		ev := r.URL.Query().Get("event")
		return ev == integrationcodec.DownlinkAck || ev == integrationcodec.DownlinkTxAck
	case integrationcodec.TTSV3:
		seg := strings.NewReplacer("-", "", "_", "", "/", "").Replace(strings.ToLower(messageType))
		return strings.HasPrefix(seg, "downlink")
	}
	return false
}

// handleDownlinkReports carries a network server's reports about downlinks (ack, txack) to the
// adapter that holds those downlinks. It runs after the same authentication, enabled check and
// message gate as an uplink.
//
// 🔴 A REPORT IS ACCEPTED ONLY FOR A DEVICE THE AUTHENTICATED INTEGRATION GOVERNS. The device is
// resolved with the bearer the sender presented, under the integration it proved, exactly as an
// uplink's is, so an integration can only speak for devices registered to it (and in its device-type
// scope). The report is then stamped with the INTEGRATION's token, which the adapter checks against
// the one the downlink was sent through. Nothing in the report's body names the tenant or the
// integration: both come from the path the token was proven for.
func (es *HttpEventSource) handleDownlinkReports(w http.ResponseWriter, r *http.Request, rt *IntegrationRoute,
	desc *integrationcodec.Descriptor, tenant, slug, bearer string, reports []integrationcodec.DownlinkEvent) {
	m := rt.Metrics
	typ := string(desc.Type)
	if rt.PublishDownlink == nil {
		m.downlinkReport(DownlinkReportNotRouted)
		m.request(typ, ResultIgnoredType)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	var out []cmddownlink.DownlinkReport
	for _, d := range reports {
		looked, err := rt.Devices.Resolve(r.Context(), tenant, slug, bearer, d.Scheme, d.Identifier)
		if err != nil {
			log.Warn().Err(err).Str("integration", slug).Msg("Integration device lookup failed for a downlink report; refusing the request")
			m.request(typ, ResultResolveUnavailable)
			http.Error(w, "device lookup is unavailable; retry later", http.StatusServiceUnavailable)
			return
		}
		switch looked.Outcome {
		case integration.Resolved:
		case integration.Unregistered:
			m.downlinkReport(DownlinkReportUnknownDevice)
			continue
		case integration.Disabled:
			rt.Auth.Forget(tenant, slug, bearer)
			m.request(typ, ResultDisabled)
			http.Error(w, "this integration is disabled", http.StatusForbidden)
			return
		case integration.Unauthenticated:
			rt.Auth.Forget(tenant, slug, bearer)
			rt.Devices.Forget(tenant, slug)
			m.request(typ, ResultUnauthorized)
			w.Header().Set("WWW-Authenticate", "Bearer")
			http.Error(w, "integration authentication failed", http.StatusUnauthorized)
			return
		default:
			m.request(typ, ResultResolveUnavailable)
			http.Error(w, "device lookup is unavailable; retry later", http.StatusServiceUnavailable)
			return
		}
		out = append(out, cmddownlink.DownlinkReport{
			Integration: slug, Kind: d.Kind, DevEUI: d.Identifier, QueueItemID: d.QueueItemID, DeviceRef: d.DeviceRef,
			DeviceToken: looked.DeviceToken, Attempt: d.Attempt, ErrorNamespace: d.ErrorNamespace, ErrorName: d.ErrorName,
			Acknowledged: d.Acknowledged, FCntDown: d.FCntDown,
		})
	}
	for _, rep := range out {
		if err := rt.PublishDownlink(tenant, rep); err != nil {
			log.Warn().Err(err).Str("integration", slug).Msg("Could not publish a downlink report; refusing the request")
			m.request(typ, ResultPublishFailed)
			http.Error(w, "unable to accept event: downstream unavailable", http.StatusServiceUnavailable)
			return
		}
		m.downlinkReport(DownlinkReportPublished)
	}
	m.request(typ, ResultAccepted)
	w.WriteHeader(http.StatusAccepted)
}

// integrationDedupKey is the identity of one uplink within a request: the device it resolved to
// and the sender's own dedup key, joined by a byte neither can hold (a token is printable and an
// AltId is checked to be printable ASCII).
func integrationDedupKey(deviceToken, altId string) string {
	return deviceToken + "\x00" + altId
}

// integrationDedupID names one integration event for the broker's duplicate window (its
// Nats-Msg-Id): tenant, resolved device and the sender's AltId.
//
// 🔴 THE DEVICE IS PART OF THE IDENTITY. An AltId is the sender's own key and carries only the
// codec's prefix, not the alias scheme the sender-local identifier was resolved under, so two
// integrations on different schemes can send the same identifier, body and time and reach
// DIFFERENT devices. Keyed on tenant:AltId alone the broker stored the first device's event and
// silently dropped the second's while both requests answered 202. The same device's retransmission
// still produces the same id, which is the dedup this exists for.
//
// The device and AltId are hashed rather than concatenated: a device token and an AltId (up to
// integrationcodec.MaxAltIdLen bytes) together would make a long id held for the whole window in every
// replica, and a hash is a fixed 43 bytes whatever either holds. 256 bits, so a collision is not a
// concern. The tenant stays in the clear, as on every other inbound id.
func integrationDedupID(tenant, deviceToken, altId string) string {
	sum := sha256.Sum256([]byte(integrationDedupKey(deviceToken, altId)))
	return tenant + ":" + base64.RawURLEncoding.EncodeToString(sum[:])
}

// ClearIntegrationMark removes the integration markers from an event. The device-facing decode
// path calls it on every event it publishes, so no decoder, present or future, can hand the
// resolver an event that claims an integration's authority: only handleIntegration sets them.
func ClearIntegrationMark(event *model.UnresolvedEvent) {
	event.Integration = ""
	event.SealedIntegrationToken = nil
	event.IntegrationIdentifier = ""
}
