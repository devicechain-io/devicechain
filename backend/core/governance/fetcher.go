// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package governance

import (
	"context"
	"encoding/json"
	"fmt"
	"math"

	"github.com/devicechain-io/dc-microservice/svcclient"
	"github.com/rs/zerolog/log"
)

// Dimension identifies one per-tenant governance dimension: the pair of
// tenantGovernance fields carrying its rate and burst overrides, plus a short name
// for logs. Its field names are composed into the query string, so they must name
// real schema fields and must never carry caller or tenant input; every dimension
// in the platform is one of the package vars declared below.
type Dimension struct {
	// Name labels the dimension in logs (bounded, never a metric label).
	Name string
	// RateField / BurstField are the tenantGovernance field names holding this
	// dimension's overrides. Both are nullable: null means "inherit the platform
	// default", which is itself a limit, never unlimited.
	RateField  string
	BurstField string
}

// The governance dimensions declared on the iam_tenants control-plane row and
// exposed by user-management's tenantGovernance query. Each is independent: a
// tenant overriding one inherits the platform default for the others.
var (
	// Ingest governs inbound device telemetry admission at event-sources (ADR-023 G.1/G.2).
	Ingest = Dimension{Name: "ingest", RateField: "ingestMessagesPerSecond", BurstField: "ingestBurst"}
	// Outbound governs REACT connector egress, charged at both the source
	// (event-processing) and the sink (outbound-connectors) — ADR-060 SD-3.
	Outbound = Dimension{Name: "outbound", RateField: "outboundMessagesPerSecond", BurstField: "outboundBurst"}
)

// serviceFetcher fetches a tenant's limits for one dimension from
// user-management's data-plane governance query over a service token (ADR-044).
type serviceFetcher struct {
	client *svcclient.Client
	umURL  string
	def    Limits
	dim    Dimension
}

// NewServiceFetcher builds a Fetcher reading dim's overrides from tenantGovernance
// at umURL (user-management's /graphql endpoint), resolving unset overrides to def.
func NewServiceFetcher(client *svcclient.Client, umURL string, def Limits, dim Dimension) Fetcher {
	return &serviceFetcher{client: client, umURL: umURL, def: def, dim: dim}
}

// NewServiceLimitResolver is the one-call wiring every enforcing service wants: a
// resolver for dim backed by user-management, defaulting to def.
func NewServiceLimitResolver(client *svcclient.Client, umURL string, def Limits, dim Dimension) *TenantLimitResolver {
	return NewTenantLimitResolver(NewServiceFetcher(client, umURL, def, dim), def, dim.Name)
}

// Fetch reads the dimension's two override fields and resolves them against the
// platform default, so the returned Limits are always concrete and never
// zero/unlimited.
//
// A null field means the tenant declared no override — inherit the default. A
// NON-POSITIVE field is also floored to the default rather than trusted: the
// GraphQL write path rejects those, so one can only arrive via a direct
// out-of-band DB write, and passing it through would hand core.TenantRateLimiter a
// ceiling that admits nothing (a self-inflicted outage for that tenant). Inherit
// is the safe reading of a value that should not exist; never a live limit of zero.
func (f *serviceFetcher) Fetch(ctx context.Context, tenant string) (Limits, error) {
	// Decoded generically (rather than into a per-dimension struct) so one fetcher
	// serves every dimension. Each field is kept RAW and parsed individually below:
	// decoding straight into a numeric map would make the whole response fail if any
	// selected field were non-numeric, which would silently pin every tenant to the
	// platform default. TenantGovernance carries non-numeric siblings today
	// (aiExternalEnabled is a Boolean), so per-field parsing keeps a dimension that
	// ever selects one from breaking every other read.
	var out struct {
		TenantGovernance map[string]json.RawMessage `json:"tenantGovernance"`
	}
	if err := f.client.Query(ctx, f.umURL, tenant, governanceQuery(f.dim), nil, &out); err != nil {
		return Limits{}, err
	}
	limits, floored := resolveLimits(out.TenantGovernance, f.def, f.dim)
	if len(floored) > 0 {
		// A present-but-unusable override is a fail-safe we must not apply silently:
		// the operator set something that is not a live ceiling, and the tenant is now
		// metered at the platform default. Only reachable via an out-of-band DB write,
		// and bounded to once per refresh (not per call), so it cannot flood.
		log.Warn().Str("tenant", tenant).Str("dimension", f.dim.Name).Strs("fields", floored).
			Msg("Ignoring an unusable per-tenant override; metering at the platform default")
	}
	return limits, nil
}

// governanceQuery composes the tenantGovernance query for one dimension. Both
// field names are package constants, never caller input.
func governanceQuery(dim Dimension) string {
	return fmt.Sprintf(`query { tenantGovernance { %s %s } }`, dim.RateField, dim.BurstField)
}

// resolveLimits folds a decoded tenantGovernance response onto the platform
// default, returning the effective limits and the names of any fields that were
// present but unusable (so the caller can report the fail-safe). Split out pure so
// the rules below are unit-testable without a live user-management (the concrete
// svcclient.Client is not an interface).
//
// An ABSENT or NULL field means the tenant declared no override — inherit the
// default, the overwhelmingly common case, and not worth reporting. A field that is
// present but not a usable positive number is floored to the default AND reported:
// see parseRate/parseBurst for why trusting one would be worse than ignoring it.
func resolveLimits(raw map[string]json.RawMessage, def Limits, dim Dimension) (Limits, []string) {
	limits := def
	var floored []string
	if v, present := raw[dim.RateField]; present && !isNull(v) {
		if rate, ok := parseRate(v); ok {
			limits.MessagesPerSecond = rate
		} else {
			floored = append(floored, dim.RateField)
		}
	}
	if v, present := raw[dim.BurstField]; present && !isNull(v) {
		if burst, ok := parseBurst(v); ok {
			limits.Burst = burst
		} else {
			floored = append(floored, dim.BurstField)
		}
	}
	return limits, floored
}

// isNull reports whether a raw field is JSON null (the tenant declared no override).
func isNull(raw json.RawMessage) bool {
	return len(raw) == 0 || string(raw) == "null"
}

// parseRate accepts only a finite, positive JSON number. A non-positive value would
// hand core.TenantRateLimiter a bucket that admits NOTHING (a self-inflicted outage
// for that tenant) — inherit-the-default is the safe reading of a value the GraphQL
// write path already rejects. A fractional rate is legal: 0.5/s is one call every
// two seconds. Anything else (a bool, a string, +Inf, an out-of-range magnitude) is
// not a ceiling we will meter on.
func parseRate(raw json.RawMessage) (float64, bool) {
	var rate float64
	if err := json.Unmarshal(raw, &rate); err != nil {
		return 0, false
	}
	if rate <= 0 || math.IsInf(rate, 0) || math.IsNaN(rate) {
		return 0, false
	}
	return rate, true
}

// parseBurst accepts only a positive JSON integer. A fractional burst is not an
// integer count and must not silently truncate into a live ceiling, so Unmarshal
// into int64 (which rejects it) is deliberate rather than a float conversion.
func parseBurst(raw json.RawMessage) (int, bool) {
	var burst int64
	if err := json.Unmarshal(raw, &burst); err != nil {
		return 0, false
	}
	if burst <= 0 || burst > math.MaxInt {
		return 0, false
	}
	return int(burst), true
}
