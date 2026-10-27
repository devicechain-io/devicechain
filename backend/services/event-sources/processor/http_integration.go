// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
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
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/eventtype"
	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
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
)

// IntegrationUplinkResults lists every uplink result.
var IntegrationUplinkResults = []string{
	UplinkPublished, UplinkUnknownDevice, UplinkUndecoded, UplinkTooManyReadings, UplinkDuplicateSuppress, UplinkShed,
}

// IntegrationTypes are the type label values of integration_requests_total: the four registered
// types, and "unknown" for a request refused before its integration was known.
var IntegrationTypes = []string{
	string(integration.ChirpStackV4), string(integration.TTSV3), string(integration.GenericJSON),
	string(integration.GenericBinary), "unknown",
}

// IntegrationMetrics is how the route reports. Any nil field is skipped. None of them carries a
// tenant or an integration name: both are strings a sender chose before its credential was
// checked.
type IntegrationMetrics struct {
	Request              func(integrationType, result string)
	Uplink               func(result string)
	FlattenSkipped       func(reason string, n int)
	UnregisteredOverflow func()
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
	// Seal seals the bearer token the sender presented for the hop to device-management, bound to
	// the tenant and the integration (credential.IntegrationSealKind).
	Seal func(tenant, integrationToken, bearer string) ([]byte, error)
	// Publish writes one marked event to inbound-events and returns whether it was durably
	// published. It must keep the event's Source: the route has stamped it.
	Publish func(source, tenant string, event *model.UnresolvedEvent, payload interface{}, dedupID string) error
	Metrics IntegrationMetrics
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
	if es.allow != nil && !es.allow(es.Id, tenant, time.Time{}, false, OriginIntegration) {
		m.request(typ, ResultShedAccepted)
		w.WriteHeader(http.StatusAccepted)
		return
	}

	// 4. The body.
	r.Body = http.MaxBytesReader(w, r.Body, integration.MaxBodyBytes)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			m.request(typ, ResultTooLarge)
			http.Error(w, fmt.Sprintf("request body is over the limit of %d bytes", integration.MaxBodyBytes), http.StatusRequestEntityTooLarge)
			return
		}
		m.request(typ, ResultBadEnvelope)
		http.Error(w, "unable to read request body", http.StatusBadRequest)
		return
	}
	es.received(es.Id, body)
	receivedAt := rt.now().UTC()

	result, err := integration.Decode(desc, &integration.Request{
		Body: body, Header: r.Header, Query: r.URL.Query(), ContentType: r.Header.Get("Content-Type"),
		MessageType: messageType, ReceivedAt: receivedAt,
	})
	switch {
	case err == nil:
	case errors.Is(err, integration.ErrUnsupportedEncoding):
		m.request(typ, ResultUnsupportedEncoding)
		http.Error(w, "the body is in an encoding this platform does not read: set the sender's integration "+
			"encoding to JSON (ChirpStack: the HTTP integration's \"JSON\" encoding)", http.StatusUnsupportedMediaType)
		return
	case errors.Is(err, integration.ErrMalformed):
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
	if result.Ignored {
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
		payload  *model.UnresolvedMeasurementsPayload
		readings int
		// identifier is the sender's name the device was resolved from; the resolver re-checks it.
		identifier string
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

		var link *integration.Link
		if desc.LinkMetrics {
			link = up.Link
		}
		var flat integration.Flattened
		if up.Decoded != nil {
			flat, err = integration.Flatten(up.Decoded, link)
			var tooMany integration.ErrTooManyReadings
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
		dedup := looked.DeviceToken + "\x00" + up.AltId
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
			// Marked as the integration's, and only here: the resolver opens the sealed token.
			o.event.Integration = slug
			o.event.SealedIntegrationToken = sealed
			o.event.IntegrationIdentifier = o.identifier
			if err := rt.Publish(es.Id, tenant, o.event, o.payload, tenant+":"+*o.event.AltId); err != nil {
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

// ClearIntegrationMark removes the integration markers from an event. The device-facing decode
// path calls it on every event it publishes, so no decoder, present or future, can hand the
// resolver an event that claims an integration's authority: only handleIntegration sets them.
func ClearIntegrationMark(event *model.UnresolvedEvent) {
	event.Integration = ""
	event.SealedIntegrationToken = nil
	event.IntegrationIdentifier = ""
}
