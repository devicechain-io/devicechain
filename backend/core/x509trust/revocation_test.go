// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package x509trust

import (
	"crypto/x509"
	"errors"
	"math/big"
	"testing"
	"time"
)

// crossPKI is the shape revocation has to get right: ONE leaf that verifies through TWO
// intermediates sharing a key and a subject (a re-issued, cross-signed CA), with different
// lifetimes, so the longest-lived path is a known one.
//
//	leaf(20h) -> ix(old, 10h) -> root
//	leaf(20h) -> ix(new, 48h) -> root     <- the path VerifyClientChain alone picks
type crossPKI struct {
	root, ixOld, ixNew, leaf *node
}

func newCrossPKI(t testing.TB) crossPKI {
	root := issue(t, nil, spec{cn: "root", ca: true})
	k := ecKey(t)
	ixOld := issue(t, root, spec{cn: "ix", ca: true, key: k, notAfter: testNow.Add(10 * time.Hour)})
	ixNew := issue(t, root, spec{cn: "ix", ca: true, key: k, notAfter: testNow.Add(48 * time.Hour)})
	leaf := issue(t, ixNew, spec{cn: "device", notAfter: testNow.Add(20 * time.Hour)})
	return crossPKI{root, ixOld, ixNew, leaf}
}

func (p crossPKI) verify(t testing.TB) *Verified {
	t.Helper()
	v, err := VerifyClientChain(p.root.cert, chainOf(p.leaf, p.ixOld, p.ixNew), testNow)
	if err != nil {
		t.Fatalf("cross-signed chain refused: %v", err)
	}
	return v
}

func entriesState() RevocationState {
	return RevocationState{Mode: ModeEntries, Now: testNow}
}

func hasCert(chain []*x509.Certificate, c *x509.Certificate) bool {
	for _, x := range chain {
		if x.Equal(c) {
			return true
		}
	}
	return false
}

func mustSerial(t testing.TB, c *x509.Certificate) string {
	t.Helper()
	s, ok := SerialHex(c.SerialNumber)
	if !ok {
		t.Fatal("certificate has no usable serial")
	}
	return s
}

func TestVerifiedKeepsEveryPath(t *testing.T) {
	p := newCrossPKI(t)
	v := p.verify(t)
	if len(v.Chains) != 2 {
		t.Fatalf("both cross-signed paths must be kept, got %d", len(v.Chains))
	}
	if !hasCert(v.Chain, p.ixNew.cert) || v.NotAfter != testNow.Add(20*time.Hour) {
		t.Fatalf("the longest-lived path is the one through the new intermediate (notAfter %v)", v.NotAfter)
	}
	found := false
	for _, c := range v.Chains {
		if &c[0] == &v.Chain[0] {
			found = true
		}
		if c[0] != p.leaf.cert || !c[len(c)-1].Equal(p.root.cert) {
			t.Fatal("a kept path does not run from the leaf to the anchor")
		}
	}
	if !found {
		t.Fatal("Chain is not one of Chains")
	}

	// The unambiguous case is unchanged: one path, Chain is that path.
	b := newBasicPKI(t)
	bv, err := VerifyClientChain(b.root.cert, chainOf(b.leaf, b.inter), testNow)
	if err != nil || len(bv.Chains) != 1 || len(bv.Chain) != 3 || &bv.Chains[0][0] != &bv.Chain[0] {
		t.Fatalf("single-path chain changed: %v %d", err, len(bv.Chains))
	}
}

func TestRevocationKeys(t *testing.T) {
	b := newBasicPKI(t)
	v, err := VerifyClientChain(b.root.cert, chainOf(b.leaf, b.inter), testNow)
	if err != nil {
		t.Fatal(err)
	}
	q := v.RevocationKeys()
	wantFP := map[string]bool{Fingerprint(b.leaf.cert): true, Fingerprint(b.inter.cert): true}
	if len(q.Fingerprints) != 2 || !wantFP[q.Fingerprints[0]] || !wantFP[q.Fingerprints[1]] {
		t.Fatalf("fingerprints: want leaf+intermediate only (the anchor excluded), got %v", q.Fingerprints)
	}
	if Fingerprint(b.root.cert) == q.Fingerprints[0] || Fingerprint(b.root.cert) == q.Fingerprints[1] {
		t.Fatal("the anchor is in the query")
	}
	wantSer := map[IssuerSerial]bool{
		{Issuer: IssuerIDOfCA(b.inter.cert), Serial: mustSerial(t, b.leaf.cert)}: true,
		{Issuer: IssuerIDOfCA(b.root.cert), Serial: mustSerial(t, b.inter.cert)}: true,
	}
	if len(q.Serials) != 2 || !wantSer[q.Serials[0]] || !wantSer[q.Serials[1]] {
		t.Fatalf("serials: %v", q.Serials)
	}
	wantIss := map[IssuerID]bool{IssuerIDOfCA(b.inter.cert): true, IssuerIDOfCA(b.root.cert): true}
	if len(q.Issuers) != 2 || !wantIss[q.Issuers[0]] || !wantIss[q.Issuers[1]] {
		t.Fatalf("issuers: %v", q.Issuers)
	}
}

func TestRevocationKeysCoverEveryPathDeduplicatedAndSorted(t *testing.T) {
	p := newCrossPKI(t)
	q := p.verify(t).RevocationKeys()
	// leaf + two intermediates; the leaf's issuer identity is shared by both paths (same key,
	// same name), the intermediates share the root's.
	if len(q.Fingerprints) != 3 {
		t.Fatalf("fingerprints across both paths: %v", q.Fingerprints)
	}
	if len(q.Serials) != 3 || len(q.Issuers) != 2 {
		t.Fatalf("serials %d issuers %d", len(q.Serials), len(q.Issuers))
	}
	for i := 1; i < len(q.Fingerprints); i++ {
		if q.Fingerprints[i-1] >= q.Fingerprints[i] {
			t.Fatal("fingerprints not sorted and unique")
		}
	}
	if !lessIssuer(q.Issuers[0], q.Issuers[1]) {
		t.Fatal("issuers not sorted")
	}
}

// What a device PRESENTS must not steer the check: a revoked certificate that rode along on no
// verified path is not looked up, and revoking it changes nothing.
func TestRevocationIgnoresPresentedCertificatesOffThePath(t *testing.T) {
	b := newBasicPKI(t)
	stray := issue(t, b.root, spec{cn: "stray-ca", ca: true})
	v, err := VerifyClientChain(b.root.cert, chainOf(b.leaf, b.inter, stray), testNow)
	if err != nil {
		t.Fatal(err)
	}
	q := v.RevocationKeys()
	for _, f := range q.Fingerprints {
		if f == Fingerprint(stray.cert) {
			t.Fatal("a certificate on no verified path is in the query")
		}
	}
	s := entriesState()
	s.RevokedFingerprints = map[string]RevocationReason{Fingerprint(stray.cert): RevocationKeyCompromise}
	s.RevokedSerials = map[IssuerSerial]RevocationReason{
		{Issuer: IssuerIDOfCA(b.root.cert), Serial: mustSerial(t, stray.cert)}: RevocationKeyCompromise,
	}
	if _, err := v.ApplyRevocation(s); err != nil {
		t.Fatalf("revoking a certificate off the path refused a valid device: %v", err)
	}
}

func TestApplyRevocationCrossSignedAlternativeAdmitted(t *testing.T) {
	p := newCrossPKI(t)
	v := p.verify(t)

	// Control: nothing revoked keeps both paths and the same longest-lived pick.
	out, err := v.ApplyRevocation(entriesState())
	if err != nil || len(out.Chains) != 2 || !hasCert(out.Chain, p.ixNew.cert) {
		t.Fatalf("control: %v", err)
	}

	for name, st := range map[string]RevocationState{
		"by fingerprint": {RevokedFingerprints: map[string]RevocationReason{Fingerprint(p.ixNew.cert): RevocationCACompromise}},
		"by issuer+serial": {RevokedSerials: map[IssuerSerial]RevocationReason{
			{Issuer: IssuerIDOfCA(p.root.cert), Serial: mustSerial(t, p.ixNew.cert)}: RevocationCACompromise}},
	} {
		t.Run(name, func(t *testing.T) {
			st.Mode, st.Now = ModeEntries, testNow
			out, err := v.ApplyRevocation(st)
			if err != nil {
				t.Fatalf("the valid cross-signed alternative must be admitted: %v", err)
			}
			if len(out.Chains) != 1 || hasCert(out.Chain, p.ixNew.cert) || !hasCert(out.Chain, p.ixOld.cert) {
				t.Fatal("the surviving path is not the one through the old intermediate")
			}
			// The result's NotAfter is the surviving path's, not the discarded longer one's.
			if out.NotAfter != testNow.Add(10*time.Hour) {
				t.Fatalf("session clamp %v must follow the surviving path", out.NotAfter)
			}
			if out.Leaf != p.leaf.cert || out.Fingerprint != Fingerprint(p.leaf.cert) {
				t.Fatal("result is not about the same leaf")
			}
			if len(v.Chains) != 2 || !hasCert(v.Chain, p.ixNew.cert) {
				t.Fatal("ApplyRevocation modified its receiver")
			}
		})
	}
}

func TestApplyRevocationRefusesWhenEveryPathIsRevoked(t *testing.T) {
	p := newCrossPKI(t)
	v := p.verify(t)
	cases := map[string]RevocationState{
		"leaf by fingerprint": {RevokedFingerprints: map[string]RevocationReason{Fingerprint(p.leaf.cert): RevocationKeyCompromise}},
		"leaf by serial": {RevokedSerials: map[IssuerSerial]RevocationReason{
			{Issuer: IssuerIDOfCA(p.ixNew.cert), Serial: mustSerial(t, p.leaf.cert)}: RevocationUnspecified}},
		"both intermediates": {RevokedFingerprints: map[string]RevocationReason{
			Fingerprint(p.ixOld.cert): RevocationCACompromise, Fingerprint(p.ixNew.cert): RevocationCACompromise}},
	}
	for name, st := range cases {
		t.Run(name, func(t *testing.T) {
			st.Mode, st.Now = ModeEntries, testNow
			out, err := v.ApplyRevocation(st)
			if out != nil || ReasonOf(err) != ReasonRevoked || !errors.Is(err, ErrChainInvalid) {
				t.Fatalf("want revoked, got %v / %v", out, err)
			}
		})
	}
}

func TestApplyRevocationIssuerSerialIsPerIssuer(t *testing.T) {
	b := newBasicPKI(t)
	v, _ := VerifyClientChain(b.root.cert, chainOf(b.leaf, b.inter), testNow)
	st := entriesState()
	// Same serial, WRONG issuer: not this certificate.
	st.RevokedSerials = map[IssuerSerial]RevocationReason{
		{Issuer: IssuerIDOfCA(b.root.cert), Serial: mustSerial(t, b.leaf.cert)}: RevocationKeyCompromise,
	}
	if _, err := v.ApplyRevocation(st); err != nil {
		t.Fatalf("a serial under another issuer revoked this leaf: %v", err)
	}
	// Right issuer, different serial.
	st.RevokedSerials = map[IssuerSerial]RevocationReason{
		{Issuer: IssuerIDOfCA(b.inter.cert), Serial: "ffffff"}: RevocationKeyCompromise,
	}
	if _, err := v.ApplyRevocation(st); err != nil {
		t.Fatalf("a different serial revoked this leaf: %v", err)
	}
	// Control: right issuer, right serial.
	st.RevokedSerials = map[IssuerSerial]RevocationReason{
		{Issuer: IssuerIDOfCA(b.inter.cert), Serial: mustSerial(t, b.leaf.cert)}: RevocationKeyCompromise,
	}
	if _, err := v.ApplyRevocation(st); ReasonOf(err) != ReasonRevoked {
		t.Fatalf("control: %v", err)
	}
}

// A CA key reused under two names is two issuers. A list for one covers none of the other's
// certificates, and an entry under one does not revoke the other's serials.
func TestIssuerIdentityIsKeyAndName(t *testing.T) {
	root := issue(t, nil, spec{cn: "root", ca: true})
	k := ecKey(t)
	caA := issue(t, root, spec{cn: "ca-A", ca: true, key: k})
	caB := issue(t, root, spec{cn: "ca-B", ca: true, key: k})
	leafA := issue(t, caA, spec{cn: "dev-a"})
	leafB := issue(t, caB, spec{cn: "dev-b"})

	idA, idB := IssuerIDOfCA(caA.cert), IssuerIDOfCA(caB.cert)
	if idA.Key != idB.Key || idA.Name == idB.Name {
		t.Fatalf("fixture: same key, different names expected: %v %v", idA, idB)
	}
	vA, err := VerifyClientChain(root.cert, chainOf(leafA, caA), testNow)
	if err != nil {
		t.Fatal(err)
	}
	vB, err := VerifyClientChain(root.cert, chainOf(leafB, caB), testNow)
	if err != nil {
		t.Fatal(err)
	}
	if vA.RevocationKeys().Issuers[0] == vB.RevocationKeys().Issuers[0] && vA.RevocationKeys().Issuers[1] == vB.RevocationKeys().Issuers[1] {
		t.Fatal("two names under one key produced the same issuer identities")
	}

	// Completeness: root and A have lists, B does not.
	req := RevocationState{
		Mode: ModeRequireCurrent, Now: testNow,
		Lists: map[IssuerID]ListStatus{
			IssuerIDOfCA(root.cert): {NextUpdate: testNow.Add(time.Hour)},
			idA:                     {NextUpdate: testNow.Add(time.Hour)},
		},
	}
	if _, err := vA.ApplyRevocation(req); err != nil {
		t.Fatalf("control: A is covered: %v", err)
	}
	if _, err := vB.ApplyRevocation(req); ReasonOf(err) != ReasonRevocationUnknown {
		t.Fatalf("B shares A's key but not its name, so it is not covered: %v", err)
	}

	// Entries: a serial listed under A's identity does not revoke B's certificate even when
	// the serial number is the same.
	st := entriesState()
	st.RevokedSerials = map[IssuerSerial]RevocationReason{
		{Issuer: idA, Serial: mustSerial(t, leafB.cert)}: RevocationKeyCompromise,
	}
	if _, err := vB.ApplyRevocation(st); err != nil {
		t.Fatalf("an entry under A's identity revoked B's certificate: %v", err)
	}
	st.RevokedSerials = map[IssuerSerial]RevocationReason{
		{Issuer: idB, Serial: mustSerial(t, leafB.cert)}: RevocationKeyCompromise,
	}
	if _, err := vB.ApplyRevocation(st); ReasonOf(err) != ReasonRevoked {
		t.Fatalf("control: B's own entry: %v", err)
	}
}

func TestApplyRevocationRequireCurrent(t *testing.T) {
	b := newBasicPKI(t)
	v, _ := VerifyClientChain(b.root.cert, chainOf(b.leaf, b.inter), testNow)
	rootID, interID := IssuerIDOfCA(b.root.cert), IssuerIDOfCA(b.inter.cert)
	next := testNow.Add(time.Hour)
	full := map[IssuerID]ListStatus{rootID: {NextUpdate: next}, interID: {NextUpdate: next}}

	st := func(lists map[IssuerID]ListStatus, now time.Time, grace time.Duration) RevocationState {
		return RevocationState{Mode: ModeRequireCurrent, Now: now, Lists: lists, StaleGrace: grace}
	}
	if _, err := v.ApplyRevocation(st(full, testNow, 0)); err != nil {
		t.Fatalf("control (both lists current): %v", err)
	}
	for name, s := range map[string]RevocationState{
		"no lists":          st(nil, testNow, 0),
		"only the root":     st(map[IssuerID]ListStatus{rootID: {NextUpdate: next}}, testNow, 0),
		"only the issuer":   st(map[IssuerID]ListStatus{interID: {NextUpdate: next}}, testNow, 0),
		"one nanosecond up": st(full, next.Add(time.Nanosecond), 0),
		"grace exceeded":    st(full, next.Add(time.Hour+time.Nanosecond), time.Hour),
	} {
		t.Run(name, func(t *testing.T) {
			if out, err := v.ApplyRevocation(s); out != nil || ReasonOf(err) != ReasonRevocationUnknown {
				t.Fatalf("want revocation_unknown, got %v / %v", out, err)
			}
		})
	}
	// Boundaries are inclusive: now == nextUpdate (+ grace) is still current.
	if _, err := v.ApplyRevocation(st(full, next, 0)); err != nil {
		t.Fatalf("now == nextUpdate must be current: %v", err)
	}
	if _, err := v.ApplyRevocation(st(full, next.Add(time.Hour), time.Hour)); err != nil {
		t.Fatalf("now == nextUpdate+grace must be current: %v", err)
	}

	// ENTRIES is fail-open on completeness: no lists, stale lists, all admitted.
	for name, lists := range map[string]map[IssuerID]ListStatus{
		"no lists": nil,
		"stale":    {rootID: {NextUpdate: testNow.Add(-48 * time.Hour)}, interID: {NextUpdate: testNow.Add(-48 * time.Hour)}},
	} {
		s := entriesState()
		s.Lists = lists
		if _, err := v.ApplyRevocation(s); err != nil {
			t.Fatalf("ENTRIES %s must admit: %v", name, err)
		}
	}
	// ...but a listed entry is enforced whatever the list's age (stale entries still refuse).
	s := entriesState()
	s.Lists = map[IssuerID]ListStatus{interID: {NextUpdate: testNow.Add(-48 * time.Hour)}}
	s.RevokedFingerprints = map[string]RevocationReason{Fingerprint(b.leaf.cert): RevocationUnspecified}
	if _, err := v.ApplyRevocation(s); ReasonOf(err) != ReasonRevoked {
		t.Fatalf("a stale list's entry must still refuse: %v", err)
	}
	// A known entry is definitive: with the list missing too, the path is judged revoked, not unknown.
	rs := st(nil, testNow, 0)
	rs.RevokedFingerprints = map[string]RevocationReason{Fingerprint(b.leaf.cert): RevocationUnspecified}
	if _, err := v.ApplyRevocation(rs); ReasonOf(err) != ReasonRevoked {
		t.Fatalf("revoked and incomplete in one path must be revoked: %v", err)
	}
}

// One path is revoked and the other cannot be shown current: the answer is revocation_unknown,
// the actionable one; make the second path current and it is admitted.
func TestApplyRevocationMixedPaths(t *testing.T) {
	root := issue(t, nil, spec{cn: "root", ca: true})
	mid := issue(t, root, spec{cn: "mid", ca: true})
	k := ecKey(t)
	ixNew := issue(t, root, spec{cn: "ix", ca: true, key: k}) // path A: leaf -> ixNew -> root
	ixOld := issue(t, mid, spec{cn: "ix", ca: true, key: k})  // path B: leaf -> ixOld -> mid -> root
	leaf := issue(t, ixNew, spec{cn: "device"})
	v, err := VerifyClientChain(root.cert, chainOf(leaf, ixOld, ixNew, mid), testNow)
	if err != nil || len(v.Chains) != 2 {
		t.Fatalf("fixture: %v %d", err, len(v.Chains))
	}
	next := testNow.Add(time.Hour)
	lists := map[IssuerID]ListStatus{
		IssuerIDOfCA(root.cert):  {NextUpdate: next},
		IssuerIDOfCA(ixNew.cert): {NextUpdate: next},
	} // no list for mid: path B incomplete
	st := RevocationState{
		Mode: ModeRequireCurrent, Now: testNow, Lists: lists,
		RevokedFingerprints: map[string]RevocationReason{Fingerprint(ixNew.cert): RevocationCACompromise},
	}
	if _, err := v.ApplyRevocation(st); ReasonOf(err) != ReasonRevocationUnknown {
		t.Fatalf("revoked + unknown must be unknown: %v", err)
	}
	st.Lists[IssuerIDOfCA(mid.cert)] = ListStatus{NextUpdate: next}
	out, err := v.ApplyRevocation(st)
	if err != nil || len(out.Chains) != 1 || !hasCert(out.Chain, ixOld.cert) {
		t.Fatalf("once path B is current it must be admitted: %v", err)
	}
}

// 🔴 Refuses by default: a state the function cannot judge is an error, never a pass, and the
// error is NOT a classified ChainError (the caller's switch must refuse in its default arm).
func TestApplyRevocationRefusesByDefault(t *testing.T) {
	b := newBasicPKI(t)
	v, _ := VerifyClientChain(b.root.cert, chainOf(b.leaf, b.inter), testNow)
	if _, err := v.ApplyRevocation(entriesState()); err != nil {
		t.Fatalf("control: %v", err)
	}
	bad := map[string]RevocationState{
		"zero value":     {},
		"zero mode":      {Now: testNow},
		"unknown mode":   {Mode: "entries", Now: testNow},
		"zero now":       {Mode: ModeEntries},
		"negative grace": {Mode: ModeEntries, Now: testNow, StaleGrace: -time.Second},
	}
	for name, s := range bad {
		t.Run(name, func(t *testing.T) {
			out, err := v.ApplyRevocation(s)
			if out != nil || err == nil {
				t.Fatalf("admitted a state it cannot judge: %v", out)
			}
			if !errors.Is(err, ErrRevocationState) || ReasonOf(err) != "" || errors.Is(err, ErrChainInvalid) {
				t.Fatalf("want the unclassified state error, got %v", err)
			}
		})
	}
	t.Run("no verified paths", func(t *testing.T) {
		handBuilt := &Verified{Leaf: b.leaf.cert, Chain: v.Chain} // Chains left empty
		if out, err := handBuilt.ApplyRevocation(entriesState()); out != nil || !errors.Is(err, ErrRevocationState) {
			t.Fatalf("a Verified with no Chains passed: %v %v", out, err)
		}
		var nilV *Verified
		if out, err := nilV.ApplyRevocation(entriesState()); out != nil || err == nil {
			t.Fatal("nil Verified passed")
		}
	})
	t.Run("degenerate path", func(t *testing.T) {
		short := &Verified{Leaf: b.leaf.cert, Chains: [][]*x509.Certificate{{b.leaf.cert}}}
		if _, err := short.ApplyRevocation(entriesState()); ReasonOf(err) != ReasonRevocationUnknown {
			t.Fatalf("a path with no issuer is not a verified path: %v", err)
		}
		withNil := &Verified{Leaf: b.leaf.cert, Chains: [][]*x509.Certificate{{b.leaf.cert, nil, b.root.cert}}}
		if _, err := withNil.ApplyRevocation(entriesState()); err == nil {
			t.Fatal("a path with a nil certificate passed")
		}
		if q := withNil.RevocationKeys(); len(q.Fingerprints) != 0 {
			t.Fatal("keys from a degenerate path")
		}
	})
}

// A serial that is not a positive integer cannot be named by a CRL, so the path through it
// cannot be checked: dropped in every mode.
func TestApplyRevocationUnrepresentableSerialIsUnknown(t *testing.T) {
	root := issue(t, nil, spec{cn: "root", ca: true})
	leaf := issue(t, root, spec{cn: "zero-serial", mutate: func(c *x509.Certificate) { c.SerialNumber = big.NewInt(0) }})
	v, err := VerifyClientChain(root.cert, chainOf(leaf), testNow)
	if err != nil {
		t.Skipf("the verifier refuses a zero serial before revocation sees it: %v", err)
	}
	if len(v.RevocationKeys().Serials) != 0 {
		t.Fatal("a zero serial produced a serial key")
	}
	if _, err := v.ApplyRevocation(entriesState()); ReasonOf(err) != ReasonRevocationUnknown {
		t.Fatalf("want revocation_unknown, got %v", err)
	}
}

func TestSerialHex(t *testing.T) {
	for in, want := range map[int64]string{1: "1", 255: "ff", 256: "100", 0xabcdef: "abcdef"} {
		if got, ok := SerialHex(big.NewInt(in)); !ok || got != want {
			t.Errorf("SerialHex(%d) = %q, %v; want %q", in, got, ok, want)
		}
	}
	for _, n := range []*big.Int{nil, big.NewInt(0), big.NewInt(-5)} {
		if _, ok := SerialHex(n); ok {
			t.Errorf("SerialHex(%v) should have no text", n)
		}
	}
}
