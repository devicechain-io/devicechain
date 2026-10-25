// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package provisioning is the HTTP endpoint of device self-registration:
//
//	POST /provisioning/{tenant}/register
//
// served on device-management's own mux (the API Ingress forwards /api/device-management/...
// with the prefix stripped). A device presents a provision key and secret, its serial number
// and a credential of its own making; the platform answers with the device's token and MQTT
// client id and stores only a digest of the credential. Everything that decides WHAT happens
// to a device lives in model.Api (AuthenticateProvisioningProfile, ProvisionDevice); this
// package owns the transport: what a stranger can make the service spend before it has proved
// anything, and how each refusal is spelled on the wire.
//
// # The order of one request
//
// Each step is there because the one before it must not have cost anything.
//
//  1. Arm a per-request read deadline. The server this mux is served from cannot carry a
//     ReadTimeout (it also carries the GraphQL subscription WebSocket), so the bound is the
//     request's own.
//  2. Check the path tenant's grammar.
//  3. Read and parse the body, at most 8 KiB, and validate every field the body decides.
//     NO SLOT IS HELD YET: a client that trickles its body ties up a goroutine and a socket
//     for at most the deadline, and nothing that bounds real work.
//  4. Take a slot. None free is a 503 with Retry-After.
//  5. Charge the per-source bucket, keyed by the address infrastructure.http.trustedProxies
//     lets us believe. Empty is a 429 with Retry-After. There is no exemption for a source
//     that succeeded before and no global bucket.
//  6. Authenticate the profile through the credential Checker. Every way this fails is the
//     same 401, and an unknown tenant, key or secret pays the same dummy compare.
//  7. Charge the per-tenant bucket (a governance dimension). Only an authenticated tenant
//     gets one, so an invented tenant name costs the limiter nothing.
//  8. Register the device.
//
// A tenant that is being deleted is looked up before the slot and refused AFTER step 5 with a
// dummy compare, so that refusing it is neither cheaper nor faster than a wrong secret.
package provisioning

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-microservice/core"
	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/sourcelimit"
	"github.com/rs/zerolog/log"
)

// Path is the route pattern the handler is registered under.
const Path = "POST /provisioning/{tenant}/register"

// MaxBodyBytes is the request body cap. It is a constant, not configuration: the largest
// legitimate body is a few hundred bytes plus the optional metadata.
const MaxBodyBytes = 8 << 10

// maxFieldLen bounds the provision key and secret as presented. A real key is a token and a
// real secret is 43 characters; anything longer cannot match and is not worth a compare.
const maxFieldLen = 256

// maxSourceKeys bounds the per-source bucket map.
const maxSourceKeys = 100_000

// Registrar is the model half the handler calls. *model.Api is the implementation; the seam
// exists so a test can count what the handler asks of it.
type Registrar interface {
	AuthenticateProvisioningProfile(ctx context.Context, provisionKey, secret string) (*model.ProvisioningProfile, error)
	RefuseProvisioning(ctx context.Context, provisionKey, secret string) error
	ProvisionDevice(ctx context.Context, request *model.ProvisionRequest) (*model.ProvisionResult, error)
}

// TenantLimiter is the per-tenant admission the handler charges after authentication.
// core.TenantRateLimiter is the implementation.
type TenantLimiter interface {
	Allow(tenant string) bool
}

// Config is the fixed (mounted) configuration of the endpoint.
type Config struct {
	// ReadDeadline bounds reading the request body, from the moment the handler starts.
	ReadDeadline time.Duration
	// MaxInFlight is how many registrations one replica works on at once.
	MaxInFlight int
	// SourceBurst and SourceEvery are the per-source bucket: a burst of SourceBurst, then one
	// request every SourceEvery.
	SourceBurst int
	SourceEvery time.Duration
}

// Deps are what the handler is assembled from.
type Deps struct {
	Registrar Registrar
	// InstanceId is part of the MQTT client id a device must connect with.
	InstanceId string
	// TenantDeleted reports a tenant that has been through the delete door. Nil means the
	// lifecycle authority is not configured, as everywhere else.
	TenantDeleted func(tenant string) bool
	// Tenants is the per-tenant bucket. It is required: an endpoint without one would admit
	// without limit once authenticated.
	Tenants TenantLimiter
	// TrustedProxies are the proxies whose X-Forwarded-For is believed; empty believes none.
	TrustedProxies []netip.Prefix
	Metrics        *Metrics
}

// Handler serves the registration endpoint.
type Handler struct {
	cfg     Config
	deps    Deps
	slots   chan struct{}
	sources *sourcelimit.Limiter
	logs    rateLogger
}

// New builds the handler, refusing anything that would leave a layer of the budget off.
func New(cfg Config, deps Deps) (*Handler, error) {
	switch {
	case cfg.ReadDeadline <= 0:
		return nil, errors.New("provisioning: the read deadline must be positive")
	case cfg.MaxInFlight < 1:
		return nil, errors.New("provisioning: maxInFlight must be at least 1")
	case deps.Registrar == nil:
		return nil, errors.New("provisioning: a registrar is required")
	case deps.Tenants == nil:
		return nil, errors.New("provisioning: a per-tenant limiter is required")
	case deps.Metrics == nil:
		return nil, errors.New("provisioning: metrics are required")
	case deps.InstanceId == "":
		return nil, errors.New("provisioning: the instance id is required")
	}
	sources, err := sourcelimit.New(sourcelimit.Limits{Burst: cfg.SourceBurst, Every: cfg.SourceEvery, MaxKeys: maxSourceKeys})
	if err != nil {
		return nil, fmt.Errorf("provisioning: per-source limits: %w", err)
	}
	return &Handler{cfg: cfg, deps: deps, slots: make(chan struct{}, cfg.MaxInFlight), sources: sources}, nil
}

// Register mounts the handler on mux.
func Register(mux *http.ServeMux, h *Handler) { mux.Handle(Path, h) }

// requestBody is the wire request. Unknown fields are refused: a misspelled field is a
// firmware defect that should fail loudly, not register a device without its name.
type requestBody struct {
	ProvisionKey     string          `json:"provisionKey"`
	ProvisionSecret  string          `json:"provisionSecret"`
	ExternalId       string          `json:"externalId"`
	CredentialSecret string          `json:"credentialSecret"`
	Name             *string         `json:"name"`
	Metadata         json.RawMessage `json:"metadata"`
}

// mqttInfo and responseBody are the success body. It holds nothing secret: the device's
// credential is its own material, which the platform never echoes.
type mqttInfo struct {
	ClientId string `json:"clientId"`
}

type responseBody struct {
	Outcome        string   `json:"outcome"`
	Tenant         string   `json:"tenant"`
	DeviceToken    string   `json:"deviceToken"`
	ExternalId     string   `json:"externalId"`
	CredentialType string   `json:"credentialType"`
	Mqtt           mqttInfo `json:"mqtt"`
}

// errorBody is every failure body.
type errorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// RateLimited is the wire code of a 429, which the model does not own.
const RateLimited = "RATE_LIMITED"

// Codes is the closed vocabulary the endpoint serves, pinned by a golden test.
var Codes = []string{
	model.ProvisioningCodeFailed, model.ProvisioningCodeInvalid, model.ProvisioningCodeNotRegistered,
	model.ProvisioningCodeNotEarmarked, model.ProvisioningCodeDeviceDisabled, model.ProvisioningCodeTypeMismatch,
	model.ProvisioningCodeTokenCollision, model.ProvisioningCodeAlreadyProvisioned, model.ProvisioningCodeLimitExceeded,
	RateLimited, model.ProvisioningCodeUnavailable, model.ProvisioningCodeMisconfigured,
	model.ProvisioningCodeCredentialTypeUnsup,
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	result := ResultError
	defer func() { h.deps.Metrics.observe(result, time.Since(start)) }()

	// 1. The read deadline. If it cannot be armed (a wrapper hides the connection) the
	// request is refused: running without the bound this package is built around would be a
	// silent downgrade, and a 503 on every request is loud.
	rc := http.NewResponseController(w)
	if err := rc.SetReadDeadline(start.Add(h.cfg.ReadDeadline)); err != nil {
		h.logs.errorf("deadline", "Provisioning could not arm the request read deadline; refusing the request: %v", err)
		result = h.write(w, ResultUnavailable, http.StatusServiceUnavailable, model.ProvisioningCodeUnavailable,
			model.ErrProvisioningUnavailable.Error(), 0)
		return
	}

	// 2. The tenant's grammar.
	tenant := r.PathValue("tenant")
	if err := core.ValidateToken(tenant); err != nil {
		result = h.write(w, ResultInvalid, http.StatusBadRequest, model.ProvisioningCodeInvalid, "the tenant in the path is not valid", 0)
		return
	}

	// 3. Read and parse the body, and validate it, before any slot.
	body, status, res := h.readBody(w, r)
	if body == nil {
		if res == ResultReadTimeout {
			w.Header().Set("Connection", "close")
		}
		result = h.write(w, res, status, model.ProvisioningCodeInvalid, bodyRefusalMessage(res, status), 0)
		return
	}
	provReq, ferr := h.validate(body)
	if ferr != nil {
		result = h.write(w, ResultInvalid, http.StatusBadRequest, model.ProvisioningCodeInvalid, ferr.Error(), 0)
		return
	}
	// The body is in; the deadline has done its job. Leaving it armed would let the server's
	// background read cancel the request context when it passes, in the middle of a transaction.
	_ = rc.SetReadDeadline(time.Time{})

	deleted := h.deps.TenantDeleted != nil && h.deps.TenantDeleted(tenant)

	// 4. A slot.
	select {
	case h.slots <- struct{}{}:
		h.deps.Metrics.InFlight.Inc()
		defer func() {
			<-h.slots
			h.deps.Metrics.InFlight.Dec()
		}()
	default:
		result = h.write(w, ResultBusy, http.StatusServiceUnavailable, model.ProvisioningCodeUnavailable,
			"the service is busy; retry later", time.Second)
		return
	}

	// 5. The per-source bucket.
	trusted := gqlcore.TrustedSourceAddr(r, h.deps.TrustedProxies)
	if !h.sources.Allow(sourcelimit.SourceKey(trusted)) {
		result = h.write(w, ResultRateLimitedSource, http.StatusTooManyRequests, RateLimited,
			"too many requests from this source; retry later", h.sources.Every())
		return
	}

	// 6. Authenticate.
	ctx := core.WithTenant(r.Context(), tenant)
	var profile *model.ProvisioningProfile
	var err error
	if deleted {
		err = h.deps.Registrar.RefuseProvisioning(ctx, body.ProvisionKey, body.ProvisionSecret)
	} else {
		profile, err = h.deps.Registrar.AuthenticateProvisioningProfile(ctx, body.ProvisionKey, body.ProvisionSecret)
	}
	if err != nil {
		result = h.refuse(w, err)
		return
	}
	if profile == nil {
		// Unreachable: authentication succeeded without a profile. Fail closed.
		result = h.refuse(w, model.ErrProvisioningFailed)
		return
	}

	// 7. The per-tenant bucket.
	if !h.deps.Tenants.Allow(tenant) {
		result = h.write(w, ResultRateLimitedTenant, http.StatusTooManyRequests, RateLimited,
			"too many registrations for this tenant; retry later", time.Second)
		return
	}

	// 8. Register.
	provReq.Profile = profile
	done, err := h.deps.Registrar.ProvisionDevice(ctx, provReq)
	if err != nil {
		result = h.refuse(w, err)
		return
	}
	clientId, cerr := messaging.DeviceClientID(h.deps.InstanceId, done.Tenant, done.DeviceToken)
	if cerr != nil {
		// The device was registered but its client id cannot be spelled. The retry will be a
		// REPLAYED that fails the same way, so say so rather than invent an id.
		h.logs.errorf("clientid", "Provisioning registered a device whose MQTT client id cannot be built: %v", cerr)
		result = h.write(w, ResultError, http.StatusInternalServerError, model.ProvisioningCodeUnavailable,
			"the registration could not be completed", 0)
		return
	}
	status = http.StatusOK
	result = strings.ToLower(string(done.Outcome))
	if done.Outcome == model.ProvisionCreated {
		status = http.StatusCreated
	}
	h.json(w, status, &responseBody{
		Outcome: string(done.Outcome), Tenant: done.Tenant, DeviceToken: done.DeviceToken,
		ExternalId: done.ExternalId, CredentialType: done.CredentialType, Mqtt: mqttInfo{ClientId: clientId},
	})
}

// readBody reads at most MaxBodyBytes and parses them. On failure it returns a nil body, the
// status and the metric result to answer with.
func (h *Handler) readBody(w http.ResponseWriter, r *http.Request) (*requestBody, int, string) {
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxBodyBytes))
	if err != nil {
		var tooBig *http.MaxBytesError
		var nerr net.Error
		switch {
		case errors.As(err, &tooBig):
			return nil, http.StatusRequestEntityTooLarge, ResultInvalid
		case errors.Is(err, os.ErrDeadlineExceeded), errors.As(err, &nerr) && nerr.Timeout():
			return nil, http.StatusRequestTimeout, ResultReadTimeout
		default:
			return nil, http.StatusBadRequest, ResultInvalid
		}
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var body requestBody
	if err := dec.Decode(&body); err != nil {
		return nil, http.StatusBadRequest, ResultInvalid
	}
	if dec.More() {
		return nil, http.StatusBadRequest, ResultInvalid
	}
	return &body, 0, ""
}

func bodyRefusalMessage(result string, status int) string {
	switch {
	case result == ResultReadTimeout:
		return "the request body was not received in time"
	case status == http.StatusRequestEntityTooLarge:
		return fmt.Sprintf("the request body is larger than %d bytes", MaxBodyBytes)
	default:
		return "the request body is not a valid registration request"
	}
}

// validate checks every field the body decides and builds the model request. Nothing here
// echoes a value: the secrets are credentials.
func (h *Handler) validate(b *requestBody) (*model.ProvisionRequest, error) {
	for _, f := range []struct {
		name, value string
	}{{"provisionKey", b.ProvisionKey}, {"provisionSecret", b.ProvisionSecret}} {
		if f.value == "" || len(f.value) > maxFieldLen {
			return nil, fmt.Errorf("%s must be 1 to %d characters", f.name, maxFieldLen)
		}
	}
	req := &model.ProvisionRequest{ExternalId: b.ExternalId, CredentialSecret: b.CredentialSecret, Name: b.Name}
	if len(b.Metadata) > 0 && !bytes.Equal(bytes.TrimSpace(b.Metadata), []byte("null")) {
		trimmed := bytes.TrimSpace(b.Metadata)
		if trimmed[0] != '{' {
			return nil, errors.New("metadata must be a JSON object")
		}
		s := string(trimmed)
		req.Metadata = &s
	}
	if err := model.ValidateProvisionRequest(req); err != nil {
		return nil, err
	}
	return req, nil
}

// refuse answers a model error and returns the metric result it counts under.
func (h *Handler) refuse(w http.ResponseWriter, err error) string {
	var refusal *model.ProvisioningRefusal
	switch {
	case errors.As(err, &refusal):
	case errors.Is(err, rdb.ErrTenantPurged):
		refusal = model.ErrProvisioningFailed
	default:
		h.logs.errorf("internal", "Provisioning failed internally: %v", err)
		return h.write(w, ResultError, http.StatusInternalServerError, model.ProvisioningCodeUnavailable,
			"the registration could not be completed; retry later", 0)
	}
	result := resultForCode(refusal)
	switch result {
	case ResultUnavailable, ResultMisconfigured, ResultUnsupportedType:
		h.logs.errorf(result, "Provisioning refused a registration: %v", refusal)
	}
	return h.write(w, result, refusal.HTTPStatus, refusal.Code, refusal.Error(), 0)
}

// resultForCode maps a refusal to its metric result.
func resultForCode(r *model.ProvisioningRefusal) string {
	switch r.Code {
	case model.ProvisioningCodeFailed:
		return ResultAuthFailed
	case model.ProvisioningCodeInvalid:
		return ResultInvalid
	case model.ProvisioningCodeNotRegistered:
		return ResultNotRegistered
	case model.ProvisioningCodeNotEarmarked:
		return ResultNotEarmarked
	case model.ProvisioningCodeDeviceDisabled:
		return ResultDisabled
	case model.ProvisioningCodeTypeMismatch:
		return ResultTypeMismatch
	case model.ProvisioningCodeTokenCollision:
		return ResultTokenCollision
	case model.ProvisioningCodeAlreadyProvisioned:
		return ResultAlreadyProvisioned
	case model.ProvisioningCodeLimitExceeded:
		return ResultLimitExceeded
	case model.ProvisioningCodeUnavailable:
		return ResultUnavailable
	case model.ProvisioningCodeMisconfigured:
		return ResultMisconfigured
	case model.ProvisioningCodeCredentialTypeUnsup:
		return ResultUnsupportedType
	default:
		return ResultError
	}
}

// write answers a failure and returns result so a call site can assign it in one line.
func (h *Handler) write(w http.ResponseWriter, result string, status int, code, message string, retryAfter time.Duration) string {
	if retryAfter > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(int(math.Max(1, math.Ceil(retryAfter.Seconds())))))
	}
	h.json(w, status, &errorBody{Code: code, Message: message})
	return result
}

func (h *Handler) json(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// rateLogger logs an error at most once a minute per kind, so a misconfigured profile that
// every device of a fleet hits does not become a log flood.
type rateLogger struct {
	mu   sync.Mutex
	last map[string]time.Time
}

func (l *rateLogger) errorf(kind, format string, args ...any) {
	l.mu.Lock()
	if l.last == nil {
		l.last = map[string]time.Time{}
	}
	now := time.Now()
	if prev, ok := l.last[kind]; ok && now.Sub(prev) < time.Minute {
		l.mu.Unlock()
		return
	}
	l.last[kind] = now
	l.mu.Unlock()
	log.Error().Msgf(format, args...)
}
