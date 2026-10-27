// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"
	"time"

	nats "github.com/nats-io/nats.go"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/devicechain-io/dc-microservice/blob"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/credential"
	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/secrets"
	"github.com/devicechain-io/dc-user-management/admin"
	"github.com/devicechain-io/dc-user-management/branding"
	"github.com/devicechain-io/dc-user-management/deadletters"
	"github.com/devicechain-io/dc-user-management/graphql"
	"github.com/devicechain-io/dc-user-management/iam"
	"github.com/devicechain-io/dc-user-management/identity"
	"github.com/devicechain-io/dc-user-management/settings"
	"github.com/devicechain-io/dc-user-management/settingsdefs"
)

// mfaSweepInterval is how often idle second-factor source-failure rows are pruned. They are kept
// a day (iam.MfaSourceFailureRetention), so hourly is far finer than the need.
const mfaSweepInterval = time.Hour

// configureMfa gives the identity manager everything the second factor needs. It is a function of
// its own, called by main, so a test can build the manager the way the service does: without the
// challenge bucket an enrolled person's sign-in FAILS CLOSED, and nothing but this call gives the
// manager one.
func configureMfa(m *identity.Manager, challenges nats.KeyValue, metrics identity.MfaMetrics,
	issuer func(context.Context) string) {
	m.SetMfaStore(challenges)
	m.SetMfaMetrics(metrics)
	m.SetMfaIssuer(issuer)
}

// newAdminService builds the admin Service the way the service serves it on BOTH planes. It is a
// function of its own for the reason newIdentityManager is: the properties that matter (deleting
// an identity with a second factor reaches its sealed seed, which needs the secret store; service
// account secrets are digested under the key) hold only if the Service main runs was handed the
// collaborators, and nothing else exercises main.go.
func newAdminService(store *iam.Store, settle, tokenHold time.Duration, letters *deadletters.Store,
	serviceAccountKey *credential.DeviceSecretKey, secretStore secrets.SecretStore) *admin.Service {
	svc := admin.NewService(store, settle, tokenHold, letters)
	svc.SetViewerBaseline(identity.ViewerAuthorities())
	svc.SetServiceAccountSecretKey(serviceAccountKey)
	svc.SetSecretStore(secretStore)
	return svc
}

// dataPlaneProviders is the map of providers injected into the data plane's GraphQL context.
func dataPlaneProviders(adminSvc *admin.Service, im *identity.Manager, settingsSvc *settings.Service,
	blobStore blob.Store) map[gqlcore.ContextKey]interface{} {
	return map[gqlcore.ContextKey]interface{}{
		graphql.ContextMembersKey:  adminSvc,
		graphql.ContextIdentityKey: im,
		graphql.ContextSettingsKey: settingsSvc,
		graphql.ContextBlobKey:     blobStore,
	}
}

// mfaMetrics is the Prometheus side of identity.MfaMetrics.
type mfaMetrics struct {
	verifications *prometheus.CounterVec
	challenges    prometheus.Counter
	enrollments   *prometheus.CounterVec
	identityLocks prometheus.Counter
}

// The label values the counters carry. Every combination is exported at zero from startup:
// an alert over increase() cannot see a series' first sample, so a series born by the first
// failure would hide exactly that one (the required MfaIdentityLocked alert depends on it).
var (
	mfaMethods     = []string{"totp", "recovery"}
	mfaResults     = []string{"ok", "invalid", "replay", "locked", "expired", "budget", "rate_limited", "unavailable"}
	mfaEnrollments = []string{"begun", "confirmed", "disabled", "reset"}
)

// newMfaMetrics registers the second-factor counters on ms. It is called once (a collector
// belongs to the PROCESS; registering twice panics).
func newMfaMetrics(ms *core.Microservice) identity.MfaMetrics {
	m := &mfaMetrics{
		verifications: ms.NewCounterVec("mfa_verifications_total",
			"Second-factor code checks by method and result. result=\"locked\" is an attempt refused "+
				"unevaluated because the account's authenticator (or the source) is locked; \"replay\" is a "+
				"correct code that was already used; \"budget\" is a request that carried more checks "+
				"than its allowance; \"expired\" is a challenge that could not be used.",
			[]string{"method", "result"}),
		enrollments: ms.NewCounterVec("mfa_enrollments_total",
			"Second-factor enrolment events: begun, confirmed, disabled by the person, reset by an operator.",
			[]string{"result"}),
	}
	m.challenges = ms.NewCounter("mfa_challenges_total",
		"Password sign-ins held for their second factor (a correct password of an enrolled account).")
	m.identityLocks = ms.NewCounter("mfa_identity_locks_total",
		"Attempts that armed an account's identity-wide authenticator lock. Any increase is evidence that "+
			"the account's password is in use by someone failing its second factor.")
	for _, method := range mfaMethods {
		for _, result := range mfaResults {
			m.verifications.WithLabelValues(method, result)
		}
	}
	for _, result := range mfaEnrollments {
		m.enrollments.WithLabelValues(result)
	}
	return m
}

func (m *mfaMetrics) Verification(method, result string) {
	m.verifications.WithLabelValues(method, result).Inc()
}
func (m *mfaMetrics) Challenge()               { m.challenges.Inc() }
func (m *mfaMetrics) Enrollment(result string) { m.enrollments.WithLabelValues(result).Inc() }
func (m *mfaMetrics) IdentityLock()            { m.identityLocks.Inc() }

// mfaIssuerSource names this instance for an authenticator app: the operator's default branding
// title when it has one, else the host of the public issuer URL, else "" (the identity layer then
// falls back to its built-in label). Read when an enrolment begins, so a rename applies to the
// next enrolment.
func mfaIssuerSource(svc func() *settings.Service, issuerURL string) func(context.Context) string {
	return func(ctx context.Context) string {
		if s := svc(); s != nil {
			if def, err := s.Get(ctx, settingsdefs.KeyBrandingDefault); err == nil {
				var b branding.Branding
				if json.Unmarshal(def.Value, &b) == nil && b.Title != nil && strings.TrimSpace(*b.Title) != "" {
					return strings.TrimSpace(*b.Title)
				}
			}
		}
		if u, err := url.Parse(issuerURL); err == nil {
			return u.Hostname()
		}
		return ""
	}
}
