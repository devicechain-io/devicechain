// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package registry

import (
	"strconv"
	"strings"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/mux"
	"github.com/rs/zerolog/log"

	"github.com/devicechain-io/dc-lwm2m-ingest/config"
)

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
}

// NewHandlers builds the registration handlers over a Registry and the identity->binding
// map (config.Bindings()). The binding map is authoritative for tenancy: an authenticated
// identity absent from it is refused (fail closed), though by construction every
// authenticated identity has a binding (both are built from the same config identities).
func NewHandlers(reg *Registry, bindings map[string]config.PskBinding, metrics Metrics) *Handlers {
	return &Handlers{reg: reg, bindings: bindings, metrics: metrics}
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
	if r.Code() != codes.POST {
		h.respond(w, codes.MethodNotAllowed)
		return
	}
	identity, binding, ok := h.authorize(w, r)
	if !ok {
		return
	}
	q := parseQueries(r)
	// `ep` is present and logged for operability, but is NEVER used for tenancy or device
	// identity — that comes only from the authenticated credential's binding (D1).
	log.Debug().Str("ep", q["ep"]).Str("externalId", binding.ExternalId).Str("tenant", binding.Tenant).
		Msg("LwM2M Register received (ep is logged, not trusted for tenancy).")

	result, regId := h.reg.Register(r.Context(), identity, binding, parseLifetime(q))
	switch result {
	case RegisterOK:
		h.respondCreated(w, regId)
	case RegisterUnknownDevice:
		h.respond(w, codes.Forbidden) // 4.03 — device not provisioned, this credential does not auto-register
	default: // RegisterUnavailable
		h.respond(w, codes.ServiceUnavailable) // 5.03 — retryable; the device re-Registers
	}
}

// handleRdItem serves POST /rd/{regId} (Update) and DELETE /rd/{regId} (Deregister).
func (h *Handlers) handleRdItem(w mux.ResponseWriter, r *mux.Message) {
	identity, _, ok := h.authorize(w, r)
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
		if h.reg.Update(identity, regId, parseLifetime(parseQueries(r))) == UpdateOK {
			h.respond(w, codes.Changed) // 2.04
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
