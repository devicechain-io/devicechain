// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package identity

import (
	"fmt"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/sourcelimit"
)

func TestBeginLimitersSourceAndGlobalBuckets(t *testing.T) {
	for _, tc := range []struct {
		name        string
		newLimiter  func(...sourcelimit.Option) beginLimiter
		sourceBurst int
		globalBurst int
	}{
		{"SSO", newBeginLimiter, ssoBeginSourceLimits.Burst, ssoBeginGlobalLimits.Burst},
		{"MFA verify", newMfaVerifyLimiter, mfaVerifyPerSourceLimits.Burst, mfaVerifyGlobalLimits.Burst},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Freeze refills while exercising the production limits.
			now := time.Unix(1700000000, 0)
			clock := sourcelimit.WithClock(func() time.Time { return now })
			t.Run("IPv6 shares a /64", func(t *testing.T) {
				l := tc.newLimiter(clock)
				for i := 1; i <= tc.sourceBurst; i++ {
					if !l.allow(fmt.Sprintf("2001:db8::%x", i)) {
						t.Fatalf("begin %d within /64 burst refused", i)
					}
				}
				if l.allow(fmt.Sprintf("2001:db8::%x", tc.sourceBurst+1)) {
					t.Error("new address in exhausted /64 bypassed source burst")
				}
				// Refused traffic must not consume the remaining global allowance.
				for i := 0; i < tc.globalBurst; i++ {
					if l.allow(fmt.Sprintf("2001:db8::%x", tc.sourceBurst+2+i)) {
						t.Error("spray in exhausted /64 allowed")
						break
					}
				}
				for _, source := range []string{"2001:db8:0:1::1", "192.0.2.1"} {
					if !l.allow(source) {
						t.Errorf("independent source %s refused after /64 spray", source)
					}
				}
			})
			t.Run("IPv4 buckets remain independent", func(t *testing.T) {
				l := tc.newLimiter(clock)
				for i := 0; i < tc.sourceBurst; i++ {
					if !l.allow("192.0.2.1") {
						t.Fatalf("IPv4 begin %d within burst refused", i+1)
					}
				}
				if l.allow("192.0.2.1") {
					t.Error("exhausted IPv4 source allowed")
				}
				if !l.allow("192.0.2.2") {
					t.Error("different IPv4 address in same /24 refused")
				}
			})
			t.Run("global bucket remains shared", func(t *testing.T) {
				l := tc.newLimiter(clock)
				for i := 1; i <= tc.globalBurst; i++ {
					if !l.allow(fmt.Sprintf("2001:db8:%x::1", i)) {
						t.Fatalf("source %d within global burst refused", i)
					}
				}
				if l.allow("192.0.2.1") {
					t.Error("fresh source bypassed exhausted global bucket")
				}
			})
		})
	}
}
