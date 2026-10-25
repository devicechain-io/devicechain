// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"fmt"
	"time"

	"github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/rdb"
)

// Defaults for the self-registration endpoint's pre-authentication budget.
const (
	// DefaultProvisioningReadDeadline bounds reading a request's body. A device on a poor link
	// sends a few hundred bytes; ten seconds is generous for that and short for a slow-body
	// attacker, who ties up a socket for at most this long and never holds a slot.
	DefaultProvisioningReadDeadline = 10 * time.Second
	// DefaultProvisioningMaxInFlight is how many requests one replica holds at once after
	// their body has arrived. It bounds memory and goroutines, NOT database connections: a
	// request holds a connection only inside the places below.
	DefaultProvisioningMaxInFlight = 64
	// DefaultProvisioningMaxConcurrentRegistrations is how many of those requests are inside
	// the database work (authentication and registration) at once, and so how many pooled
	// connections the endpoint can hold. The default pool is 20, budgeted as resolution 10 +
	// raise-alarm 6 + 4 for reads and everything else, so the endpoint takes two of the four
	// and the rest queue without a connection. Registrations against one profile serialize on
	// its row anyway, so more places buy little.
	DefaultProvisioningMaxConcurrentRegistrations = 2
	// DefaultProvisioningSourceBurst and DefaultProvisioningSourceEvery are the per-source
	// bucket: sixty requests at once, then one a second. A factory line behind one NAT
	// registers its first sixty units at once and the rest at a steady rate.
	DefaultProvisioningSourceBurst = 60
	DefaultProvisioningSourceEvery = time.Second

	// MaxProvisioningReadDeadline bounds the deadline from above: a deadline measured in
	// minutes is not a bound.
	MaxProvisioningReadDeadline = time.Minute
	// MaxProvisioningMaxInFlight bounds the slots from above. Each in-flight registration
	// holds a pooled connection, so a count past any plausible pool only queues.
	MaxProvisioningMaxInFlight = 1024
)

// ProvisioningConfiguration is the fixed configuration of the self-registration endpoint
// (POST /provisioning/{tenant}/register). Unknown keys are refused when the document loads,
// like every other key, and an out-of-range value stops the service naming the setting.
//
// Durations are written the way Go writes them ("10s", "500ms").
type ProvisioningConfiguration struct {
	// ReadDeadline is how long a request has to deliver its body, from when the handler
	// starts. Default 10s, at most 1m.
	ReadDeadline string
	// MaxInFlight is how many requests one replica holds at once after their body arrived; the
	// next gets a 503 with Retry-After. A request still sending its body holds no slot. It
	// bounds memory, not connections. Default 64.
	MaxInFlight int
	// MaxConcurrentRegistrations is how many requests are inside the database work at once;
	// the rest of the slots queue for a place (at most five seconds, then 503) holding no
	// connection. It must be below the connection pool (rdbConfiguration.maxOpenConnections,
	// 20 unless set), and with resolution.workers and raiseAlarm.workers should leave reads
	// room: a sum over the pool is logged. Default 2.
	MaxConcurrentRegistrations int
	// SourceBurst and SourceEvery are the per-source bucket, in force per replica: a source
	// may make SourceBurst requests at once and then one every SourceEvery. A source is the
	// client address as infrastructure.http.trustedProxies lets the service believe it; with
	// that list empty every device behind the ingress shares one bucket. Defaults 60 and 1s.
	SourceBurst int
	SourceEvery string
}

// ApplyDefaults fills what the document left out.
func (c *ProvisioningConfiguration) ApplyDefaults() {
	if c.ReadDeadline == "" {
		c.ReadDeadline = DefaultProvisioningReadDeadline.String()
	}
	if c.MaxInFlight == 0 {
		c.MaxInFlight = DefaultProvisioningMaxInFlight
	}
	if c.MaxConcurrentRegistrations == 0 {
		c.MaxConcurrentRegistrations = DefaultProvisioningMaxConcurrentRegistrations
	}
	if c.SourceBurst == 0 {
		c.SourceBurst = DefaultProvisioningSourceBurst
	}
	if c.SourceEvery == "" {
		c.SourceEvery = DefaultProvisioningSourceEvery.String()
	}
}

// ReadDeadlineDuration is ReadDeadline parsed. Validate has already refused one that is not.
func (c ProvisioningConfiguration) ReadDeadlineDuration() time.Duration {
	d, _ := time.ParseDuration(c.ReadDeadline)
	return d
}

// SourceEveryDuration is SourceEvery parsed. Validate has already refused one that is not.
func (c ProvisioningConfiguration) SourceEveryDuration() time.Duration {
	d, _ := time.ParseDuration(c.SourceEvery)
	return d
}

// Validate refuses a bound outside its range. Nothing is clamped: a limit that silently read
// as another value would be an endpoint with its budget off and nothing to say so.
func (c ProvisioningConfiguration) Validate(pool config.MicroserviceDatastoreConfiguration) error {
	d, err := time.ParseDuration(c.ReadDeadline)
	switch {
	case err != nil:
		return fmt.Errorf("provisioning.readDeadline %q is not a duration (want a form like 10s): %w", c.ReadDeadline, err)
	case d <= 0 || d > MaxProvisioningReadDeadline:
		return fmt.Errorf("provisioning.readDeadline must be above 0 and at most %s, got %s", MaxProvisioningReadDeadline, d)
	}
	if c.MaxInFlight < 1 || c.MaxInFlight > MaxProvisioningMaxInFlight {
		return fmt.Errorf("provisioning.maxInFlight must be between 1 and %d, got %d", MaxProvisioningMaxInFlight, c.MaxInFlight)
	}
	if err := rdb.CheckWriterCount("provisioning.maxConcurrentRegistrations", c.MaxConcurrentRegistrations, pool); err != nil {
		return err
	}
	if c.SourceBurst < 1 {
		return fmt.Errorf("provisioning.sourceBurst must be at least 1, got %d", c.SourceBurst)
	}
	every, err := time.ParseDuration(c.SourceEvery)
	switch {
	case err != nil:
		return fmt.Errorf("provisioning.sourceEvery %q is not a duration (want a form like 1s): %w", c.SourceEvery, err)
	case every <= 0:
		return fmt.Errorf("provisioning.sourceEvery must be above 0, got %s", every)
	}
	return nil
}
