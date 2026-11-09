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
// The order is the design: the credential is PROVEN FIRST (device-management authenticates it as it
// resolves the plan), then the tenant's message gate, then the body, then the reading gate, then the
// shared backpressure gate, then the publish. The plan is advisory (the resolver decides what runs);
// it chooses the charge and whether the bytes are carried at all.
//
// 🔴 NOTHING IS SAID BEFORE THE CREDENTIAL IS PROVEN. A caller who does not hold the device's
// credential gets one answer, 401, whether the device exists and whether its type has a decoder: the
// refusals that depend on the device (no decoder, the shape of the body) come only after, so they
// reach only its own holder. The tenant's message gate and the lookup budgets are charged to the
// tenant the credential was proven for, never to the tenant the URL names.
//
// 🔴 THE ROUTE IS OFF UNLESS THE OPERATOR TURNS IT ON (httpIngest.deviceUplinks). It publishes an
// uplink that carries no integration mark, which a device-management from before this route
// refuses and records as a failed event; the operator turns it on once every device-management
// replica has been rolled to a release that reads it, so no request is answered 202 and then lost.

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
	// dedupID names the message for the stream's duplicate window: "" on HTTP, which has no stable id
	// for a request, and the capture sequence on MQTT, so a redelivered capture message is stored once.
	Publish func(source, tenant string, event *model.UnresolvedEvent, payload interface{}, dedupID string) error
	// Result counts one request's result. May be nil.
	Result func(result string)
	// Charged counts the readings charged for a carried uplink. May be nil.
	Charged func(n int)
	// Message counts one raw payload received on a device's MQTT uplink topic, by the same result
	// labels (device_uplink_messages_total). May be nil.
	Message func(result string)
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

// errNotJSONObject is a body that was announced as JSON and is not an object.
var errNotJSONObject = errors.New("the body is not a JSON object")

// carryDeviceBytes builds the carrier for a device's own bytes, for the plan of its type: what the
// decoder reads decides what is parsed. The JSON is parsed only when the caller says the bytes are
// JSON (a JSON content type on HTTP; a leading '{' on MQTT) and only when the decoder reads it; an
// unknown input kind is carried in full, as the integration route does. It is the one place the
// HTTP and MQTT device wires build a carrier, so the two charge and refuse alike.
//
// notJSON is non-nil when the bytes were announced as JSON and are not an object; carrier refusals
// (a frame or object over its cap, a too-deep object) come back as refusal.
func carryDeviceBytes(body []byte, jsonContent bool, plan integration.DecoderPlan) (carried *model.UnresolvedIntegrationUplinkPayload, charge int, refusal *carrierRefusal, notJSON error) {
	wantsJSON := plan.Input == "JSON" || plan.Cost.ParsesJSON || !(plan.Input == "FRAME" || plan.Input == "DECODED")
	up := &integrationcodec.Uplink{
		Frame: body, FPort: integrationcodec.NoLoRaWAN, FCnt: integrationcodec.NoLoRaWAN,
	}
	if wantsJSON && jsonContent {
		var obj map[string]any
		dc := json.NewDecoder(bytes.NewReader(body))
		dc.UseNumber()
		if err := dc.Decode(&obj); err != nil || obj == nil {
			return nil, 0, nil, errNotJSONObject
		}
		up.Decoded = obj
		up.DepthErr = integrationcodec.CheckDepth(obj)
	}
	carried, charge, refusal = buildUplinkCarrier(&integrationcodec.Descriptor{Type: integrationcodec.GenericJSON}, up, plan)
	return carried, charge, refusal, nil
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

	// 🔴 THE CREDENTIAL IS PROVEN BEFORE ANYTHING ELSE IS DECIDED OR SAID, and before the body is read.
	// Device-management authenticates it while it resolves the plan; every way of not being
	// authenticated is this one 401, so an anonymous caller learns nothing about the device or its
	// type. Not knowing the answer is never "unauthenticated" and never "no decoder": the device is
	// told to retry.
	look, err := rt.Plans.Lookup(r.Context(), tenant, device, integration.DeviceCredential{Type: *ctype, ID: *cid, Secret: csecret})
	if err != nil {
		log.Warn().Err(err).Msg("Device decoder lookup failed; refusing the uplink")
		rt.result(DeviceUplinkPlanUnavail)
		w.Header().Set("Retry-After", retryAfterSeconds)
		http.Error(w, "decoder lookup is unavailable; retry later", http.StatusServiceUnavailable)
		return
	}
	if !look.Authenticated {
		rt.result(DeviceUplinkUnauthorized)
		w.Header().Set("WWW-Authenticate", "Bearer")
		http.Error(w, "device authentication failed", http.StatusUnauthorized)
		return
	}

	// The tenant's own message ceiling, charged to the tenant the credential was proven for (the
	// path's tenant is that tenant: device-management authenticated the credential inside it).
	if es.allow != nil && !es.allow(es.Id, tenant, time.Time{}, false, OriginUntrusted) {
		rt.result(DeviceUplinkRateLimited)
		w.Header().Set("Retry-After", "1")
		http.Error(w, "ingest rate limit exceeded for tenant", http.StatusTooManyRequests)
		return
	}

	plan := look.Plan
	if !plan.Present {
		rt.result(DeviceUplinkNoDecoder)
		http.Error(w, "no payload decoder is attached to this device's type", http.StatusUnprocessableEntity)
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

	carried, charge, refusal, notJSON := carryDeviceBytes(body, isJSONContent(r.Header.Get("Content-Type")), plan)
	if notJSON != nil {
		rt.result(DeviceUplinkBadRequest)
		es.failed(es.Id, tenant, body, notJSON)
		http.Error(w, notJSON.Error(), http.StatusBadRequest)
		return
	}
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
	if err := rt.Publish(es.Id, tenant, event, carried, ""); err != nil {
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
