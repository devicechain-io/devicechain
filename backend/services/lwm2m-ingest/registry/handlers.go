// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package registry

import (
	"context"
	"strconv"
	"strings"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/mux"
	"github.com/rs/zerolog/log"

	"github.com/devicechain-io/dc-event-sources/adapter"
	"github.com/devicechain-io/dc-lwm2m-ingest/config"
	"github.com/devicechain-io/dc-lwm2m-ingest/decode"
	"github.com/devicechain-io/dc-lwm2m-ingest/observe"
)

// observer is the telemetry-lifecycle seam the handler drives after a Register/Update
// (*observe.Manager satisfies it). It is optional: a nil observer (an inert deployment or a
// presence-only test) simply skips establishing observations. Cancel-on-session-end is wired
// separately through the Registry's OnSessionEnd hook, not the handler.
type observer interface {
	Establish(identity string, epoch uint64, conn mux.Conn, target observe.Target, paths []string) bool
	Reestablish(identity string, conn mux.Conn, target observe.Target)
}

// messageLimiter is the narrow slice of *adapter.IngestLimiter the /rd path uses to gate a
// device message against its tenant's ADR-023 ingest ceiling (ADR-075 L2c). A Register or an
// Update is a device message that does real work — mint an epoch + durably emit presence, and
// spawn up to MaxObservationsPerRegistration downlink Observe exchanges — so an authenticated
// device could otherwise flood durable writes and goroutines past the ingest ceiling the Notify
// path enforces. A nil limiter (a presence-only test/inert deployment) skips gating.
type messageLimiter interface {
	AllowMessage(tenant string) bool
}

// LwM2M registration interface paths (OMA LwM2M 1.x). Register is POST to the well-known
// /rd; Update (POST) and Deregister (DELETE) target the server-assigned location under
// it, which this adapter returns as /rd/{regId}.
const (
	rdPath     = "/rd"
	rdItemPath = "/rd/{regId}"
)

// Handlers is the CoAP mux surface for the /rd registration interface. It recovers the
// authenticated PSK identity from each request's connection (ADR-075 D1), resolves it to
// a tenancy binding, and drives the Registry — mapping each outcome to a CoAP response
// code. Mount it on the transport's router.
type Handlers struct {
	reg      *Registry
	bindings map[string]config.PskBinding
	metrics  Metrics
	obs      observer               // nil ⇒ presence-only (no telemetry observations)
	allow    decode.ObjectAllowlist // which objects in a registration are telemetry to observe
	limit    messageLimiter         // nil ⇒ /rd ungated (presence-only test / inert deployment)
	// leaderCtx is the leadership term's context (ADR-075 L3a): while this replica holds the
	// ownership lease it is non-nil and live; the instant the term is evicted (lease lost) or
	// shut down it is cancelled, before the transport socket is closed. The /rd handlers refuse
	// with 5.03 once it is cancelled so no registration is processed — and no presence minted —
	// by a replica that is no longer the leader (ADR-070; the confused-deputy / split-brain
	// guard for N>1, and the honest self-eviction signal at N=1). A nil leaderCtx means "always
	// serving" — an inert deployment with no lease, or a presence-only test.
	leaderCtx context.Context
}

// NewHandlers builds the registration handlers over a Registry and the identity->binding
// map (config.Bindings()). The binding map is authoritative for tenancy: an authenticated
// identity absent from it is refused (fail closed), though by construction every
// authenticated identity has a binding (both are built from the same config identities). obs
// is the telemetry-lifecycle manager (L2b) and may be nil (presence-only); allow selects which
// registered object instances are observed. limit is the per-tenant ingest gate (L2c) applied to
// Register/Update and may be nil (ungated). leaderCtx is the leadership term's context (L3a): the
// handlers refuse 5.03 once it is cancelled; a nil leaderCtx means always-serving (inert / test).
func NewHandlers(reg *Registry, bindings map[string]config.PskBinding, metrics Metrics, obs observer, allow decode.ObjectAllowlist, limit messageLimiter, leaderCtx context.Context) *Handlers {
	return &Handlers{reg: reg, bindings: bindings, metrics: metrics, obs: obs, allow: allow, limit: limit, leaderCtx: leaderCtx}
}

// notLeading reports whether this replica has stopped being the serving leader — its leadership
// term context has been cancelled (eviction or shutdown). A nil leaderCtx (inert / test) is always
// leading. It is the L3a self-eviction gate: the transport socket is torn down right after the term
// context is cancelled, but any request already in the accept path in that window must not mint
// presence for a non-leader.
func (h *Handlers) notLeading() bool {
	return h.leaderCtx != nil && h.leaderCtx.Err() != nil
}

// targetFrom builds the telemetry correlation for a registration from its authenticated
// binding — tenancy is bound to the credential (ADR-075 D1), never the device payload.
func targetFrom(b config.PskBinding) observe.Target {
	return observe.Target{
		Tenant:     b.Tenant,
		ExternalId: b.ExternalId,
		Policy: adapter.IngestPolicy{
			Source:          SourceLwM2M,
			DeviceTypeToken: b.DeviceTypeToken,
			AutoRegister:    b.AutoRegister,
		},
	}
}

// Mount registers the /rd and /rd/{regId} handlers on the transport router.
func (h *Handlers) Mount(router *mux.Router) error {
	if err := router.Handle(rdPath, mux.HandlerFunc(h.handleRd)); err != nil {
		return err
	}
	return router.Handle(rdItemPath, mux.HandlerFunc(h.handleRdItem))
}

// handleRd serves POST /rd — Register. Any other method is 4.05.
func (h *Handlers) handleRd(w mux.ResponseWriter, r *mux.Message) {
	if h.notLeading() {
		h.respond(w, codes.ServiceUnavailable) // 5.03 — this replica is no longer the leader; the device retries
		return
	}
	if r.Code() != codes.POST {
		h.respond(w, codes.MethodNotAllowed)
		return
	}
	identity, binding, ok := h.authorize(w, r)
	if !ok {
		return
	}
	// STAGE 1 ingest gate (ADR-075 L2c): a Register is an amplifying device message — it mints a
	// presence epoch, durably emits a StateChange, and spawns downlink Observe exchanges — so it
	// is charged against the tenant's message-rate ceiling BEFORE any of that work. A shed
	// Register answers 4.29 Too Many Requests (RFC 8516); the LwM2M client retries registration,
	// so presence is delayed under a flood, never silently lost.
	if h.limit != nil && !h.limit.AllowMessage(binding.Tenant) {
		h.respond(w, codes.TooManyRequests)
		return
	}
	q := parseQueries(r)
	// `ep` is present and logged for operability, but is NEVER used for tenancy or device
	// identity — that comes only from the authenticated credential's binding (D1).
	log.Debug().Str("ep", q["ep"]).Str("externalId", binding.ExternalId).Str("tenant", binding.Tenant).
		Msg("LwM2M Register received (ep is logged, not trusted for tenancy).")

	// The registration payload is the CoRE-Link object list the observations are selected from
	// (L2b). Read it before responding; an unreadable body yields no telemetry (presence still
	// registers), never a refused registration. Reading it does not affect the response write.
	body, err := r.ReadBody()
	if err != nil {
		body = nil
		log.Debug().Err(err).Str("tenant", binding.Tenant).Str("externalId", binding.ExternalId).
			Msg("Could not read the LwM2M registration body; registering presence with no observations.")
	}

	result, regId, epoch, establish := h.reg.Register(r.Context(), identity, binding, parseLifetime(q))
	switch result {
	case RegisterOK:
		h.respondCreated(w, regId)
		if h.obs != nil && establish {
			// Select the telemetry object instances and (re)establish observations on THIS conn,
			// off the handler goroutine — go-coap writes the 2.01 only after the handler returns,
			// so a synchronous Observe would GET a client still awaiting its 2.01.
			paths, overflow := decode.Observations(body, h.allow)
			if overflow > 0 {
				incr(h.metrics.ObservationOverflow, overflow)
			}
			conn := w.Conn()
			go h.obs.Establish(identity, epoch, conn, targetFrom(binding), paths)
		}
	case RegisterUnknownDevice:
		h.respond(w, codes.Forbidden) // 4.03 — device not provisioned, this credential does not auto-register
	default: // RegisterUnavailable
		h.respond(w, codes.ServiceUnavailable) // 5.03 — retryable; the device re-Registers
	}
}

// handleRdItem serves POST /rd/{regId} (Update) and DELETE /rd/{regId} (Deregister).
func (h *Handlers) handleRdItem(w mux.ResponseWriter, r *mux.Message) {
	if h.notLeading() {
		h.respond(w, codes.ServiceUnavailable) // 5.03 — this replica is no longer the leader; the device retries
		return
	}
	identity, binding, ok := h.authorize(w, r)
	if !ok {
		return
	}
	regId := ""
	if r.RouteParams != nil {
		regId = r.RouteParams.Vars["regId"]
	}
	if regId == "" {
		h.badRequest(w)
		return
	}
	switch r.Code() {
	case codes.POST:
		// STAGE 1 ingest gate (ADR-075 L2c): an Update does work (heal observations onto the
		// conn) and is a device message, so it is charged like a Register. A Deregister (DELETE
		// below) is NOT gated — it FREES a session's resources, and shedding it would keep a
		// session alive against the device's intent.
		if h.limit != nil && !h.limit.AllowMessage(binding.Tenant) {
			h.respond(w, codes.TooManyRequests)
			return
		}
		if h.reg.Update(identity, regId, parseLifetime(parseQueries(r))) == UpdateOK {
			h.respond(w, codes.Changed) // 2.04
			if h.obs != nil {
				// Heal observations onto THIS conn: a queue-mode sleeper that re-handshaked on a
				// new conn Updates rather than re-Registers, so its observations must follow it
				// (a cheap no-op when they already ride this live conn). Off the handler goroutine.
				conn := w.Conn()
				go h.obs.Reestablish(identity, conn, targetFrom(binding))
			}
			return
		}
		h.respond(w, codes.NotFound) // 4.04 — unknown OR foreign regId (uniform, no existence leak)
	case codes.DELETE:
		if h.reg.Deregister(identity, regId) {
			h.respond(w, codes.Deleted) // 2.02
			return
		}
		h.respond(w, codes.NotFound) // 4.04
	default:
		h.respond(w, codes.MethodNotAllowed)
	}
}

// authorize recovers the authenticated identity and its binding, or fails the request
// closed (4.01) and returns ok=false. A request whose connection carries no recoverable
// identity, or an identity with no binding, has no trustworthy tenancy and must not
// proceed.
func (h *Handlers) authorize(w mux.ResponseWriter, r *mux.Message) (string, config.PskBinding, bool) {
	identity, ok := authIdentity(w.Conn())
	if !ok {
		incr(h.metrics.AuthErrors, 1)
		log.Warn().Msg("Refusing an LwM2M request: could not recover an authenticated PSK identity from the connection.")
		h.respond(w, codes.Unauthorized)
		return "", config.PskBinding{}, false
	}
	binding, ok := h.bindings[identity]
	if !ok {
		// An authenticated identity with no binding should be impossible (the credential
		// and binding maps come from the same config), so this is a fail-closed backstop.
		incr(h.metrics.AuthErrors, 1)
		log.Warn().Str("identity", identity).Msg("Authenticated PSK identity has no tenancy binding; refusing.")
		h.respond(w, codes.Unauthorized)
		return "", config.PskBinding{}, false
	}
	return identity, binding, true
}

// respondCreated answers a Register with 2.01 Created and the registration location as
// Location-Path options (one per path segment): /rd/{regId}. A conformant client stores
// that location and targets its Update/Deregister at it.
func (h *Handlers) respondCreated(w mux.ResponseWriter, regId string) {
	opts := []message.Option{
		{ID: message.LocationPath, Value: []byte("rd")},
		{ID: message.LocationPath, Value: []byte(regId)},
	}
	if err := w.SetResponse(codes.Created, message.TextPlain, nil, opts...); err != nil {
		log.Warn().Err(err).Msg("Failed to write LwM2M 2.01 Created response.")
	}
}

// badRequest answers 4.00 and counts it.
func (h *Handlers) badRequest(w mux.ResponseWriter) {
	incr(h.metrics.BadRequests, 1)
	h.respond(w, codes.BadRequest)
}

// respond writes a bodyless CoAP response with the given code.
func (h *Handlers) respond(w mux.ResponseWriter, code codes.Code) {
	if err := w.SetResponse(code, message.TextPlain, nil); err != nil {
		log.Warn().Err(err).Str("code", code.String()).Msg("Failed to write LwM2M response.")
	}
}

// parseQueries splits a CoAP request's URI-Query options into a key->value map. A bare
// flag (no '=') maps to an empty string. A malformed query option set yields an empty map
// (the caller treats missing keys as absent).
func parseQueries(r *mux.Message) map[string]string {
	out := map[string]string{}
	qs, err := r.Queries()
	if err != nil {
		return out
	}
	for _, q := range qs {
		if k, v, found := strings.Cut(q, "="); found {
			out[k] = v
		} else {
			out[q] = ""
		}
	}
	return out
}

// parseLifetime reads the `lt` query as whole seconds; 0 when absent or unparseable (the
// Registry then applies its default). A negative or junk value is treated as absent
// rather than rejected — the clamp handles the floor.
func parseLifetime(q map[string]string) int {
	v, ok := q["lt"]
	if !ok {
		return 0
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return 0
	}
	return n
}
