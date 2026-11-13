// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package x509trust

import (
	"crypto/x509"
	"testing"
	"time"
)

// FuzzVerifyOCSPResponse feeds the response verifier mutated bytes under a fixed certificate and
// issuer. It must never panic, and an ACCEPTED response is re-checked against facts that do not
// depend on how the parser reached it.
func FuzzVerifyOCSPResponse(f *testing.F) {
	fx := newOCSPFixture(f)
	good := fx.good()
	revoked := fx.good()
	revoked.statusTag, revoked.revokedAt, revoked.reasonCode = 1, testNow.Add(-time.Hour), 1
	delegated := fx.good()
	delegated.signer, delegated.embed = fx.delegate, []*x509.Certificate{fx.delegate.cert}
	for _, b := range []ocspBuild{good, revoked, delegated} {
		d := b.der(f)
		f.Add(d, int64(0))
		f.Add(d[:len(d)/2], int64(0))
		f.Add(append(append([]byte(nil), d...), 0), int64(0))
	}
	f.Add([]byte{}, int64(0))
	f.Add(good.der(f), int64(7*24*3600))
	f.Fuzz(func(t *testing.T, raw []byte, skewSeconds int64) {
		now := testNow.Add(time.Duration(skewSeconds%(30*24*3600)) * time.Second)
		ans, err := VerifyOCSPResponse(raw, fx.leaf.cert, fx.inter.cert, now, ocspOpts)
		if err != nil {
			if ans != nil {
				t.Fatal("a refusal returned an answer")
			}
			if OCSPFailureOf(err) == "" {
				t.Fatalf("an unclassified refusal: %v", err)
			}
			return
		}
		switch ans.Status {
		case OCSPStatusGood, OCSPStatusRevoked, OCSPStatusUnknown:
		default:
			t.Fatalf("status %q", ans.Status)
		}
		if !now.Before(ans.ValidUntil) {
			t.Fatalf("accepted a response already unusable: validUntil %v now %v", ans.ValidUntil, now)
		}
		if ans.ValidUntil.After(ans.ThisUpdate.Add(ocspOpts.MaxAge)) {
			t.Fatalf("validUntil %v exceeds the replay bound from thisUpdate %v", ans.ValidUntil, ans.ThisUpdate)
		}
	})
}
