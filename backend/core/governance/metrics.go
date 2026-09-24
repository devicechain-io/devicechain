// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package governance

import (
	core "github.com/devicechain-io/dc-microservice/core"
	"github.com/prometheus/client_golang/prometheus"
)

// unresolvedCauses are the Sources core.WithUnresolvedAdmissions reports, which are the
// cause label's whole vocabulary.
var unresolvedCauses = []core.CeilingSource{core.CeilingPending, core.CeilingUnreachable, core.CeilingUnknownTenant}

// NewUnresolvedAdmissions registers <area>_governance_unresolved_admissions_total{dimension,cause}
// with cause ∈ {pending, unreachable, unknown-tenant}, every child created at 0, and returns the
// counting func for core.WithUnresolvedAdmissions. Call once per service; a service
// whose two limiters meter the same dimension passes the same func to both.
//
// It counts traffic ADMITTED at the platform default because the tenant's own ceiling
// was not known. unreachable is the cause an operator must act on (user-management
// cannot be asked); unknown-tenant (it answered that no such tenant exists) and pending
// (nothing has answered yet — a cold cache, or refreshes the resolver's own budget
// refused under a spray of novel names) are charted but never page.
func NewUnresolvedAdmissions(ms *core.Microservice, dim Dimension) func(core.CeilingSource) {
	vec := ms.NewCounterVec("governance_unresolved_admissions_total",
		"Count of admissions metered at the platform default because the tenant's own ceiling "+
			"was not known, by governance dimension and cause (unreachable: user-management could "+
			"not be asked; unknown-tenant: it answered that no such tenant exists; pending: no "+
			"answer yet)",
		[]string{"dimension", "cause"})
	children := make(map[core.CeilingSource]prometheus.Counter, len(unresolvedCauses))
	for _, c := range unresolvedCauses {
		children[c] = vec.WithLabelValues(dim.Name, c.String())
	}
	return func(src core.CeilingSource) {
		if c, ok := children[src]; ok {
			c.Inc()
		}
	}
}

// NewOverflowAdmissions registers <area>_ratelimit_overflow_admissions_total, created at 0:
// the admissions served by a limiter's shared overflow bucket (see
// core.TenantRateLimiter.AllowUntrusted).
func NewOverflowAdmissions(ms *core.Microservice) prometheus.Counter {
	return ms.NewCounter("ratelimit_overflow_admissions_total",
		"Count of admissions served by the one allowance every unconfirmed tenant name shares "+
			"once the fixed set of per-name allowances is in use")
}
