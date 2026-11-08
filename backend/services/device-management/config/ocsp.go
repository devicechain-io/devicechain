// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"fmt"
	"time"
)

// Bounds of the OCSP revocation check (deviceManagement.callout.ocsp). Fixed configuration, read
// from the mounted file when the pod starts, refused outside its range rather than clamped.
const (
	DefaultOcspFetchDeadline = 400 * time.Millisecond
	// MaxOcspFetchDeadline sits well under the broker's 5 s authorization timeout: OCSP gets a
	// sub-budget of it, never the whole.
	MaxOcspFetchDeadline = 2 * time.Second

	DefaultOcspMaxInFlight = 32
	MaxOcspMaxInFlight     = 256

	DefaultOcspMaxAge = 6 * time.Hour
	MinOcspMaxAge     = 5 * time.Minute
	MaxOcspMaxAge     = 7 * 24 * time.Hour

	DefaultOcspFailureTtl = 30 * time.Second
	MinOcspFailureTtl     = time.Second
	MaxOcspFailureTtl     = 10 * time.Minute

	DefaultOcspCacheEntries = 100_000
	MinOcspCacheEntries     = 1_000
	MaxOcspCacheEntries     = 1_000_000

	DefaultOcspMaxResponderHostsPerTenant = 16
	MaxOcspMaxResponderHostsPerTenant     = 256
)

// OcspConfiguration bounds the OCSP check of a certificate connect. Unknown keys are refused when
// the document loads, like every other key.
type OcspConfiguration struct {
	// Enabled is the instance-wide master switch, default FALSE. While it is off no OCSP request
	// is ever made, whatever a tenant's anchors say: an ENFORCE_KNOWN anchor runs as OFF, a
	// REQUIRE_CURRENT anchor (whose contract is fail-closed) refuses new connects, and the API
	// refuses to switch an anchor to either mode.
	Enabled bool
	// AllowRequireCurrent additionally lets an anchor be switched to REQUIRE_CURRENT, default
	// false. That mode refuses a certificate whose status could not be confirmed, so it is only
	// safe once the cache is kept warm; this build keeps no refresher, and the switch stays off
	// until one ships.
	AllowRequireCurrent bool
	// FetchDeadline is the ONE deadline for every OCSP fetch a connect makes (they run
	// concurrently). 0s up to 2s, default 400ms. 0s selects cache-only: nothing is fetched on the
	// connect path, and an uncached certificate has no answer.
	FetchDeadline string
	// MaxInFlight bounds the fetches one replica has outstanding. A connect never queues for a
	// slot: a full replica is "no usable answer" at once, which each anchor's mode turns into a
	// decision. Each tenant may hold at most max(4, MaxInFlight/4) of them. 1 to 256, default 32.
	MaxInFlight int
	// MaxAge is the oldest an answer's thisUpdate may be, however far its nextUpdate: the replay
	// bound (a request carries no nonce). 5m to 168h, default 6h.
	MaxAge string
	// FailureTtl is how long a responder that failed stays skipped before one probe is admitted.
	// 1s to 10m, default 30s.
	FailureTtl string
	// CacheEntries bounds the good answers one replica holds. 1000 to 1,000,000, default 100,000.
	CacheEntries int
	// MaxResponderHostsPerTenant bounds the distinct responder hosts a tenant's certificates may
	// name (AIA entries; an anchor's own override is not counted). 1 to 256, default 16.
	MaxResponderHostsPerTenant int
}

// ApplyDefaults fills what the document left out. The durations' legitimate values include 0s
// (FetchDeadline), so only the empty string means unset.
func (c *OcspConfiguration) ApplyDefaults() {
	if c.FetchDeadline == "" {
		c.FetchDeadline = DefaultOcspFetchDeadline.String()
	}
	if c.MaxInFlight == 0 {
		c.MaxInFlight = DefaultOcspMaxInFlight
	}
	if c.MaxAge == "" {
		c.MaxAge = DefaultOcspMaxAge.String()
	}
	if c.FailureTtl == "" {
		c.FailureTtl = DefaultOcspFailureTtl.String()
	}
	if c.CacheEntries == 0 {
		c.CacheEntries = DefaultOcspCacheEntries
	}
	if c.MaxResponderHostsPerTenant == 0 {
		c.MaxResponderHostsPerTenant = DefaultOcspMaxResponderHostsPerTenant
	}
}

// FetchDeadlineDuration, MaxAgeDuration and FailureTtlDuration are the settings parsed. Validate
// has already refused one that is not a duration.
func (c OcspConfiguration) FetchDeadlineDuration() time.Duration {
	d, _ := time.ParseDuration(c.FetchDeadline)
	return d
}
func (c OcspConfiguration) MaxAgeDuration() time.Duration {
	d, _ := time.ParseDuration(c.MaxAge)
	return d
}
func (c OcspConfiguration) FailureTtlDuration() time.Duration {
	d, _ := time.ParseDuration(c.FailureTtl)
	return d
}

// Validate refuses a bound outside its range. Nothing is clamped.
func (c OcspConfiguration) Validate() error {
	if d, err := time.ParseDuration(c.FetchDeadline); err != nil || d < 0 || d > MaxOcspFetchDeadline {
		return fmt.Errorf("callout.ocsp.fetchDeadline must be a duration between 0s and %s, got %q", MaxOcspFetchDeadline, c.FetchDeadline)
	}
	if c.MaxInFlight < 1 || c.MaxInFlight > MaxOcspMaxInFlight {
		return fmt.Errorf("callout.ocsp.maxInFlight must be between 1 and %d, got %d", MaxOcspMaxInFlight, c.MaxInFlight)
	}
	if d, err := time.ParseDuration(c.MaxAge); err != nil || d < MinOcspMaxAge || d > MaxOcspMaxAge {
		return fmt.Errorf("callout.ocsp.maxAge must be a duration between %s and %s, got %q", MinOcspMaxAge, MaxOcspMaxAge, c.MaxAge)
	}
	if d, err := time.ParseDuration(c.FailureTtl); err != nil || d < MinOcspFailureTtl || d > MaxOcspFailureTtl {
		return fmt.Errorf("callout.ocsp.failureTtl must be a duration between %s and %s, got %q", MinOcspFailureTtl, MaxOcspFailureTtl, c.FailureTtl)
	}
	if c.CacheEntries < MinOcspCacheEntries || c.CacheEntries > MaxOcspCacheEntries {
		return fmt.Errorf("callout.ocsp.cacheEntries must be between %d and %d, got %d", MinOcspCacheEntries, MaxOcspCacheEntries, c.CacheEntries)
	}
	if c.MaxResponderHostsPerTenant < 1 || c.MaxResponderHostsPerTenant > MaxOcspMaxResponderHostsPerTenant {
		return fmt.Errorf("callout.ocsp.maxResponderHostsPerTenant must be between 1 and %d, got %d",
			MaxOcspMaxResponderHostsPerTenant, c.MaxResponderHostsPerTenant)
	}
	if c.AllowRequireCurrent && !c.Enabled {
		return fmt.Errorf("callout.ocsp.allowRequireCurrent needs callout.ocsp.enabled")
	}
	return nil
}
