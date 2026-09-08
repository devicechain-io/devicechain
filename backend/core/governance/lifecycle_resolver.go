// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package governance

import (
	"context"
	"fmt"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/svcclient"
	"github.com/rs/zerolog/log"
)

// LifecycleActive is the state of an ordinary live tenant, and the value this package
// serves for a tenant it cannot resolve. It mirrors user-management's iam.PurgeActive
// on the wire; the string is duplicated rather than imported because core cannot depend
// on a service module, and it is the wire contract either way (both sides have tests).
const LifecycleActive = "active"

// TenantLifecycleResolver answers "has this tenant been deleted?" for the data-plane
// services that act on a tenant's behalf with no human in the request. None of them can
// learn it any other way — a device credential and a queued command both outlive the
// operator's delete, and neither carries a lifecycle.
//
// Its consumers are deliberately not listed here. The set spans the device plane
// (connect-auth, the ingest fronts, command dispatch) and outbound egress, and it has
// already grown twice since this was written — a list frozen in a comment only ever drifts
// away from the tree. Ask the tree: `grep -rl NewTenantLifecycleGate backend`.
//
// Two consumers are worth naming for their ASYMMETRY rather than for being on the list.
// On the ingest and command paths, a call this gate admits a moment too late is data that
// stays on our disks until the sweep reaches it. At outbound-connectors and
// notification-management it is a webhook POST, a broker publish or an email that has
// already landed somewhere we do not own — where being late is irreversible, and where the
// refusal is therefore worth more than the availability it costs.
//
// Do not write "the only path that emits" here, or anywhere else, without re-deriving the
// set. It was written once, about outbound-connectors alone, and notification-management
// falsified it — its escalation scheduler re-pages open alarms from its own rows on a
// timer, so it emits with no inbound traffic at all.
//
// It reads purgeState from user-management's tenantGovernance query through the SAME
// shared tenantResolver as the ADR-023 ceilings and the ADR-063 shed priority: 60s TTL,
// inflight dedupe, refresh-concurrency cap, out-of-band refresh. That is not incidental
// reuse — connect-auth and ingest resolve this per inbound connection or message, on
// paths where the tenant is attacker-influenced, so a flood of distinct tenants must not
// amplify into unbounded lookups at user-management.
//
// 🔴 IT FAILS OPEN, and the reasoning belongs here rather than in a commit message
// because on a security gate a fail-open default reads as an oversight unless it is
// argued. An unresolvable or not-yet-fetched tenant reads "active", so a user-management
// outage lets devices under a purging tenant keep connecting, ingesting and receiving
// commands until the cache warms.
//
// That is defensible because of what this gate is FOR. It is not the correctness path
// for erasure — ADR-077's epoch-bounded per-area fence is (rdb.PurgedTenant and the
// callbacks in rdb.RegisterTenantFence), and it runs at the areas that own the data,
// inside the writing transaction, where it cannot be bypassed by an unreachable
// authority, cannot be stale, and cannot be missing from a call site. This gate exists
// to stop the bleeding early: it cuts the hot flows within one TTL of the operator's
// delete so the sweep is not chasing rows that are still arriving. Failing closed instead
// would make user-management a hard dependency of device connectivity for EVERY tenant —
// trading a bounded window of data that the sweep would have reclaimed anyway for an
// instance-wide availability regression.
//
// The 60s TTL bounds how long after a delete this gate keeps SAYING "active". It does not
// bound how long a tenant's device plane keeps moving, and conflating the two would be the
// easy misreading: this gate refuses new connects, new ingest and new dispatch, but a
// session established before the delete holds its broker-minted credential for that
// credential's own TTL (12h by default), and a device with a live subscription or
// lifetime drops off on its own schedule. Everything downstream of admission is the
// fence's job, and the fence is the guarantee.
type TenantLifecycleResolver struct {
	*tenantResolver[string]
}

// NewTenantLifecycleResolver wires a lifecycle resolver to user-management at umURL over
// the service-token client. Mirrors NewShedPriorityResolver.
func NewTenantLifecycleResolver(client *svcclient.Client, umURL string) *TenantLifecycleResolver {
	fetch := func(ctx context.Context, tenant string) (string, error) {
		return fetchPurgeState(ctx, client, umURL, tenant)
	}
	return &TenantLifecycleResolver{
		tenantResolver: newTenantResolver(fetch, LifecycleActive, "tenant-lifecycle"),
	}
}

// NewTenantLifecycleGate builds the ADR-077 lifecycle gate for one service: the
// closure its data path calls to decide whether a tenant has been deleted. It returns
// NIL when user-management is not configured, which every caller must treat as "the gate
// is off" rather than as an error.
//
// It exists so the services carrying this gate do not each re-derive the same six
// lines, because the parts worth getting identical are the parts a copy gets wrong.
// (Deliberately not counted or enumerated here, for the reason given above: the set
// has already grown once since this was written, and a number frozen in prose only
// ever drifts away from the tree. Ask the tree.)
//
//   - The service token is minted with tenant:read ALONE. Every one of these services
//     already holds an svcclient for something else, and reusing it would mean widening
//     that token's authorities to cover a lifecycle read. A gate is not a reason to grow
//     a service credential.
//   - The unconfigured case is a WARN and a nil gate, not a startup failure. Refusing to
//     start would make user-management a hard dependency of the device plane, which is
//     the same availability trade the resolver's fail-open rejects, only louder.
//
// subject names the calling service in the minted token, as svcclient.New expects.
func NewTenantLifecycleGate(umCfg config.UserManagementConfiguration, serviceSecret, subject string) func(string) bool {
	if serviceSecret == "" || umCfg.Hostname == "" || umCfg.Port == 0 {
		log.Warn().Str("service", subject).
			Msg("user-management endpoint or service secret not configured — this service will NOT refuse work for tenants that have been deleted.")
		return nil
	}
	client := svcclient.New(umCfg, serviceSecret, subject, []string{string(auth.TenantRead)})
	umURL := fmt.Sprintf("http://%s:%d/graphql", umCfg.Hostname, umCfg.Port)
	log.Info().Str("service", subject).Str("userManagement", umURL).
		Msg("Work for tenants that have been deleted will be refused (tenant deletion lifecycle).")
	return NewTenantLifecycleResolver(client, umURL).Deleted
}

// Deleted reports whether the tenant has been through the delete door and must no longer
// be acted on. It is the only method callers need, and it is phrased as the REFUSAL
// rather than as the state so that the fail-open default falls out of the type's zero
// reading: an unknown tenant is not deleted, and callers cannot get that backwards by
// comparing against the wrong constant.
//
// It deliberately uses resolve rather than resolveOK. The shed resolver needs the
// "was this actually fetched?" bool because its default is a bronze band that would shed
// a gold tenant during a cold-cache window — acting on the default there is a real,
// wrong action. Here the default is the null action: "not deleted" means "carry on
// exactly as before this gate existed", so there is nothing for a caller to hold off on.
func (r *TenantLifecycleResolver) Deleted(tenant string) bool {
	return lifecycleDeleted(r.resolve(tenant))
}

// lifecycleDeleted reads one wire state as a refusal. Split out pure so the rule is
// testable without a cache or a live user-management, the same way resolveShedPriority
// is — and so the two edge cases below are decided HERE, at the decision point, rather
// than normalized somewhere upstream where a second caller would miss them.
//
// The two unusual states are treated OPPOSITELY, which is the whole content of this
// function:
//
//   - An EMPTY state is a broken answer and reads active. It is unreachable through the
//     schema (purgeState is non-null), which is exactly why it is handled: were it ever
//     reachable, the naive `!= active` reading would lock every tenant on the instance
//     out of the platform. The fail-open argued on the type has to hold for the
//     malformed answer too, or it only covers the failures we thought of.
//   - An UNRECOGNISED state reads DELETED. ADR-077 may add a state between the cut and
//     reclamation, and a reader too old to name it must not conclude the tenant is fine
//     — that is the one direction where guessing keeps a service acting on a tenant an
//     operator has already cut off. Forward-compatibility here is worth more than
//     symmetry with the case above.
func lifecycleDeleted(state string) bool {
	return state != LifecycleActive && state != ""
}

// purgeStateQuery reads the tenant's lifecycle from user-management. Named rather than
// inlined at the call so it can be pinned by a test: the concrete svcclient.Client is not
// an interface, so nothing else about the fetch is reachable from a unit test, and a typo
// here would make every fetch on the instance error — which, given this resolver's
// deliberate fail-open, means every gate silently stops gating and only a WARN says so.
// governanceQuery is split out for the same reason.
const purgeStateQuery = `query { tenantGovernance { purgeState } }`

// purgeStateResponse is the wire shape of that query's data object. Named for the same
// reason as the query: an anonymous struct inside the fetch cannot be handed a real
// response by a test, so a wrong json tag would decode to "" — which lifecycleDeleted
// reads as ACTIVE, turning a broken decode into a gate that quietly passes everything.
type purgeStateResponse struct {
	TenantGovernance struct {
		PurgeState string `json:"purgeState"`
	} `json:"tenantGovernance"`
}

// fetchPurgeState reads the scalar purgeState from tenantGovernance. It caches the wire
// value verbatim; reading it is lifecycleDeleted's job.
func fetchPurgeState(ctx context.Context, client *svcclient.Client, umURL, tenant string) (string, error) {
	var out purgeStateResponse
	if err := client.Query(ctx, umURL, tenant, purgeStateQuery, nil, &out); err != nil {
		return "", err
	}
	return out.TenantGovernance.PurgeState, nil
}
