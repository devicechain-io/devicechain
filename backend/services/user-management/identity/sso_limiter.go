// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package identity

import (
	"time"

	"github.com/devicechain-io/dc-microservice/sourcelimit"
)

// beginSso is public, so anyone can call it, and each call that gets as far as a
// connection writes a ten-minute record to the state bucket. Two in-process token buckets
// ration it:
//
//   - one per SOURCE address, which stops a single sender from taking the whole allowance;
//   - one GLOBAL, which is the part that holds against a sender who varies the source
//     (the address is whatever the nearest proxy reports, and a fairness key only).
//
// They are per replica, not shared: the bound on what a sender can write is the sum over
// replicas, which is the same order of magnitude and needs no store of its own. The state
// bucket's byte ceiling is the hard stop behind them, and a full bucket makes beginSso fail
// closed (and raises an alert) rather than degrade.
const (
	// ssoBeginPerSourceBurst and ssoBeginPerSourceEvery: a person retries a few times;
	// a script cannot sustain more than one begin every two seconds from one address.
	ssoBeginPerSourceBurst = 5
	ssoBeginPerSourceEvery = 2 * time.Second
	// ssoBeginGlobalBurst and ssoBeginGlobalEvery: a few hundred sign-ins a minute for the
	// whole replica, far above any real login rate and far below what fills the bucket.
	ssoBeginGlobalBurst = 200
	ssoBeginGlobalEvery = 250 * time.Millisecond
	// ssoLimiterMaxSources bounds the per-source map; past it the oldest idle entries are
	// dropped, so a spray of distinct addresses cannot grow memory.
	ssoLimiterMaxSources = 10000
)

// ssoBeginSourceLimits and ssoBeginGlobalLimits are beginSso's, above. The global one is a
// single key (a bucket shared by every sender).
var (
	ssoBeginSourceLimits = sourcelimit.Limits{
		Burst: ssoBeginPerSourceBurst, Every: ssoBeginPerSourceEvery, MaxKeys: ssoLimiterMaxSources,
	}
	ssoBeginGlobalLimits = sourcelimit.Limits{
		Burst: ssoBeginGlobalBurst, Every: ssoBeginGlobalEvery, MaxKeys: 1,
	}
)

// beginLimiter is the pair of buckets above, built from the shared per-source limiter in
// core/sourcelimit. The global bucket is this endpoint's own policy: the primitive carries none.
type beginLimiter struct {
	perSource, global *sourcelimit.Limiter
}

func newBeginLimiter(opts ...sourcelimit.Option) beginLimiter {
	return beginLimiter{
		perSource: sourcelimit.Must(ssoBeginSourceLimits, opts...),
		global:    sourcelimit.Must(ssoBeginGlobalLimits, opts...),
	}
}

// allow reports whether a begin from source may proceed. A refusal from either bucket
// refuses; the per-source bucket is charged first so a sender already over its own
// allowance does not also drain the global one.
func (l beginLimiter) allow(source string) bool {
	return l.perSource.Allow(source) && l.global.Allow("")
}
