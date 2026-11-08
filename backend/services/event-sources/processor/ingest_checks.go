// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/devicechain-io/dc-event-sources/credcheck"
	"github.com/devicechain-io/dc-event-sources/model"
	"github.com/rs/zerolog/log"
)

// EventIdHeader is the response header a 202 (and a 400 for a refused document) carries: the receipt the platform minted
// for the request. It is a receipt for THIS request, not the stored event's id: that is derived
// from the resolved event and does not exist yet at the door.
const EventIdHeader = "X-DC-Event-Id"

// The reasons /events refuses synchronously, as the refusal counter labels them. They are the
// operator's distinction: the sender is told one thing for both (see writeUnauthorized).
const (
	RefusedUnknownTenant = "unknown_tenant"
	RefusedCredential    = "bad_credential"
)

// TenantWaitBudget is how long the first request for a tenant waits for the lookup it triggers,
// for a TenantUnknown that waits. Only a tenant never seen before waits at all.
const TenantWaitBudget = 300 * time.Millisecond

// CredentialVerifier answers whether the credential in an event's body would authenticate.
type CredentialVerifier interface {
	Verify(ctx context.Context, tenant string, p credcheck.Presented) credcheck.Verdict
}

// IngestChecks are the synchronous refusals HTTP /events makes before it answers 202. Each is
// optional: a nil member makes no such check, and every one of them FAILS OPEN, so a dependency
// that cannot answer leaves the door as it was.
type IngestChecks struct {
	// TenantUnknown reports whether the tenant is KNOWN NOT TO EXIST. It must answer true only on
	// an authoritative "no such tenant"; a tenant it has not learned about, or cannot ask about, is
	// false. It is on the hot path of every request, so a warm answer must cost a lookup and no
	// allocation; only a first sight of a tenant may wait, bounded by TenantWaitBudget.
	TenantUnknown func(ctx context.Context, tenant string) bool
	// Credential answers whether the credential in an event's body would authenticate.
	Credential CredentialVerifier
	// Refused counts a refusal by source and reason (the Refused* constants). May be nil.
	Refused func(source, reason string)
}

// count records a refusal: the counter, and a log line sampled to one a second per reason, since
// a refusal no longer produces a failed-event record (the event never enters the pipeline) and the
// log is where a single device's refusals can still be found. The line names the tenant and the
// device token as the SENDER claimed them (both bounded text), never the credential.
func (c *IngestChecks) count(source, tenant, device, reason string) {
	if c.Refused != nil {
		c.Refused(source, reason)
	}
	if !refusalLog.allow(reason, time.Now()) {
		return
	}
	if len(device) > 64 {
		device = device[:64]
	}
	log.Warn().Str("source", source).Str("tenant", tenant).Str("device", device).Str("reason", reason).
		Msg("Refused an HTTP event with 401 before queueing it (logged at most once a second per reason; see total_http_ingest_refused).")
}

// sampler lets one event through per interval per key.
type sampler struct {
	interval time.Duration
	mu       sync.Mutex
	last     map[string]time.Time
}

func (s *sampler) allow(key string, now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if last, ok := s.last[key]; ok && now.Sub(last) < s.interval {
		return false
	}
	s.last[key] = now
	return true
}

var refusalLog = &sampler{interval: time.Second, last: map[string]time.Time{}}

func (c *IngestChecks) refusedTenant(ctx context.Context, tenant string) bool {
	if c.TenantUnknown == nil {
		return false
	}
	return c.TenantUnknown(ctx, tenant)
}

func (c *IngestChecks) refusedCredential(ctx context.Context, tenant string, event *model.UnresolvedEvent) bool {
	if c.Credential == nil {
		return false
	}
	p := credcheck.Presented{Device: event.Device}
	if event.CredentialType != nil {
		p.Type = *event.CredentialType
	}
	if event.CredentialId != nil {
		p.Id = *event.CredentialId
	}
	if event.CredentialSecret != nil {
		p.Secret = *event.CredentialSecret
	}
	return c.Credential.Verify(ctx, tenant, p) == credcheck.Rejected
}

// UseIngestChecks sets the synchronous refusals /events makes. Call it before the source starts.
func (es *HttpEventSource) UseIngestChecks(checks *IngestChecks) { es.checks = checks }

// newEventId mints the receipt a request is answered with: 128 random bits, hex.
func newEventId() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand does not fail on the platforms this runs on; an empty receipt is still a
		// truthful one (the header is omitted) rather than a made-up value.
		return ""
	}
	return hex.EncodeToString(b[:])
}

// unauthorizedBody is the one body every 401 carries. It names neither the tenant nor the
// credential: an unknown tenant, an unknown credential id, a wrong secret and a missing credential
// the policy requires must be indistinguishable to the sender, or the door answers which tenants
// exist and which usernames are real.
const unauthorizedBody = `{"error":"unauthorized"}` + "\n"

// writeUnauthorized answers 401, identically for every cause.
func writeUnauthorized(w http.ResponseWriter) {
	h := w.Header()
	h.Set("WWW-Authenticate", `DeviceCredential realm="device-ingest"`)
	h.Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	_, _ = w.Write([]byte(unauthorizedBody))
}

// unprocessableBody is the 400 body for a JSON document the platform refuses: what is wrong and
// where. The status is the 400 the endpoint always answered; Message is the sentence the plain-text
// body always carried, and Errors (the JSON Pointer) is the additive part.
type unprocessableBody struct {
	Error   string          `json:"error"`
	Message string          `json:"message"`
	EventId string          `json:"eventId,omitempty"`
	Errors  []DecodeProblem `json:"errors"`
}

// writeUnprocessable answers 400 for a document that is well-formed JSON the platform refuses.
func writeUnprocessable(w http.ResponseWriter, eventId string, err error) {
	h := w.Header()
	if eventId != "" {
		h.Set(EventIdHeader, eventId)
	}
	h.Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusBadRequest)
	_ = json.NewEncoder(w).Encode(unprocessableBody{
		Error:   "unprocessable_event",
		Message: fmt.Sprintf("unable to decode event: %v", err),
		EventId: eventId,
		Errors:  []DecodeProblem{ProblemOf(err)},
	})
}
