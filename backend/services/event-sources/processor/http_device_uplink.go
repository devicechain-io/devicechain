// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"
	"time"

	"github.com/devicechain-io/dc-event-sources/integration"
	"github.com/devicechain-io/dc-event-sources/model"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/eventtype"
	"github.com/devicechain-io/dc-microservice/integrationcodec"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/rs/zerolog/log"
)

// A DEVICE'S OWN PAYLOAD, DECODED.
//
//	POST /{instanceId}/{tenant}/devices/{token}/uplink
//
// A device that publishes a binary or vendor-JSON payload of its own, rather than the DeviceChain
// JSON envelope, posts it here. It is the device's connection, not an integration's: the device
// presents its OWN credential (headers, since a binary body has no room for one), device-management
// authenticates it as it does for /events, and the decoder on the device type's profile reads the
// bytes. The decode, its budgets, its health and its samples are the ones the integration route
// uses; this route only carries the bytes there.
//
// 🔴 THE WIRE IS THE OPT-IN. Nothing on /events changes, and nothing here is selected by what a body
// looks like or by a decoder being attached: a device uses the decoder by addressing this route. A
// device type with no decoder is answered 422, never accepted and never flattened.
//
// The order is the design: the tenant's message gate, then the body, then the plan, then the reading
// gate, then the shared backpressure gate, then the publish. The plan is advisory (the resolver
// decides what runs); it chooses the charge and whether the bytes are carried at all.

// Results of a device-uplink request (the result label of device_uplink_requests_total).
const (
	DeviceUplinkAccepted      = "accepted"
	DeviceUplinkBadRequest    = "bad_request"
	DeviceUplinkUnauthorized  = "unauthorized"
	DeviceUplinkTooLarge      = "too_large"
	DeviceUplinkNoDecoder     = "no_decoder"
	DeviceUplinkRateLimited   = "rate_limited"
	DeviceUplinkPlanUnavail   = "plan_unavailable"
	DeviceUplinkBackpressure  = "backpressure"
	DeviceUplinkPublishFailed = "publish_failed"
	DeviceUplinkInternalError = "internal_error"
)

// DeviceUplinkResults lists every result, so a series can be exported at zero.
var DeviceUplinkResults = []string{
	DeviceUplinkAccepted, DeviceUplinkBadRequest, DeviceUplinkUnauthorized, DeviceUplinkTooLarge, DeviceUplinkNoDecoder,
	DeviceUplinkRateLimited, DeviceUplinkPlanUnavail, DeviceUplinkBackpressure,
	DeviceUplinkPublishFailed, DeviceUplinkInternalError,
}

// The headers a device presents its credential in.
const (
	headerCredentialType   = "X-DeviceChain-Credential-Type"
	headerCredentialID     = "X-DeviceChain-Credential-Id"
	headerCredentialSecret = "X-DeviceChain-Credential-Secret"
	accessTokenType        = "ACCESS_TOKEN"
)

// DeviceUplinkRoute is the device-uplink route's dependencies.
type DeviceUplinkRoute struct {
	// Plans says which devices' types have a payload decoder. Required.
	Plans *integration.DeviceDecoders
	// Publish writes the device's IntegrationUplink to the uplink stream and returns whether it was
	// durably published (processor.ErrEventNotBuilt when the message could not be built).
	Publish func(source, tenant string, event *model.UnresolvedEvent, payload interface{}) error
	// Result counts one request's result. May be nil.
	Result func(result string)
	// Charged counts the readings charged for a carried uplink. May be nil.
	Charged func(n int)
}

func (rt *DeviceUplinkRoute) result(r string) {
	if rt.Result != nil {
		rt.Result(r)
	}
}

// UseDeviceUplinks enables the device-uplink route on this source. Without it the route does not
// exist (404), exactly as before the route did.
func (es *HttpEventSource) UseDeviceUplinks(rt *DeviceUplinkRoute) error {
	if rt == nil || rt.Plans == nil || rt.Publish == nil {
		return errors.New("a device-uplink route needs its plan cache and its publisher")
	}
	es.deviceUplinks = rt
	return nil
}

// presentedHeaderCredential reads the credential a device sent in headers. Bearer is an
// ACCESS_TOKEN; the explicit trio carries any other type. Both at once is ambiguous and refused.
func presentedHeaderCredential(h http.Header) (typ, id, secret *string, err error) {
	var bearer string
	if a := h.Get("Authorization"); a != "" {
		scheme, tok, ok := strings.Cut(a, " ")
		if !ok || !strings.EqualFold(scheme, "Bearer") || strings.TrimSpace(tok) == "" {
			return nil, nil, nil, errors.New("Authorization must be a Bearer token")
		}
		bearer = strings.TrimSpace(tok)
	}
	t, i, s := h.Get(headerCredentialType), h.Get(headerCredentialID), h.Get(headerCredentialSecret)
	explicit := t != "" || i != "" || s != ""
	switch {
	case bearer != "" && explicit:
		return nil, nil, nil, errors.New("present either Authorization or the credential headers, not both")
	case bearer != "":
		t := accessTokenType
		return &t, &bearer, nil, nil
	case explicit:
		if t == "" || i == "" {
			return nil, nil, nil, fmt.Errorf("%s and %s are both required", headerCredentialType, headerCredentialID)
		}
		if s != "" {
			return &t, &i, &s, nil
		}
		return &t, &i, nil, nil
	}
	return nil, nil, nil, nil
}

// isJSONContent reports whether a Content-Type names JSON.
func isJSONContent(ct string) bool {
	mt, _, err := mime.ParseMediaType(ct)
	if err != nil {
		return false
	}
	return mt == "application/json" || strings.HasSuffix(mt, "+json")
}

func (es *HttpEventSource) handleDeviceUplink(w http.ResponseWriter, r *http.Request) {
	rt := es.deviceUplinks
	tenant, device := r.PathValue("tenant"), r.PathValue("token")
	if err := core.ValidateToken(tenant); err != nil {
		rt.result(DeviceUplinkBadRequest)
		http.Error(w, fmt.Sprintf("invalid tenant in path: %v", err), http.StatusBadRequest)
		return
	}
	if err := core.ValidateToken(device); err != nil {
		rt.result(DeviceUplinkBadRequest)
		http.Error(w, fmt.Sprintf("invalid device in path: %v", err), http.StatusBadRequest)
		return
	}
	ctype, cid, csecret, err := presentedHeaderCredential(r.Header)
	if err != nil {
		rt.result(DeviceUplinkBadRequest)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// 🔴 THIS WIRE ALWAYS PRESENTS A CREDENTIAL, whatever deviceAuthMode the instance runs. The route
	// selects a decoder and spends the profile's budgets on the device's behalf, so a request that
	// names a device and nothing else must not reach it. device-management still authenticates what
	// is presented (and, with authentication off, ignores it, as it does on /events).
	if ctype == nil {
		rt.result(DeviceUplinkUnauthorized)
		w.Header().Set("WWW-Authenticate", "Bearer")
		http.Error(w, "a device credential is required on this route", http.StatusUnauthorized)
		return
	}

	// The tenant's own message ceiling first, before the body is read, as /events.
	if es.allow != nil && !es.allow(es.Id, tenant, time.Time{}, false, OriginUntrusted) {
		rt.result(DeviceUplinkRateLimited)
		w.Header().Set("Retry-After", "1")
		http.Error(w, "ingest rate limit exceeded for tenant", http.StatusTooManyRequests)
		return
	}

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			rt.result(DeviceUplinkTooLarge)
			http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
			return
		}
		rt.result(DeviceUplinkBadRequest)
		http.Error(w, "unable to read request body", http.StatusBadRequest)
		return
	}
	es.received(es.Id, body)

	look, err := rt.Plans.Lookup(r.Context(), tenant, device)
	if err != nil {
		// Not knowing whether a decoder exists is never "no decoder": the device is told to retry.
		log.Warn().Err(err).Msg("Device decoder lookup failed; refusing the uplink")
		rt.result(DeviceUplinkPlanUnavail)
		w.Header().Set("Retry-After", retryAfterSeconds)
		http.Error(w, "decoder lookup is unavailable; retry later", http.StatusServiceUnavailable)
		return
	}
	plan := look.Plan
	if !plan.Present {
		rt.result(DeviceUplinkNoDecoder)
		http.Error(w, "no payload decoder is attached to this device's type", http.StatusUnprocessableEntity)
		return
	}

	// What the decoder reads decides what is parsed. The JSON is parsed only for a JSON content type
	// and only when the decoder reads it; an unknown input kind is carried in full, as the integration
	// route does.
	wantsJSON := plan.Input == "JSON" || plan.Cost.ParsesJSON || !(plan.Input == "FRAME" || plan.Input == "DECODED")
	up := &integrationcodec.Uplink{
		Frame: body, FPort: integrationcodec.NoLoRaWAN, FCnt: integrationcodec.NoLoRaWAN,
	}
	if wantsJSON && isJSONContent(r.Header.Get("Content-Type")) {
		var obj map[string]any
		dc := json.NewDecoder(bytes.NewReader(body))
		dc.UseNumber()
		if err := dc.Decode(&obj); err != nil || obj == nil {
			rt.result(DeviceUplinkBadRequest)
			es.failed(es.Id, tenant, body, fmt.Errorf("the body is not a JSON object"))
			http.Error(w, "the body is not a JSON object", http.StatusBadRequest)
			return
		}
		up.Decoded = obj
		up.DepthErr = integrationcodec.CheckDepth(obj)
	}
	carried, charge, refusal := buildUplinkCarrier(&integrationcodec.Descriptor{Type: integrationcodec.GenericJSON}, up, plan)
	if refusal != nil {
		code := http.StatusBadRequest
		res := DeviceUplinkBadRequest
		if refusal.uplink == UplinkTooLargeInput {
			code, res = http.StatusRequestEntityTooLarge, DeviceUplinkTooLarge
		}
		rt.result(res)
		es.failed(es.Id, tenant, body, fmt.Errorf("%s: %w", refusal.uplink, refusal.err))
		http.Error(w, refusal.err.Error(), code)
		return
	}

	if !es.readings(es.Id, tenant, time.Time{}, false, OriginUntrusted, charge) {
		rt.result(DeviceUplinkRateLimited)
		w.Header().Set("Retry-After", "1")
		http.Error(w, "ingest rate limit exceeded for tenant (counted in readings)", http.StatusTooManyRequests)
		return
	}
	if rt.Charged != nil {
		rt.Charged(charge)
	}
	if err := es.admit(es.Id); err != nil {
		rt.result(DeviceUplinkBackpressure)
		writeBackpressured(w)
		return
	}

	now := time.Now().UTC()
	event := &model.UnresolvedEvent{
		Device:           device,
		OccurredTime:     now,
		ProcessedTime:    now,
		EventType:        eventtype.IntegrationUplink,
		CredentialType:   ctype,
		CredentialId:     cid,
		CredentialSecret: csecret,
	}
	if err := rt.Publish(es.Id, tenant, event, carried); err != nil {
		switch {
		case errors.Is(err, messaging.ErrStreamBackpressure):
			rt.result(DeviceUplinkBackpressure)
			writeBackpressured(w)
		case errors.Is(err, ErrEventNotBuilt):
			rt.result(DeviceUplinkInternalError)
			http.Error(w, "unable to accept event: internal error", http.StatusInternalServerError)
		default:
			rt.result(DeviceUplinkPublishFailed)
			http.Error(w, "unable to accept event: downstream unavailable", http.StatusServiceUnavailable)
		}
		return
	}
	rt.result(DeviceUplinkAccepted)
	w.WriteHeader(http.StatusAccepted)
}
