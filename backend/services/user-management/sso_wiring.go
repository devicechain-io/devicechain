// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"

	mscfg "github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/credential"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/secrets"
	"github.com/devicechain-io/dc-user-management/identity"
)

// newIdentityManager builds the identity Manager AND gives it the HTTP client every
// external-identity-provider request goes through.
//
// It is a function of its own so the test that proves a private discovery URL is refused
// builds the Manager exactly as the service does, rather than a copy of it. That is the
// point: the egress guard lives in the client's transport, so the property "the service
// cannot be pointed at an internal address" holds only if the client is installed on the
// Manager main actually runs — and nothing else exercises main.go.
//
// An infrastructure configuration with a malformed egress allowance fails here, at startup,
// rather than yielding a guard that silently drops it.
func newIdentityManager(ms *core.Microservice, db *rdb.RdbManager, lock *messaging.DistributedLock,
	store secrets.SecretStore, accessTTL, refreshTTL time.Duration, issuerUrl string,
	bootstrap identity.BootstrapConfig, credentials *credential.Checker,
	infra mscfg.InfrastructureConfiguration) (*identity.Manager, error) {

	m := identity.NewManager(ms, db, lock, store, accessTTL, refreshTTL, issuerUrl, bootstrap, credentials)
	client, err := identity.NewSsoHTTPClient(infra)
	if err != nil {
		return nil, err
	}
	m.SetSsoHTTPClient(client)
	return m, nil
}

// ssoMetrics is the Prometheus side of identity.SsoMetrics.
type ssoMetrics struct {
	logins *prometheus.CounterVec
	fetch  *prometheus.CounterVec
}

// newSsoMetrics registers the single sign-on counters on ms. It is called once, when the
// service wires single sign-on (a collector belongs to the PROCESS; registering twice panics).
func newSsoMetrics(ms *core.Microservice) identity.SsoMetrics {
	return &ssoMetrics{
		logins: ms.NewCounterVec("sso_logins_total",
			"Single sign-on attempts by connection and outcome. outcome=\"success\" is a completed "+
				"sign-in; every refusal has an outcome of its own (refused_*, state_invalid, idp_error). "+
				"outcome=\"state_store_full\" means the sign-in state bucket was full and beginSso failed "+
				"closed. refused_unverified_email, refused_unverified_domain, refused_required_claim and "+
				"refused_system_role_identity are SECURITY events: an identity provider vouched for someone "+
				"it should not have been able to. The connection label is a connection token, or "+
				"\"unknown\" when the request named none.",
			[]string{"connection", "outcome"}),
		fetch: ms.NewCounterVec("sso_idp_fetch_failures_total",
			"Requests to an identity provider's token endpoint, key set or discovery document that "+
				"failed to complete, by connection.",
			[]string{"connection"}),
	}
}

func (m *ssoMetrics) Login(connection, outcome string) {
	m.logins.WithLabelValues(connection, outcome).Inc()
}

func (m *ssoMetrics) IdpFetchFailure(connection string) {
	m.fetch.WithLabelValues(connection).Inc()
}
