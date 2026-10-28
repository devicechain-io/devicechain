// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"sync"

	"github.com/devicechain-io/dc-device-management/provisioning"
	"github.com/devicechain-io/dc-microservice/auth"
	mscfg "github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/governance"
	"github.com/devicechain-io/dc-microservice/svcclient"
	"github.com/rs/zerolog/log"
)

// ProvisioningMetrics are the self-registration endpoint's series, built once on the
// initialize path (buildMetrics).
var ProvisioningMetrics *provisioning.Metrics

// ProvisioningUnresolvedAdmissions counts registrations admitted at the platform default for
// want of a tenant's own ceiling. Like every collector it is built once, on the initialize path.
var ProvisioningUnresolvedAdmissions func(core.CeilingSource)

// ProvisioningTenants is the per-tenant registration ceiling: ONE limiter, charged by the
// self-registration endpoint and by the auth callout's registration of devices by certificate,
// so a tenant's two doors share a single allowance and neither is a way around the other.
// wireProvisioningEndpoint sets it; provisioningTenantLimiter reads it.
var (
	ProvisioningTenants   *core.TenantRateLimiter
	provisioningTenantsMu sync.Mutex
)

// provisioningTenantLimiter is the shared ceiling, built on first use if the endpoint has not
// been wired yet.
func provisioningTenantLimiter(infra mscfg.InfrastructureConfiguration) *core.TenantRateLimiter {
	provisioningTenantsMu.Lock()
	defer provisioningTenantsMu.Unlock()
	if ProvisioningTenants == nil {
		ProvisioningTenants = buildProvisioningTenantLimiter(infra)
	}
	return ProvisioningTenants
}

// buildProvisioningMetrics builds the endpoint's series. buildMetrics calls it, once: a
// collector registered twice on this microservice's registry panics.
func buildProvisioningMetrics() {
	ProvisioningMetrics = provisioning.NewMetrics(Microservice)
	ProvisioningUnresolvedAdmissions = governance.NewUnresolvedAdmissions(Microservice, governance.Provisioning)
}

// wireProvisioningEndpoint mounts POST /provisioning/{tenant}/register on this microservice's
// OWN mux (never http.DefaultServeMux, which no server here serves), assembled from the
// loaded configuration and the service's Api.
//
// 🔴 THE ROUTE IS MOUNTED WHETHER OR NOT THE INSTANCE HAS THE KEYS IT COMPARES UNDER. Without
// them the Api answers ErrProvisioningUnavailable and the endpoint a 503, with a
// rate-limited ERROR in the log. Leaving the route off would answer a 404, which reads as a
// wrong URL and sends a firmware author hunting in the wrong place.
//
// It is a function of its own so that provisioning_wiring_test.go drives exactly what the
// service drives: the mux, the Api, the configured limits and the governance resolver are all
// joined here, and a handler test builds none of them.
func wireProvisioningEndpoint() error {
	infra := Microservice.InstanceConfiguration.Infrastructure
	trusted, err := infra.Http.TrustedProxyPrefixes()
	if err != nil {
		return fmt.Errorf("infrastructure.http.trustedProxies: %w", err)
	}
	if len(trusted) == 0 {
		log.Warn().Msg("infrastructure.http.trustedProxies is empty: per-source provisioning limits will key on " +
			"the ingress, not the device, so every device behind it shares one bucket. Name the ingress controller's " +
			"range to key on the client address.")
	}

	pcfg := Configuration.Provisioning
	tenants := buildProvisioningTenantLimiter(infra)
	provisioningTenantsMu.Lock()
	ProvisioningTenants = tenants
	provisioningTenantsMu.Unlock()
	h, err := provisioning.New(provisioning.Config{
		ReadDeadline:               pcfg.ReadDeadlineDuration(),
		MaxInFlight:                pcfg.MaxInFlight,
		MaxConcurrentRegistrations: pcfg.MaxConcurrentRegistrations,
		SourceBurst:                pcfg.SourceBurst,
		SourceEvery:                pcfg.SourceEveryDuration(),
	}, provisioning.Deps{
		Registrar:      Api,
		InstanceId:     Microservice.InstanceId,
		TenantDeleted:  provisioningTenantGate(infra),
		Tenants:        tenants,
		TrustedProxies: trusted,
		Metrics:        ProvisioningMetrics,
	})
	if err != nil {
		return err
	}
	provisioning.Register(Microservice.Mux(), h)
	log.Info().Str("path", provisioning.Path).Dur("readDeadline", pcfg.ReadDeadlineDuration()).
		Int("maxInFlight", pcfg.MaxInFlight).Int("maxConcurrentRegistrations", pcfg.MaxConcurrentRegistrations).Int("sourceBurst", pcfg.SourceBurst).
		Dur("sourceEvery", pcfg.SourceEveryDuration()).Int("trustedProxies", len(trusted)).
		Msg("Device self-registration endpoint mounted.")
	return nil
}

// provisioningTenantGate builds the lifecycle gate that refuses a tenant being deleted. It is a
// variable only so that the wiring test can stand a gate in and prove the endpoint consults it.
var provisioningTenantGate = func(infra mscfg.InfrastructureConfiguration) func(string) bool {
	return governance.NewTenantLifecycleGate(infra.UserManagement, infra.ServiceAuth.Secret, "device-management", safetyGates)
}

// buildProvisioningTenantLimiter is the per-tenant registration ceiling. With no service
// secret or user-management endpoint every tenant is metered at the platform default, which
// is itself a limit; otherwise a tenant's override, else its tier's, else that default is read
// from user-management's tenantGovernance, failing open to the default and never to
// unlimited. The tenant it is charged for is authenticated, so it always gets an allowance of
// its own.
func buildProvisioningTenantLimiter(infra mscfg.InfrastructureConfiguration) *core.TenantRateLimiter {
	counted := core.WithUnresolvedAdmissions(ProvisioningUnresolvedAdmissions)
	def := governance.DefaultProvisioningLimits()
	if infra.ServiceAuth.Secret == "" || infra.UserManagement.Hostname == "" || infra.UserManagement.Port == 0 {
		log.Warn().Float64("registrationsPerSecond", def.RatePerSecond).Int("burst", def.Burst).
			Msg("Service secret or user-management endpoint not configured — the per-tenant self-registration ceiling is the platform default for every tenant.")
		return core.NewTenantRateLimiter(core.StaticCeiling(def.RatePerSecond, def.Burst), counted)
	}
	client := svcclient.New(infra.UserManagement, infra.ServiceAuth.Secret, "device-management",
		[]string{string(auth.TenantRead)})
	umURL := fmt.Sprintf("http://%s:%d/graphql", infra.UserManagement.Hostname, infra.UserManagement.Port)
	return core.NewTenantRateLimiter(governance.NewProvisioningLimitResolver(client, umURL).Ceiling, counted)
}
