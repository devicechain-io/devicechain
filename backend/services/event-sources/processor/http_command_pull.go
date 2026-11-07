// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/netip"
	"strconv"
	"sync"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/sourcelimit"
	"github.com/rs/zerolog/log"
)

// The HTTP command pull: how a device whose type has the HTTP_PULL command channel receives
// commands. The device holds no connection the platform can publish to, so it asks.
//
//	GET  /{instanceId}/{tenant}/devices/{device}/commands?wait=25
//	POST /{instanceId}/{tenant}/devices/{device}/commands/{command}/ack
//	POST /{instanceId}/{tenant}/devices/{device}/commands/{command}/response
//
// The names, the query parameter and the JSON fields below are the device wire and freeze on
// release. They are on this listener beside POST .../events, and are published to the internet
// only where the operator chooses to publish that listener.
//
// # The exchange
//
// A GET claims the commands the device may be sent and returns them as {"commands":[...]}, or
// 204 when nothing arrived within the wait. The device acknowledges each command BEFORE it acts
// on it (POST .../ack, 204), then reports what it did (POST .../response, 204). An acknowledgement
// that never arrives is the platform's evidence that the pull was lost: the next GET hands the
// command out again, up to the command's maxAttempts, and then the command fails as NOT_DELIVERED.
//
// # Authentication
//
// Authorization: Bearer <the device's access token>, proved against device-management on EVERY
// request (a revoked token stops receiving commands on the next one), and the device named in the
// path must be the device the token belongs to. A token is never accepted in the URL, which lands
// in access logs. A device whose type has not opted in to HTTP_PULL gets 403: nothing here
// changes how its commands are delivered.
//
// # Waiting
//
// wait is the longest a GET holds the request open, in seconds: 0 to 30, 25 by default (under
// the idle timeouts of the usual ingress). A GET with a wait costs one counted poller against its
// tenant's cap, and over the cap is answered 429 with a Retry-After; a GET with wait=0 is never
// held and is not counted. While held, the request asks the platform for the device's commands
// every PollInterval, so any replica can serve any device and no state lives in this process.

// Results of a command-pull request (the result label of http_command_pull_requests_total).
const (
	PullResultCommands     = "commands"
	PullResultEmpty        = "empty"
	PullResultRecorded     = "recorded"
	PullResultUnauthorized = "unauthorized"
	PullResultForbidden    = "forbidden"
	PullResultNotFound     = "not_found"
	PullResultConflict     = "conflict"
	PullResultBadRequest   = "bad_request"
	PullResultSourceLimit  = "source_limited"
	PullResultPollerCap    = "poller_cap"
	PullResultUnavailable  = "unavailable"
)

// CommandPullResults lists every result, so a series can be exported at zero.
var CommandPullResults = []string{
	PullResultCommands, PullResultEmpty, PullResultRecorded, PullResultUnauthorized, PullResultForbidden,
	PullResultNotFound, PullResultConflict, PullResultBadRequest, PullResultSourceLimit, PullResultPollerCap,
	PullResultUnavailable,
}

// CommandPullOps lists the operations a request can be, the op label of the same counter.
var CommandPullOps = []string{"poll", "ack", "response"}

const (
	// DefaultPullWait and MaxPullWait bound how long a GET is held, in seconds.
	DefaultPullWait = 25
	MaxPullWait     = 30
	// DefaultPollInterval is how often a held GET asks the platform for the device's commands.
	DefaultPollInterval = 2 * time.Second
	// maxPullResponseBytes caps a device's response body.
	maxPullResponseBytes = 128 << 10
	// pullBatch is how many commands one GET hands out at most.
	pullBatch = 16
	// maxBearerDevicePath bounds the path segments, which are checked against the token grammar.
)

// PulledCommand is one command handed to a device.
type PulledCommand struct {
	Token        string
	Name         string
	Payload      *string
	DeliveryMode *string
	Attempt      int
	ExpiresAt    *string
}

// PullAnswer is command-delivery's answer to an acknowledgement or a response.
type PullAnswer string

const (
	AnswerRecorded    PullAnswer = "RECORDED"
	AnswerNotFound    PullAnswer = "NOT_FOUND"
	AnswerNotPulled   PullAnswer = "NOT_PULLED"
	AnswerNotAwaiting PullAnswer = "NOT_AWAITING"
	AnswerNotAnswered PullAnswer = "NOT_ANSWERABLE"
)

// PullDevice is what authenticating an access token yields.
type PullDevice struct {
	Token string
	// OptedIn is true when the device's type has the HTTP_PULL command channel.
	OptedIn bool
}

// CommandPullBackend is the platform side of the exchange: device-management proves the token and
// command-delivery owns the commands. Every method fails closed: an error says nothing about the
// device's credential and must not be read as a refusal or as permission.
type CommandPullBackend interface {
	// Authenticate proves an access token for a tenant. A nil device with a nil error is the
	// one answer to every way a token can fail.
	Authenticate(ctx context.Context, tenant, accessToken string) (*PullDevice, error)
	Pull(ctx context.Context, tenant, device string, limit int) ([]PulledCommand, error)
	Ack(ctx context.Context, tenant, device, command string) (PullAnswer, error)
	Respond(ctx context.Context, tenant, device, command string, success bool, payload, errMsg *string) (PullAnswer, error)
}

// CommandPullMetrics is how the route reports. Any nil field is skipped. None carries a tenant
// or a device: both are strings a sender chose.
type CommandPullMetrics struct {
	Request func(op, result string)
	// Pollers is called with the change in the number of held GETs (+1 and -1).
	Pollers func(delta int)
}

func (m CommandPullMetrics) request(op, result string) {
	if m.Request != nil {
		m.Request(op, result)
	}
}

// CommandPullRoute is everything the command-pull routes need.
type CommandPullRoute struct {
	Backend CommandPullBackend
	// Sources rations requests by source address; every request asks device-management to prove
	// its token, so a spray of guesses is bounded by address before it is bounded by anything
	// else. Required.
	Sources        *sourcelimit.Limiter
	TrustedProxies []netip.Prefix
	// MaxPollersPerTenant caps the GETs one tenant may hold open at once on this replica.
	MaxPollersPerTenant int
	// PollInterval is how often a held GET asks for commands; zero takes DefaultPollInterval.
	PollInterval time.Duration
	Metrics      CommandPullMetrics

	mu      sync.Mutex
	pollers map[string]int
	stop    chan struct{}
}

// Validate refuses a route built without a part of it.
func (rt *CommandPullRoute) Validate() error {
	switch {
	case rt == nil:
		return errors.New("no command pull route")
	case rt.Backend == nil:
		return errors.New("a command pull route needs a backend")
	case rt.Sources == nil:
		return errors.New("a command pull route needs a per-source limiter")
	case rt.MaxPollersPerTenant < 1:
		return errors.New("a command pull route needs a positive poller cap per tenant")
	}
	return nil
}

func (rt *CommandPullRoute) interval() time.Duration {
	if rt.PollInterval > 0 {
		return rt.PollInterval
	}
	return DefaultPollInterval
}

// begin opens the route for a start of the source; end releases every held GET so a stop does not
// wait out the longest wait.
func (rt *CommandPullRoute) begin() {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	rt.stop = make(chan struct{})
}

func (rt *CommandPullRoute) end() {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if rt.stop != nil {
		close(rt.stop)
		rt.stop = nil
	}
}

func (rt *CommandPullRoute) stopped() <-chan struct{} {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if rt.stop == nil {
		// Never started, or already stopped: a closed channel releases a GET at once.
		c := make(chan struct{})
		close(c)
		return c
	}
	return rt.stop
}

// acquire counts a held GET against the tenant's cap, reporting whether there was room.
func (rt *CommandPullRoute) acquire(tenant string) bool {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if rt.pollers == nil {
		rt.pollers = make(map[string]int)
	}
	if rt.pollers[tenant] >= rt.MaxPollersPerTenant {
		return false
	}
	rt.pollers[tenant]++
	if rt.Metrics.Pollers != nil {
		rt.Metrics.Pollers(1)
	}
	return true
}

func (rt *CommandPullRoute) release(tenant string) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if rt.pollers[tenant] > 0 {
		rt.pollers[tenant]--
		if rt.pollers[tenant] == 0 {
			delete(rt.pollers, tenant)
		}
	}
	if rt.Metrics.Pollers != nil {
		rt.Metrics.Pollers(-1)
	}
}

// UseCommandPull enables the command-pull routes on this source. Without it they answer 503
// saying the channel is not available, rather than 404, which would read as a wrong URL. It must
// be called before the source starts.
func (es *HttpEventSource) UseCommandPull(rt *CommandPullRoute) error {
	if err := rt.Validate(); err != nil {
		return err
	}
	es.commandPull = rt
	return nil
}

// pullAuth is the part every command-pull request shares: validate the path, charge the source,
// prove the token, check the device and its opt-in. It writes the refusal and returns false when
// the request is not to proceed.
func (es *HttpEventSource) pullAuth(w http.ResponseWriter, r *http.Request, op string) (*CommandPullRoute, string, string, bool) {
	rt := es.commandPull
	if rt == nil {
		http.Error(w, "the command channel is not available on this instance", http.StatusServiceUnavailable)
		return nil, "", "", false
	}
	m := rt.Metrics
	tenant, device := r.PathValue("tenant"), r.PathValue("device")
	if err := core.ValidateToken(tenant); err != nil {
		m.request(op, PullResultBadRequest)
		http.Error(w, fmt.Sprintf("invalid tenant in path: %v", err), http.StatusBadRequest)
		return nil, "", "", false
	}
	if err := core.ValidateToken(device); err != nil {
		m.request(op, PullResultBadRequest)
		http.Error(w, fmt.Sprintf("invalid device in path: %v", err), http.StatusBadRequest)
		return nil, "", "", false
	}
	bearer, ok := bearerOf(r)
	if !ok {
		m.request(op, PullResultUnauthorized)
		w.Header().Set("WWW-Authenticate", "Bearer")
		http.Error(w, "an Authorization: Bearer header carrying the device's access token is required", http.StatusUnauthorized)
		return nil, "", "", false
	}
	src := gqlcore.TrustedSourceAddr(r, rt.TrustedProxies)
	if !rt.Sources.Allow(sourcelimit.SourceKey(src)) {
		m.request(op, PullResultSourceLimit)
		w.Header().Set("Retry-After", strconv.Itoa(max(1, int(rt.Sources.Every().Seconds()+0.999))))
		http.Error(w, "too many requests from this source; retry later", http.StatusTooManyRequests)
		return nil, "", "", false
	}
	if !es.proveDevice(w, r, rt, op, tenant, device, bearer) {
		return nil, "", "", false
	}
	return rt, tenant, device, true
}

// proveDevice proves the bearer token against device-management and checks that it belongs to the
// device in the path and that the device's type is on the HTTP_PULL channel. It writes the refusal
// and returns false when the request is not to proceed.
//
// 🔴 A HELD POLL CALLS THIS AGAIN ON EVERY WAKE, not only at the start: each wake can claim
// commands, a claim is an actuation, and a token revoked, a device disabled or a type moved off
// HTTP_PULL mid-poll must stop the very next one rather than the next request. The cost is one
// authentication per PollInterval per held poll.
func (es *HttpEventSource) proveDevice(w http.ResponseWriter, r *http.Request, rt *CommandPullRoute,
	op, tenant, device, bearer string) bool {
	m := rt.Metrics
	dev, err := rt.Backend.Authenticate(r.Context(), tenant, bearer)
	switch {
	case err != nil:
		log.Warn().Err(err).Msg("Command pull authentication unavailable; refusing the request")
		m.request(op, PullResultUnavailable)
		http.Error(w, "device authentication is unavailable; retry later", http.StatusServiceUnavailable)
		return false
	case dev == nil:
		m.request(op, PullResultUnauthorized)
		w.Header().Set("WWW-Authenticate", "Bearer")
		http.Error(w, "device authentication failed", http.StatusUnauthorized)
		return false
	case dev.Token != device:
		// Authenticated, but as a different device: it may not act for this one.
		m.request(op, PullResultForbidden)
		http.Error(w, "the access token does not belong to the device in the path", http.StatusForbidden)
		return false
	case !dev.OptedIn:
		m.request(op, PullResultForbidden)
		http.Error(w, "this device's type does not use the HTTP_PULL command channel", http.StatusForbidden)
		return false
	}
	return true
}

// pullWire is the JSON a command is served as.
type pullWire struct {
	Token        string          `json:"token"`
	Name         string          `json:"name"`
	Payload      json.RawMessage `json:"payload,omitempty"`
	DeliveryMode string          `json:"deliveryMode,omitempty"`
	Attempt      int             `json:"attempt"`
	ExpiresAt    string          `json:"expiresAt,omitempty"`
}

type pullBody struct {
	Commands []pullWire `json:"commands"`
}

func toWire(cmds []PulledCommand) pullBody {
	out := pullBody{Commands: make([]pullWire, 0, len(cmds))}
	for _, c := range cmds {
		w := pullWire{Token: c.Token, Name: c.Name, Attempt: c.Attempt}
		if c.Payload != nil && json.Valid([]byte(*c.Payload)) {
			w.Payload = json.RawMessage(*c.Payload)
		}
		if c.DeliveryMode != nil {
			w.DeliveryMode = *c.DeliveryMode
		}
		if c.ExpiresAt != nil {
			w.ExpiresAt = *c.ExpiresAt
		}
		out.Commands = append(out.Commands, w)
	}
	return out
}

// handleCommandPoll serves GET .../commands.
func (es *HttpEventSource) handleCommandPoll(w http.ResponseWriter, r *http.Request) {
	rt, tenant, device, ok := es.pullAuth(w, r, "poll")
	if !ok {
		return
	}
	m := rt.Metrics
	wait := DefaultPullWait
	if v := r.URL.Query().Get("wait"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			m.request("poll", PullResultBadRequest)
			http.Error(w, "wait must be a whole number of seconds from 0 to 30", http.StatusBadRequest)
			return
		}
		wait = min(n, MaxPullWait)
	}
	if wait > 0 {
		if !rt.acquire(tenant) {
			m.request("poll", PullResultPollerCap)
			w.Header().Set("Retry-After", "5")
			http.Error(w, "too many commands polls are waiting for this tenant; retry later", http.StatusTooManyRequests)
			return
		}
		defer rt.release(tenant)
	}

	deadline := time.NewTimer(time.Duration(wait) * time.Second)
	defer deadline.Stop()
	stop := rt.stopped()
	bearer, _ := bearerOf(r)
	for first := true; ; first = false {
		if !first && !es.proveDevice(w, r, rt, "poll", tenant, device, bearer) {
			return
		}
		cmds, err := rt.Backend.Pull(r.Context(), tenant, device, pullBatch)
		if err != nil {
			if r.Context().Err() != nil {
				return
			}
			log.Warn().Err(err).Msg("Command pull unavailable; refusing the request")
			m.request("poll", PullResultUnavailable)
			http.Error(w, "commands are unavailable; retry later", http.StatusServiceUnavailable)
			return
		}
		if len(cmds) > 0 {
			m.request("poll", PullResultCommands)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(toWire(cmds))
			return
		}
		if wait == 0 {
			break
		}
		// Jittered, so a fleet of pollers that started together does not ask in step.
		pause := time.NewTimer(rt.interval()/2 + rand.N(rt.interval()))
		select {
		case <-pause.C:
		case <-deadline.C:
			pause.Stop()
			m.request("poll", PullResultEmpty)
			w.WriteHeader(http.StatusNoContent)
			return
		case <-stop:
			pause.Stop()
			m.request("poll", PullResultEmpty)
			w.WriteHeader(http.StatusNoContent)
			return
		case <-r.Context().Done():
			pause.Stop()
			return
		}
	}
	m.request("poll", PullResultEmpty)
	w.WriteHeader(http.StatusNoContent)
}

// writePullAnswer maps command-delivery's answer to a status.
func writePullAnswer(w http.ResponseWriter, m CommandPullMetrics, op string, answer PullAnswer) {
	switch answer {
	case AnswerRecorded:
		m.request(op, PullResultRecorded)
		w.WriteHeader(http.StatusNoContent)
	case AnswerNotFound:
		m.request(op, PullResultNotFound)
		http.Error(w, "no such command for this device", http.StatusNotFound)
	case AnswerNotPulled:
		m.request(op, PullResultConflict)
		http.Error(w, "the command was not handed to this device's pull", http.StatusConflict)
	case AnswerNotAwaiting:
		m.request(op, PullResultConflict)
		http.Error(w, "the command is no longer awaiting an acknowledgement", http.StatusConflict)
	case AnswerNotAnswered:
		m.request(op, PullResultConflict)
		http.Error(w, "the command is not awaiting an answer from this device", http.StatusConflict)
	default:
		// An answer added later is not one this build can act on, and guessing is how a failure
		// becomes a success.
		m.request(op, PullResultUnavailable)
		http.Error(w, "the platform gave an answer this endpoint does not understand", http.StatusBadGateway)
	}
}

// handleCommandAck serves POST .../commands/{command}/ack.
func (es *HttpEventSource) handleCommandAck(w http.ResponseWriter, r *http.Request) {
	rt, tenant, device, ok := es.pullAuth(w, r, "ack")
	if !ok {
		return
	}
	command := r.PathValue("command")
	if err := core.ValidateToken(command); err != nil {
		rt.Metrics.request("ack", PullResultBadRequest)
		http.Error(w, fmt.Sprintf("invalid command in path: %v", err), http.StatusBadRequest)
		return
	}
	answer, err := rt.Backend.Ack(r.Context(), tenant, device, command)
	if err != nil {
		log.Warn().Err(err).Msg("Command acknowledgement unavailable; refusing the request")
		rt.Metrics.request("ack", PullResultUnavailable)
		http.Error(w, "the acknowledgement could not be recorded; retry", http.StatusServiceUnavailable)
		return
	}
	writePullAnswer(w, rt.Metrics, "ack", answer)
}

// responseBody is the JSON a device posts to .../response.
type responseBody struct {
	Success *bool           `json:"success"`
	Payload json.RawMessage `json:"payload,omitempty"`
	Error   *string         `json:"error,omitempty"`
}

// handleCommandResponse serves POST .../commands/{command}/response.
func (es *HttpEventSource) handleCommandResponse(w http.ResponseWriter, r *http.Request) {
	rt, tenant, device, ok := es.pullAuth(w, r, "response")
	if !ok {
		return
	}
	m := rt.Metrics
	command := r.PathValue("command")
	if err := core.ValidateToken(command); err != nil {
		m.request("response", PullResultBadRequest)
		http.Error(w, fmt.Sprintf("invalid command in path: %v", err), http.StatusBadRequest)
		return
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxPullResponseBytes))
	if err != nil {
		m.request("response", PullResultBadRequest)
		http.Error(w, "unable to read request body", http.StatusBadRequest)
		return
	}
	var body responseBody
	if err := json.Unmarshal(raw, &body); err != nil || body.Success == nil {
		m.request("response", PullResultBadRequest)
		http.Error(w, `the body must be JSON with a boolean "success"`, http.StatusBadRequest)
		return
	}
	var payload *string
	if len(body.Payload) > 0 && string(body.Payload) != "null" {
		var asString string
		// A JSON string is the text itself; anything else is carried as its JSON text.
		if json.Unmarshal(body.Payload, &asString) == nil {
			payload = &asString
		} else {
			text := string(body.Payload)
			payload = &text
		}
	}
	answer, err := rt.Backend.Respond(r.Context(), tenant, device, command, *body.Success, payload, body.Error)
	if err != nil {
		log.Warn().Err(err).Msg("Command response unavailable; refusing the request")
		m.request("response", PullResultUnavailable)
		http.Error(w, "the response could not be recorded; retry", http.StatusServiceUnavailable)
		return
	}
	writePullAnswer(w, m, "response", answer)
}
