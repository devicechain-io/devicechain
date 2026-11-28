// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package identity

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/credential"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/rs/zerolog/log"
)

// The area-bound mint (auth.AreaServiceTokenPath). Unlike the shared-secret mint beside it, this one
// is an AUTHORIZATION boundary, and each of its three rules closes one way to widen what an area can
// obtain:
//
//  1. THE SUBJECT IS DERIVED, NEVER ACCEPTED. It is the area whose mounted credential matched. The
//     body is decoded strictly, so a body naming a subject is a 400, not a field quietly ignored.
//  2. THE CEILING IS ENFORCED. Every requested authority must be inside the area's ceiling; the
//     super-authority is refused; an area with no ceiling entry is refused everything. A refusal is
//     a 403 and a counted result, and nothing is issued.
//  3. ONLY AN AREA CREDENTIAL AUTHENTICATES. The shared service secret is not read here at all.
//
// The shared-secret endpoint is untouched by this file.

// Results of an area-bound mint request: a closed set, because each is a metric label.
const (
	AreaMintIssued         = "issued"
	AreaMintOverCeiling    = "over_ceiling"
	AreaMintWildcard       = "wildcard"
	AreaMintBadCredential  = "bad_credential"
	AreaMintInvalidRequest = "invalid_request"
)

// AreaMintResults lists every result, for pre-creating metric series.
var AreaMintResults = []string{AreaMintIssued, AreaMintOverCeiling, AreaMintWildcard, AreaMintBadCredential, AreaMintInvalidRequest}

// AreaMintMetrics counts area-bound mint requests:
//
//	devicechain_<subsystem>_area_mint_requests_total{area, result}
//
// `area` is an area-bound caller's name or "other" (a request whose credential matched nobody), so
// the label set is closed. A nil *AreaMintMetrics records nothing.
type AreaMintMetrics struct {
	requests *prometheus.CounterVec

	logMu     sync.Mutex
	logWindow time.Time
	logCount  int
}

// NewAreaMintMetrics registers the counter on reg (nil leaves it unregistered) and pre-creates every
// series at zero.
func NewAreaMintMetrics(reg prometheus.Registerer, subsystem string) *AreaMintMetrics {
	requests := promauto.With(reg).NewCounterVec(prometheus.CounterOpts{
		Namespace: "devicechain", Subsystem: subsystem, Name: "area_mint_requests_total",
		Help: "Area-bound service-token mint requests, by area (an area-bound caller, or \"other\" when no " +
			"area credential matched) and result (issued, over_ceiling, wildcard, bad_credential, " +
			"invalid_request). Every result but issued is a refusal; over_ceiling, wildcard and " +
			"invalid_request from an area-bound area are requests it is built never to make.",
	}, []string{"area", "result"})
	for _, a := range append(auth.AreaBoundCallerNames(), auth.UnknownCallerLabel) {
		for _, r := range AreaMintResults {
			requests.WithLabelValues(a, r)
		}
	}
	return &AreaMintMetrics{requests: requests}
}

func (m *AreaMintMetrics) counter(area, result string) prometheus.Counter {
	if !auth.IsAreaBoundCaller(area) {
		area = auth.UnknownCallerLabel
	}
	return m.requests.WithLabelValues(area, result)
}

func (m *AreaMintMetrics) record(area, result string) {
	if m == nil {
		return
	}
	m.counter(area, result).Inc()
}

// allowLog bounds refusal logging to ceilingLogBurst lines per ceilingLogWindow: an area that asks
// in a tight loop must not turn its refusals into a log flood.
func (m *AreaMintMetrics) allowLog() bool {
	if m == nil {
		return true
	}
	m.logMu.Lock()
	defer m.logMu.Unlock()
	now := time.Now()
	if now.Sub(m.logWindow) >= ceilingLogWindow {
		m.logWindow, m.logCount = now, 0
	}
	if m.logCount >= ceilingLogBurst {
		return false
	}
	m.logCount++
	return true
}

// AreaServiceTokenHandler builds the area-bound mint endpoint.
//
//   - areas is the area-bound callers this user-management serves (auth.AreaBoundCallerNames).
//   - credential returns one area's configured credential (the mounted file). An error, or a value
//     shorter than auth.MinAreaCredentialLen, means that area has none and can match nothing.
//   - issue signs a service token for the derived subject.
//   - check judges the request against the ceiling (auth.CheckServiceCeiling); only a
//     within-ceiling verdict is issued.
func AreaServiceTokenHandler(
	areas []string,
	credential func(area string) (string, error),
	issue func(subject string, authorities []string) (auth.IssuedToken, error),
	check func(subject string, authorities []string) auth.ServiceCeilingResult,
	metrics *AreaMintMetrics,
) http.Handler {
	return BoundRequests(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		area, configured, err := matchAreaCredential(areas, credential, r.Header.Get(auth.AreaCredentialHeader))
		if configured == 0 {
			http.Error(w, "area-bound service-token minting is not configured", http.StatusServiceUnavailable)
			return
		}
		if err != nil {
			// Two areas share one credential: answering with either would let it act as the other.
			metrics.record(auth.UnknownCallerLabel, AreaMintBadCredential)
			log.Error().Msg("area-bound service-token request refused: the presented credential is configured for more than one area; " +
				"give each area-bound area a credential of its own")
			http.Error(w, "area credentials are not distinct; refusing to choose an area", http.StatusUnauthorized)
			return
		}
		if area == "" {
			metrics.record(auth.UnknownCallerLabel, AreaMintBadCredential)
			http.Error(w, "invalid area credential", http.StatusUnauthorized)
			return
		}

		authorities, err := decodeAreaMintRequest(r.Body)
		if err != nil {
			metrics.record(area, AreaMintInvalidRequest)
			refuseLog(metrics, area, AreaMintInvalidRequest, nil, err.Error())
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		res := check(area, authorities)
		if res.Verdict != auth.CeilingWithin {
			result := AreaMintOverCeiling
			if res.Verdict == auth.CeilingWildcard {
				result = AreaMintWildcard
			}
			metrics.record(area, result)
			refuseLog(metrics, area, result, res.Offending, "")
			http.Error(w, "requested authorities are outside the "+area+" ceiling: "+strings.Join(res.Offending, ","), http.StatusForbidden)
			return
		}

		tok, err := issue(area, authorities)
		if err != nil {
			http.Error(w, "failed to mint service token", http.StatusInternalServerError)
			return
		}
		metrics.record(area, AreaMintIssued)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(auth.ServiceTokenResponse{Token: tok.Token, ExpiresAt: tok.ExpiresAt.Unix()})
	}), maxMintBodyBytes)
}

// matchAreaCredential reads every area's configured credential and hands the compare to the
// credential primitive (credential.MatchSharedCredential): every configured credential compared in
// constant time over SHA-256 digests with no early exit, the length floor on both sides, and a value
// configured for two areas refused rather than resolved to one. A credential that cannot be read is
// that area having none.
func matchAreaCredential(areas []string, read func(string) (string, error), presented string) (string, int, error) {
	configured := make([]credential.ConfiguredCredential, 0, len(areas))
	for _, a := range areas {
		c, err := read(a)
		if err != nil {
			c = ""
		}
		configured = append(configured, credential.ConfiguredCredential{Name: a, Value: c})
	}
	return credential.MatchSharedCredential(presented, configured)
}

var errAreaMintSubject = errors.New("invalid request body: an area-bound mint takes no subject; the subject is the area whose credential matched")

// decodeAreaMintRequest reads the body strictly: one JSON object with exactly the known fields, at
// least one authority, every authority in the vocabulary.
func decodeAreaMintRequest(body io.Reader) ([]string, error) {
	raw, err := io.ReadAll(io.LimitReader(body, maxMintBodyBytes))
	if err != nil {
		return nil, errors.New("invalid request body")
	}
	// Named before the strict decode so the refusal says why: a subject is the one field a caller
	// might think it can choose.
	var probe map[string]json.RawMessage
	if json.Unmarshal(raw, &probe) == nil {
		if _, named := probe["subject"]; named {
			return nil, errAreaMintSubject
		}
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	var req auth.AreaServiceTokenRequest
	if err := dec.Decode(&req); err != nil {
		return nil, errors.New("invalid request body")
	}
	if dec.More() {
		return nil, errors.New("invalid request body: one JSON object expected")
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, errors.New("invalid request body: one JSON object expected")
	}
	if len(req.Authorities) == 0 {
		return nil, errors.New("authorities are required")
	}
	for _, a := range req.Authorities {
		if !auth.ValidAuthority(a) {
			return nil, errors.New("unknown authority: " + clip(a))
		}
	}
	return req.Authorities, nil
}

// refuseLog warns about a refusal from an AUTHENTICATED area (rate limited). It names the area, the
// result and the offending authorities (vocabulary names, already validated); never a credential.
func refuseLog(m *AreaMintMetrics, area, result string, offending []string, reason string) {
	if !m.allowLog() {
		return
	}
	if len(offending) > ceilingLogMaxAuthorities {
		offending = offending[:ceilingLogMaxAuthorities]
	}
	ev := log.Warn().Str("area", area).Str("result", result)
	if len(offending) > 0 {
		ev = ev.Strs("offending", offending)
	}
	if reason != "" {
		ev = ev.Str("reason", reason)
	}
	ev.Msg("area-bound service-token request refused: the area asked for something it may never have")
}
