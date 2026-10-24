// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package governance

import (
	"github.com/devicechain-io/dc-microservice/svcclient"
)

// The platform default for the Provisioning dimension: the per-tenant ceiling on device
// self-registrations that applies to a tenant whose own override and whose tier both say
// nothing, and that stands in for every tenant while user-management cannot be reached.
//
// It is a real, metered ceiling and never "unlimited" (the polarity every dimension
// shares): 20 registrations a second sustained, with room for a burst of 500 — a factory
// line or a power-restored site registering a batch at once, which is the shape this
// traffic takes, rather than a steady stream.
const (
	DefaultProvisioningRegistrationsPerSecond = 20
	DefaultProvisioningBurst                  = 500
)

// DefaultProvisioningLimits is the platform default for the Provisioning dimension, in the
// per-second unit core.TenantRateLimiter meters on.
func DefaultProvisioningLimits() Limits {
	return Limits{
		RatePerSecond: Provisioning.PerSecond(DefaultProvisioningRegistrationsPerSecond),
		Burst:         DefaultProvisioningBurst,
	}
}

// NewProvisioningLimitResolver builds the per-tenant self-registration ceiling resolver
// for device-management: a tenant's override, else its tier's, else the platform default
// (DefaultProvisioningLimits), read from user-management's tenantGovernance over the
// service client. It fails open to that default — metered, never unlimited — when the
// authority is unreachable.
//
// The client's token must carry tenant:read, the scope tenantGovernance is gated on.
func NewProvisioningLimitResolver(client *svcclient.Client, umURL string) *TenantLimitResolver {
	return NewServiceLimitResolver(client, umURL, DefaultProvisioningLimits(), Provisioning)
}
