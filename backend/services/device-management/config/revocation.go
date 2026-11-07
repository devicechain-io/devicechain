// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"fmt"
	"time"
)

// Bounds of the X.509 revocation store (deviceManagement.callout.revocation). Every one is a
// fixed-configuration key: read from the mounted file when the pod starts, never from the
// database, and refused outside its range rather than clamped.
const (
	DefaultRevocationMaxCrlBytes   = 2 << 20
	MinRevocationMaxCrlBytes       = 64 << 10
	MaxRevocationMaxCrlBytes       = 32 << 20
	DefaultRevocationMaxCrlEntries = 100_000
	MaxRevocationMaxCrlEntries     = 2_000_000

	DefaultRevocationMaxListsPerAnchor = 16
	MaxRevocationMaxListsPerAnchor     = 64

	DefaultRevocationMaxManualEntriesPerTenant = 100_000
	MaxRevocationMaxManualEntriesPerTenant     = 1_000_000

	DefaultRevocationStaleGrace = 24 * time.Hour
	MaxRevocationStaleGrace     = 7 * 24 * time.Hour

	// DefaultRevocationResweepAfter is how long after its first sweep a revocation's session kick is
	// repeated. The floor (the callout lookup timeout plus the broker's authorization timeout) is
	// checked where the callout is wired, against the timeout in force; the ceiling is here.
	DefaultRevocationResweepAfter = 10 * time.Second
	MaxRevocationResweepAfter     = 5 * time.Minute

	// DefaultRevocationMaxGraphqlCrlBytes is also the ceiling: a PEM of 2 MiB of DER is about
	// 2.8 MiB, which fits inside the 4 MiB GraphQL request body cap with its JSON envelope. A larger
	// list goes over the REST upload.
	DefaultRevocationMaxGraphqlCrlBytes = 2 << 20
	MinRevocationMaxGraphqlCrlBytes     = 64 << 10
	MaxRevocationMaxGraphqlCrlBytes     = 2 << 20

	DefaultRevocationMaxConcurrentWrites = 1
	MaxRevocationMaxConcurrentWrites     = 8
)

// RevocationConfiguration bounds the CRLs an operator may store and how many of them one replica
// writes at once. Unknown keys are refused when the document loads, like every other key.
type RevocationConfiguration struct {
	// MaxCrlBytes bounds one CRL as submitted (PEM or DER), checked before parsing and, on the REST
	// upload, before and during the stream. 64 KiB to 32 MiB, default 2 MiB.
	//
	// 🔴 SIZE IT AGAINST THE POD'S MEMORY LIMIT. Verifying a CRL parses all of it: measured on this
	// build, an upload peaks at roughly 20 to 30 times the CRL's size in heap (a 6.9 MB CRL of
	// 300,000 entries: +190 MiB). The default keeps one upload at 40 to 65 MiB; the limit is per replica and
	// maxConcurrentWrites uploads can overlap. A larger CRL needs a larger limit on the pod.
	MaxCrlBytes int
	// MaxCrlEntries bounds the revoked certificates one CRL lists. 1 to 2,000,000, default 100,000.
	MaxCrlEntries int
	// MaxListsPerAnchor bounds the issuers one trust anchor holds a current list for. 1 to 64,
	// default 16.
	MaxListsPerAnchor int
	// MaxManualEntriesPerTenant bounds the manual revocations one tenant holds. 1 to 1,000,000,
	// default 100,000.
	MaxManualEntriesPerTenant int
	// DefaultStaleGrace is how long past nextUpdate a list still counts as current under
	// REQUIRE_CURRENT when an anchor sets no grace of its own. 0 to 168h, default 24h.
	DefaultStaleGrace string
	// MaxGraphqlCrlBytes bounds the CRL the GraphQL mutation accepts, as DER bytes: its PEM text
	// is held to the PEM expansion of this. 64 KiB to 2 MiB, default 2 MiB. Larger lists go over
	// the REST upload, which streams.
	MaxGraphqlCrlBytes int
	// MaxConcurrentWrites bounds the CRL uploads (parse, verify, store) one replica works on at
	// once; the REST upload refuses the next with 503 and Retry-After, a GraphQL mutation waits
	// its turn. 1 to 8, default 1.
	MaxConcurrentWrites int
	// ResweepAfter is how long after the first sweep a certificate revocation's session kick is
	// repeated, as a Go duration, to catch a device that was being admitted at the instant of the
	// revocation. Up to 5m, default 10s. It must also exceed the callout lookup timeout plus the
	// broker's 5 s authorization timeout; device-management refuses to start otherwise.
	ResweepAfter string
}

// ApplyDefaults fills what the document left out. DefaultStaleGrace is a duration, whose
// legitimate value 0 is spelled "0s", so only the empty string means unset.
func (c *RevocationConfiguration) ApplyDefaults() {
	if c.MaxCrlBytes == 0 {
		c.MaxCrlBytes = DefaultRevocationMaxCrlBytes
	}
	if c.MaxCrlEntries == 0 {
		c.MaxCrlEntries = DefaultRevocationMaxCrlEntries
	}
	if c.MaxListsPerAnchor == 0 {
		c.MaxListsPerAnchor = DefaultRevocationMaxListsPerAnchor
	}
	if c.MaxManualEntriesPerTenant == 0 {
		c.MaxManualEntriesPerTenant = DefaultRevocationMaxManualEntriesPerTenant
	}
	if c.DefaultStaleGrace == "" {
		c.DefaultStaleGrace = DefaultRevocationStaleGrace.String()
	}
	if c.MaxGraphqlCrlBytes == 0 {
		c.MaxGraphqlCrlBytes = DefaultRevocationMaxGraphqlCrlBytes
	}
	if c.MaxConcurrentWrites == 0 {
		c.MaxConcurrentWrites = DefaultRevocationMaxConcurrentWrites
	}
	if c.ResweepAfter == "" {
		c.ResweepAfter = DefaultRevocationResweepAfter.String()
	}
}

// ResweepAfterDuration is the setting parsed. Validate has already refused one that is not a
// duration.
func (c RevocationConfiguration) ResweepAfterDuration() time.Duration {
	d, _ := time.ParseDuration(c.ResweepAfter)
	return d
}

// DefaultStaleGraceDuration is the setting parsed. Validate has already refused one that is not a
// duration.
func (c RevocationConfiguration) DefaultStaleGraceDuration() time.Duration {
	d, _ := time.ParseDuration(c.DefaultStaleGrace)
	return d
}

// Validate refuses a bound outside its range. Nothing is clamped.
func (c RevocationConfiguration) Validate() error {
	if c.MaxCrlBytes < MinRevocationMaxCrlBytes || c.MaxCrlBytes > MaxRevocationMaxCrlBytes {
		return fmt.Errorf("callout.revocation.maxCrlBytes must be between %d and %d, got %d",
			MinRevocationMaxCrlBytes, MaxRevocationMaxCrlBytes, c.MaxCrlBytes)
	}
	if c.MaxCrlEntries < 1 || c.MaxCrlEntries > MaxRevocationMaxCrlEntries {
		return fmt.Errorf("callout.revocation.maxCrlEntries must be between 1 and %d, got %d",
			MaxRevocationMaxCrlEntries, c.MaxCrlEntries)
	}
	if c.MaxListsPerAnchor < 1 || c.MaxListsPerAnchor > MaxRevocationMaxListsPerAnchor {
		return fmt.Errorf("callout.revocation.maxListsPerAnchor must be between 1 and %d, got %d",
			MaxRevocationMaxListsPerAnchor, c.MaxListsPerAnchor)
	}
	if c.MaxManualEntriesPerTenant < 1 || c.MaxManualEntriesPerTenant > MaxRevocationMaxManualEntriesPerTenant {
		return fmt.Errorf("callout.revocation.maxManualEntriesPerTenant must be between 1 and %d, got %d",
			MaxRevocationMaxManualEntriesPerTenant, c.MaxManualEntriesPerTenant)
	}
	grace, err := time.ParseDuration(c.DefaultStaleGrace)
	if err != nil || grace < 0 || grace > MaxRevocationStaleGrace {
		return fmt.Errorf("callout.revocation.defaultStaleGrace must be a duration between 0s and %s, got %q",
			MaxRevocationStaleGrace, c.DefaultStaleGrace)
	}
	if c.MaxGraphqlCrlBytes < MinRevocationMaxGraphqlCrlBytes || c.MaxGraphqlCrlBytes > MaxRevocationMaxGraphqlCrlBytes {
		return fmt.Errorf("callout.revocation.maxGraphqlCrlBytes must be between %d and %d, got %d",
			MinRevocationMaxGraphqlCrlBytes, MaxRevocationMaxGraphqlCrlBytes, c.MaxGraphqlCrlBytes)
	}
	if c.MaxConcurrentWrites < 1 || c.MaxConcurrentWrites > MaxRevocationMaxConcurrentWrites {
		return fmt.Errorf("callout.revocation.maxConcurrentWrites must be between 1 and %d, got %d",
			MaxRevocationMaxConcurrentWrites, c.MaxConcurrentWrites)
	}
	if d, err := time.ParseDuration(c.ResweepAfter); err != nil || d <= 0 || d > MaxRevocationResweepAfter {
		return fmt.Errorf("callout.revocation.resweepAfter must be a duration above 0s and at most %s, got %q",
			MaxRevocationResweepAfter, c.ResweepAfter)
	}
	if c.MaxGraphqlCrlBytes > c.MaxCrlBytes {
		return fmt.Errorf("callout.revocation.maxGraphqlCrlBytes (%d) cannot exceed maxCrlBytes (%d)",
			c.MaxGraphqlCrlBytes, c.MaxCrlBytes)
	}
	return nil
}
