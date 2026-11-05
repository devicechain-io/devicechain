// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"testing"

	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/x509trust"
)

// Manual certificate revocation, served end to end.

const (
	wireRevokeCert = `mutation ($a: String!, $c: String, $f: String, $r: CertificateRevocationReason) {
  revokeDeviceCertificate(anchorToken: $a, certificatePem: $c, fingerprintSha256: $f, reason: $r) {
    kind fingerprintSha256 issuerKey serial reason revokedAt } }`
	wireUnrevokeCert = `mutation ($a: String!, $f: String!) { unrevokeDeviceCertificate(anchorToken: $a, fingerprintSha256: $f) }`
	wireRevokedCerts = `query ($a: String!, $k: String) { deviceRevokedCertificates(anchorToken: $a,
  criteria: {pageNumber: 1, pageSize: 10, kind: $k}) {
    results { kind fingerprintSha256 reason revokedAt } pagination { totalRecords } } }`
)

func TestManualRevocationOverTheWire(t *testing.T) {
	f := newTrustAnchorWireFixture(t)
	ctx := f.operator()
	ca := newWirePki(t, nil, "Maker Root", true)
	if made := serve(t, ctx, wireCreateAnchor, map[string]any{"request": map[string]any{"token": "maker", "certificatePem": ca.pem()}}); len(made.errs) > 0 {
		t.Fatalf("create anchor: %v", made.errs[0].Message)
	}
	leaf := newWirePki(t, &ca, "SN-0001", false)
	fp := x509trust.Fingerprint(leaf.cert)

	// By certificate, with a reason: the fingerprint is computed and the enum round-trips.
	got := serve(t, ctx, wireRevokeCert, map[string]any{"a": "maker", "c": leaf.pem(), "r": "KEY_COMPROMISE"})
	if len(got.errs) > 0 {
		t.Fatalf("revoke: %v", got.errs[0].Message)
	}
	row := got.data["revokeDeviceCertificate"].(map[string]any)
	if row["kind"] != "MANUAL" || row["fingerprintSha256"] != fp || row["reason"] != "KEY_COMPROMISE" || row["issuerKey"] != nil || row["serial"] != nil {
		t.Fatalf("revoked = %v", row)
	}

	// By fingerprint, no reason: UNSPECIFIED. And again for the same one: the existing entry.
	other := newWirePki(t, &ca, "SN-0002", false)
	ofp := x509trust.Fingerprint(other.cert)
	got = serve(t, ctx, wireRevokeCert, map[string]any{"a": "maker", "f": ofp})
	if len(got.errs) > 0 || got.data["revokeDeviceCertificate"].(map[string]any)["reason"] != "UNSPECIFIED" {
		t.Fatalf("revoke by fingerprint: %v %v", got.errs, got.data)
	}

	list := serve(t, ctx, wireRevokedCerts, map[string]any{"a": "maker"})
	if len(list.errs) > 0 {
		t.Fatalf("list: %v", list.errs[0].Message)
	}
	page := list.data["deviceRevokedCertificates"].(map[string]any)
	if page["pagination"].(map[string]any)["totalRecords"] != float64(2) || len(page["results"].([]any)) != 2 {
		t.Fatalf("page = %v", page)
	}
	crl := serve(t, ctx, wireRevokedCerts, map[string]any{"a": "maker", "k": "CRL"})
	if len(crl.errs) > 0 || len(crl.data["deviceRevokedCertificates"].(map[string]any)["results"].([]any)) != 0 {
		t.Fatalf("a CRL-only listing: %v %v", crl.errs, crl.data)
	}

	// Refusals carry the right codes.
	if code := errCode(t, serve(t, ctx, wireRevokeCert, map[string]any{"a": "maker"})); code != "INVALID_VALUE" {
		t.Fatalf("neither certificate nor fingerprint: extensions.code = %v", code)
	}
	if code := errCode(t, serve(t, ctx, wireRevokeCert, map[string]any{"a": "maker", "f": x509trust.Fingerprint(ca.cert)})); code != "INVALID_VALUE" {
		t.Fatalf("the anchor's own certificate: extensions.code = %v", code)
	}
	if bad := serve(t, ctx, wireRevokeCert, map[string]any{"a": "maker", "f": fp, "r": "BECAUSE"}); len(bad.errs) == 0 {
		t.Fatal("an unknown reason enum value was accepted")
	}
	if code := errCode(t, serve(t, ctx, wireRevokeCert, map[string]any{"a": "nope", "f": fp})); code != "NOT_FOUND" {
		t.Fatalf("an unknown anchor: extensions.code = %v", code)
	}

	// An anchor holding revocations is not deleted from under them.
	if code := errCode(t, serve(t, ctx, wireDeleteAnchor, map[string]any{"t": "maker"})); code != "REFERENCE_VIOLATION" {
		t.Fatalf("deleting an anchor that holds revocations: extensions.code = %v", code)
	}

	un := serve(t, ctx, wireUnrevokeCert, map[string]any{"a": "maker", "f": fp})
	if len(un.errs) > 0 || un.data["unrevokeDeviceCertificate"] != true {
		t.Fatalf("unrevoke: %v %v", un.errs, un.data)
	}
	again := serve(t, ctx, wireUnrevokeCert, map[string]any{"a": "maker", "f": fp})
	if len(again.errs) > 0 || again.data["unrevokeDeviceCertificate"] != false {
		t.Fatalf("a second unrevoke must report false: %v %v", again.errs, again.data)
	}
}

// Revoking needs device:write UNSCOPED and listing needs device:read UNSCOPED, like every trust
// anchor operation: a viewer is refused the writes and a customer-scoped member everything.
func TestManualRevocationNeedsUnscopedDeviceAuthority(t *testing.T) {
	f := newTrustAnchorWireFixture(t)
	admin := f.operator()
	ca := newWirePki(t, nil, "Maker Root", true)
	serve(t, admin, wireCreateAnchor, map[string]any{"request": map[string]any{"token": "maker", "certificatePem": ca.pem()}})
	fp := x509trust.Fingerprint(newWirePki(t, &ca, "SN-1", false).cert)
	if got := serve(t, admin, wireRevokeCert, map[string]any{"a": "maker", "f": fp}); len(got.errs) > 0 {
		t.Fatalf("the control: the operator revokes: %v", got.errs[0].Message)
	}

	scopedWriter := f.withClaims(&auth.Claims{TokenType: auth.TokenTypeAccess, Tenant: scopeTenant,
		Authorities: []string{string(auth.DeviceRead), string(auth.DeviceWrite)}, ScopeAnchors: []string{"customer:acme"}})
	viewer := f.withClaims(&auth.Claims{TokenType: auth.TokenTypeAccess, Tenant: scopeTenant,
		Authorities: []string{string(auth.DeviceRead)}})

	fp2 := x509trust.Fingerprint(newWirePki(t, &ca, "SN-2", false).cert)
	for who, ctx := range map[string]context.Context{"viewer": viewer, "scoped writer": scopedWriter} {
		if got := serve(t, ctx, wireRevokeCert, map[string]any{"a": "maker", "f": fp2}); len(got.errs) == 0 {
			t.Errorf("revoke was admitted for a %s", who)
		}
		if got := serve(t, ctx, wireUnrevokeCert, map[string]any{"a": "maker", "f": fp}); len(got.errs) == 0 {
			t.Errorf("unrevoke was admitted for a %s", who)
		}
	}
	if got := serve(t, scopedWriter, wireRevokedCerts, map[string]any{"a": "maker"}); len(got.errs) == 0 {
		t.Error("the listing was admitted for a scoped member")
	}
	if got := serve(t, viewer, wireRevokedCerts, map[string]any{"a": "maker"}); len(got.errs) > 0 {
		t.Errorf("the control: the listing was refused for a viewer: %v", got.errs[0].Message)
	}
	rows, err := f.api.DeviceRevokedCertificates(admin, "maker", modelCriteria())
	if err != nil || len(rows.Results) != 1 {
		t.Fatalf("a refused write changed the store: %v %v", rows, err)
	}
}

func modelCriteria() model.DeviceRevokedCertificateSearchCriteria {
	return model.DeviceRevokedCertificateSearchCriteria{}
}
