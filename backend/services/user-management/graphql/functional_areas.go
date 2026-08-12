// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The functional-area capability query: which areas this instance actually
// deployed, so the console can stop offering features the cluster does not serve.
//
// THE DEFECT IT CLOSES. The console addresses areas by convention
// (/api/{area}/graphql) while the ingress emits a rule only for ENABLED areas. On
// an instance without ai-inference, the "AI models" tab issues a POST that matches
// no api rule, falls through to the SPA's static nginx, and comes back 405 — a
// status code describing nginx's routing rather than the request, which reads as
// "wrong HTTP method". Connectors (outbound-connectors) fails the same way. The
// console had no notion of which areas were deployed.
//
// WHY user-management SERVES IT. It is a core area: every profile deploys it, so
// the question can always be asked. The answer is derived by listing its own
// per-area config mount (core.EnabledFunctionalAreas) — the chart already wrote
// the area set onto every pod, so this adds no catalog to keep in step with the
// chart's `devicechain.enabledAreas` guard.
//
// IT IS SERVED ON TWO PLANES ON PURPOSE. The affected surfaces straddle them:
// Connectors is a tenant-plane page, while the AI provider/packaging/models
// screens are identity-token admin pages. Neither token subsumes the other — the
// identity token has NO refresh and is dropped on load once expired, while the
// tenant refresh pair persists, so a reload can leave a console holding only a
// tenant session; and an operator superuser browsing a tenant they are not a
// member of may hold no tenant session at all. One resolver, two entry points.

package graphql

import (
	"context"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
)

// functionalAreaConfigDir is the mount the two resolvers below read. It is a
// package variable solely so tests can point it at a fixture directory; nothing
// in production reassigns it.
var functionalAreaConfigDir = core.MicroserviceConfigDir

// FunctionalAreas resolves the deployed-area set on the TENANT plane.
//
// 🔴 The authentication check is explicit because nothing else here performs one.
// The tenant GraphQL handler deliberately lets a request with no token through
// (login and refresh have to run before a token exists), and the fail-closed
// tenant-scope DB callback never fires for this resolver because it touches no
// tenant-scoped table. Measured before adding this: an unauthenticated POST to
// /api/user-management/graphql answers 200. Without the check below, the
// instance's area topology would be readable by anyone who can reach the URL.
//
// Any authenticated principal may read it, with no authority requirement. It
// reveals only which areas the operator deployed — which the console already
// exposes through which surfaces work — and gating it on an authority would
// mean the surfaces a role cannot see are also the ones it cannot be told about.
func (r *SchemaResolver) FunctionalAreas(ctx context.Context) ([]string, error) {
	if _, ok := auth.ClaimsFromContext(ctx); !ok {
		return nil, auth.ErrUnauthenticated
	}
	return core.EnabledFunctionalAreasIn(functionalAreaConfigDir)
}

// FunctionalAreas resolves the deployed-area set on the ADMIN (identity) plane.
//
// No explicit claims check here, and the asymmetry with the tenant plane above is
// deliberate rather than an oversight: this schema is served by
// NewAdminHttpHandler, which validates an identity-tier token before any resolver
// runs. Verified against a running instance — an unauthenticated POST to
// /api/user-management/admin/graphql answers 401, where the tenant plane answers
// 200.
func (r *AdminResolver) FunctionalAreas(ctx context.Context) ([]string, error) {
	return core.EnabledFunctionalAreasIn(functionalAreaConfigDir)
}
