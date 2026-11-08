// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package auth

import "sort"

// The service-token authority ceiling: for each calling service, the authorities it may ask
// user-management to put in a service token.
//
// Today the mint endpoint checks that the secret is right and that each requested authority
// exists, and nothing more, so any holder of the shared secret can ask for "*" under any
// subject. The ceiling is the missing authorization step. It is a TABLE, not a policy engine:
// the callers are a small set fixed by the deployment, and a new caller is a code change
// (a new svcclient.New call) that already needs review, so the table changes in the same
// review. In this release it is only OBSERVED (the verdict is counted and logged, and the
// token is minted as before); refusing is a later release's decision, taken from what the
// observation shows.

// ServiceCeilingVerdict is the outcome of judging one mint request against the ceiling. The set
// is closed because the verdict is a metric label.
type ServiceCeilingVerdict string

const (
	// CeilingWithin: the caller is in the table and every requested authority is in its ceiling.
	CeilingWithin ServiceCeilingVerdict = "within_ceiling"
	// CeilingOver: the caller is in the table and asked for at least one authority outside its
	// ceiling (and did not ask for the wildcard).
	CeilingOver ServiceCeilingVerdict = "over_ceiling"
	// CeilingWildcard: the request names the super-authority. It outranks the other refusal
	// verdicts because no caller's ceiling may ever contain it.
	CeilingWildcard ServiceCeilingVerdict = "wildcard"
	// CeilingUnknownCaller: the claimed subject is not in the table.
	CeilingUnknownCaller ServiceCeilingVerdict = "unknown_caller"
)

// ServiceCeilingVerdicts lists every verdict, for pre-creating metric series.
var ServiceCeilingVerdicts = []ServiceCeilingVerdict{CeilingWithin, CeilingOver, CeilingWildcard, CeilingUnknownCaller}

// UnknownCallerLabel is the metric label for a subject that is not in the table. The subject is
// chosen by the sender, so it is never a label itself.
const UnknownCallerLabel = "other"

// serviceCallers is the ceiling table, derived from every svcclient.New call site and the
// tenant-lifecycle gate. It is unexported so no code can widen a ceiling at run time; read it
// through ServiceCallerNames and CheckServiceCeiling.
var serviceCallers = map[string][]Authority{
	"ai-inference":            {TenantRead},
	"command-delivery":        {DeviceRead, TenantRead, StateRead},
	"dashboard-management":    {DeviceRead, TenantRead},
	"device-management":       {DeviceRead, TenantRead},
	"event-management":        {DeviceRead},
	"event-processing":        {CommandWrite, TenantRead, DeviceRead, AIInfer},
	"event-sources":           {DeviceRead, TenantRead, StateRead, CommandWake},
	"lwm2m-ingest":            {DeviceRead, DeviceWrite, StateRead, TenantRead, CommandRead, CommandClaim, CommandPark},
	"notification-management": {TenantRead},
	"outbound-connectors":     {TenantRead},
	"sparkplug-ingest":        {TenantRead, DeviceRead, DeviceWrite, StateRead, CommandRead, CommandClaim, CommandPark},
	"update-management":       {DeviceRead, TenantRead},
}

// ServiceCallerNames returns the callers in the table, sorted.
func ServiceCallerNames() []string {
	out := make([]string, 0, len(serviceCallers))
	for name := range serviceCallers {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// ServiceCeiling returns a copy of one caller's ceiling and whether the caller is in the table.
func ServiceCeiling(caller string) ([]Authority, bool) {
	c, ok := serviceCallers[caller]
	if !ok {
		return nil, false
	}
	return append([]Authority(nil), c...), true
}

// ServiceCeilingResult is the judgement of one mint request.
type ServiceCeilingResult struct {
	// Caller is the table name when the subject is in the table, else UnknownCallerLabel. Safe as
	// a metric label.
	Caller  string
	Verdict ServiceCeilingVerdict
	// Offending lists the requested authorities that are outside the ceiling (the wildcard
	// included), in request order, de-duplicated. Empty when the verdict is CeilingWithin. For
	// an unknown caller it is every requested authority.
	Offending []string
}

// CheckServiceCeiling judges a mint request. It decides nothing: the caller records the result and
// (in a later release) acts on it.
//
// Order matters and is fixed: the wildcard outranks everything, then an unknown caller, then an
// over-ceiling request. A request that is both unknown and a wildcard is a wildcard.
func CheckServiceCeiling(subject string, authorities []string) ServiceCeilingResult {
	ceiling, known := serviceCallers[subject]
	res := ServiceCeilingResult{Caller: UnknownCallerLabel}
	if known {
		res.Caller = subject
	}
	allowed := make(map[Authority]struct{}, len(ceiling))
	for _, a := range ceiling {
		allowed[a] = struct{}{}
	}
	seen := make(map[string]struct{}, len(authorities))
	wildcard := false
	for _, a := range authorities {
		if Authority(a) == AuthorityAll {
			wildcard = true
		}
		if _, ok := allowed[Authority(a)]; ok {
			continue
		}
		if _, dup := seen[a]; dup {
			continue
		}
		seen[a] = struct{}{}
		res.Offending = append(res.Offending, a)
	}
	switch {
	case wildcard:
		res.Verdict = CeilingWildcard
	case !known:
		res.Verdict = CeilingUnknownCaller
	case len(res.Offending) > 0:
		res.Verdict = CeilingOver
	default:
		res.Verdict = CeilingWithin
	}
	return res
}
