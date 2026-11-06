// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/x509trust"
)

// CRL upload, delete and the anchor's revocation settings, served end to end.

const (
	wirePutList = `mutation ($a: String!, $c: String!, $i: String) {
  putDeviceRevocationList(anchorToken: $a, crlPem: $c, issuerChainPem: $i) {
    newlyRevoked unchanged sessionsEnded list { issuerSubject issuerKey issuerName crlNumber thisUpdate nextUpdate entryCount crlSha256 source status } } }`
	wireDeleteList = `mutation ($a: String!, $k: String!, $n: String!) { deleteDeviceRevocationList(anchorToken: $a, issuerKey: $k, issuerName: $n) }`
	wireAnchorRev  = `query ($t: [String!]!) { deviceTrustAnchorsByToken(tokens: $t) {
  token revocationMode revocationStaleGrace revocationStatus revocationLists { issuerKey crlNumber entryCount status } } }`
	wireSetMode = `mutation ($t: String!, $request: DeviceTrustAnchorUpdateRequest!) {
  updateDeviceTrustAnchor(token: $t, request: $request) { token revocationMode revocationStaleGrace revocationStatus } }`
)

func wireCrlPEM(t *testing.T, ca wirePki, number int64, revoked ...*big.Int) string {
	t.Helper()
	tmpl := &x509.RevocationList{Number: big.NewInt(number), ThisUpdate: time.Now().Add(-time.Hour), NextUpdate: time.Now().Add(72 * time.Hour)}
	for _, s := range revoked {
		tmpl.RevokedCertificateEntries = append(tmpl.RevokedCertificateEntries, x509.RevocationListEntry{SerialNumber: s, RevocationTime: time.Now().Add(-time.Hour)})
	}
	der, err := x509.CreateRevocationList(rand.Reader, tmpl, ca.cert, ca.key)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "X509 CRL", Bytes: der}))
}

func TestRevocationListOverTheWire(t *testing.T) {
	f := newTrustAnchorWireFixture(t)
	ctx := f.operator()
	ca := newWirePki(t, nil, "Maker Root", true)
	if made := serve(t, ctx, wireCreateAnchor, map[string]any{"request": map[string]any{"token": "maker", "certificatePem": ca.pem()}}); len(made.errs) > 0 {
		t.Fatalf("create anchor: %v", made.errs[0].Message)
	}
	leaf := newWirePki(t, &ca, "SN-0001", false)

	// An anchor with nothing stored reports the defaults.
	read := serve(t, ctx, wireAnchorRev, map[string]any{"t": []any{"maker"}})
	if len(read.errs) > 0 {
		t.Fatalf("read: %v", read.errs[0].Message)
	}
	a := read.data["deviceTrustAnchorsByToken"].([]any)[0].(map[string]any)
	if a["revocationMode"] != "ENTRIES" || a["revocationStaleGrace"] != "24h0m0s" || a["revocationStatus"] != "NONE" ||
		len(a["revocationLists"].([]any)) != 0 {
		t.Fatalf("defaults = %v", a)
	}

	got := serve(t, ctx, wirePutList, map[string]any{"a": "maker", "c": wireCrlPEM(t, ca, 3, leaf.cert.SerialNumber)})
	if len(got.errs) > 0 {
		t.Fatalf("put: %v", got.errs[0].Message)
	}
	res := got.data["putDeviceRevocationList"].(map[string]any)
	list := res["list"].(map[string]any)
	is := x509trust.IssuerIDOfCA(ca.cert)
	if res["newlyRevoked"] != float64(1) || res["unchanged"] != false || res["sessionsEnded"] != true || list["crlNumber"] != "3" || list["entryCount"] != float64(1) ||
		list["issuerKey"] != is.Key || list["issuerName"] != is.Name || list["source"] != "UPLOAD" || list["status"] != "FRESH" ||
		list["issuerSubject"] != "CN=Maker Root" {
		t.Fatalf("put result = %v", res)
	}

	read = serve(t, ctx, wireAnchorRev, map[string]any{"t": []any{"maker"}})
	a = read.data["deviceTrustAnchorsByToken"].([]any)[0].(map[string]any)
	if a["revocationStatus"] != "FRESH" || len(a["revocationLists"].([]any)) != 1 {
		t.Fatalf("anchor after put = %v", a)
	}
	// The entries are listed through the existing door.
	entries := serve(t, ctx, wireRevokedCerts, map[string]any{"a": "maker", "k": "CRL"})
	if len(entries.errs) > 0 || len(entries.data["deviceRevokedCertificates"].(map[string]any)["results"].([]any)) != 1 {
		t.Fatalf("CRL entries = %v %v", entries.errs, entries.data)
	}

	// Refusals carry the right codes and name the rule.
	rb := serve(t, ctx, wirePutList, map[string]any{"a": "maker", "c": wireCrlPEM(t, ca, 2)})
	if code := errCode(t, rb); code != "INVALID_VALUE" || !strings.Contains(rb.errs[0].Message, model.RuleCrlNumberNotNewer) {
		t.Fatalf("a rollback: %v %v", code, rb.errs[0].Message)
	}
	if code := errCode(t, serve(t, ctx, wirePutList, map[string]any{"a": "maker", "c": "nonsense"})); code != "INVALID_VALUE" {
		t.Fatalf("garbage: %v", code)
	}
	if code := errCode(t, serve(t, ctx, wirePutList, map[string]any{"a": "ghost", "c": wireCrlPEM(t, ca, 9)})); code != "NOT_FOUND" {
		t.Fatalf("unknown anchor: %v", code)
	}
	huge := serve(t, ctx, wirePutList, map[string]any{"a": "maker", "c": strings.Repeat("A", 4<<20)})
	if code := errCode(t, huge); code != "INVALID_VALUE" || !strings.Contains(huge.errs[0].Message, "too_large") {
		t.Fatalf("an oversized CRL: %v %v", code, huge.errs[0].Message)
	}

	// The very same CRL again is a no-op, not a rollback.
	same := wireCrlPEM(t, ca, 4, leaf.cert.SerialNumber)
	if first := serve(t, ctx, wirePutList, map[string]any{"a": "maker", "c": same}); len(first.errs) > 0 {
		t.Fatalf("put 4: %v", first.errs[0].Message)
	}
	again := serve(t, ctx, wirePutList, map[string]any{"a": "maker", "c": same})
	if len(again.errs) > 0 || again.data["putDeviceRevocationList"].(map[string]any)["unchanged"] != true {
		t.Fatalf("re-put of the same CRL: %v %v", again.errs, again.data)
	}

	// Mode and grace on the anchor.
	m := serve(t, ctx, wireSetMode, map[string]any{"t": "maker", "request": map[string]any{
		"revocationMode": "REQUIRE_CURRENT", "revocationStaleGrace": "36h"}})
	if len(m.errs) > 0 {
		t.Fatalf("set mode: %v", m.errs[0].Message)
	}
	u := m.data["updateDeviceTrustAnchor"].(map[string]any)
	if u["revocationMode"] != "REQUIRE_CURRENT" || u["revocationStaleGrace"] != "36h0m0s" || u["revocationStatus"] != "FRESH" {
		t.Fatalf("updated = %v", u)
	}
	if bad := serve(t, ctx, wireSetMode, map[string]any{"t": "maker", "request": map[string]any{"revocationMode": "SOMETIMES"}}); len(bad.errs) == 0 {
		t.Fatal("an unknown mode enum value was accepted")
	}
	if code := errCode(t, serve(t, ctx, wireSetMode, map[string]any{"t": "maker", "request": map[string]any{"revocationMode": nil}})); code != "INVALID_VALUE" {
		t.Fatalf("null mode: %v", code)
	}

	// Delete: the entries go, the list goes, the mark stays.
	del := serve(t, ctx, wireDeleteList, map[string]any{"a": "maker", "k": is.Key, "n": is.Name})
	if len(del.errs) > 0 || del.data["deleteDeviceRevocationList"] != true {
		t.Fatalf("delete: %v %v", del.errs, del.data)
	}
	entries = serve(t, ctx, wireRevokedCerts, map[string]any{"a": "maker", "k": "CRL"})
	if len(entries.data["deviceRevokedCertificates"].(map[string]any)["results"].([]any)) != 0 {
		t.Fatalf("entries after delete = %v", entries.data)
	}
	del = serve(t, ctx, wireDeleteList, map[string]any{"a": "maker", "k": is.Key, "n": is.Name})
	if len(del.errs) > 0 || del.data["deleteDeviceRevocationList"] != false {
		t.Fatalf("a second delete: %v %v", del.errs, del.data)
	}
	if rb := serve(t, ctx, wirePutList, map[string]any{"a": "maker", "c": wireCrlPEM(t, ca, 4)}); len(rb.errs) == 0 {
		t.Fatal("re-uploading the deleted list's number was accepted")
	}
	if code := errCode(t, serve(t, ctx, wireDeleteList, map[string]any{"a": "maker", "k": "zz", "n": "zz"})); code != "INVALID_VALUE" {
		t.Fatalf("malformed issuer id: %v", code)
	}

	// The anchor is deletable although it once held a CRL.
	if d := serve(t, ctx, wireDeleteAnchor, map[string]any{"t": "maker"}); len(d.errs) > 0 || d.data["deleteDeviceTrustAnchor"] != true {
		t.Fatalf("deleting an anchor that held a CRL: %v %v", d.errs, d.data)
	}
}

// Upload and delete need device:write UNSCOPED, like every trust anchor write.
func TestRevocationListWritesNeedUnscopedDeviceWrite(t *testing.T) {
	f := newTrustAnchorWireFixture(t)
	admin := f.operator()
	ca := newWirePki(t, nil, "Maker Root", true)
	serve(t, admin, wireCreateAnchor, map[string]any{"request": map[string]any{"token": "maker", "certificatePem": ca.pem()}})
	if got := serve(t, admin, wirePutList, map[string]any{"a": "maker", "c": wireCrlPEM(t, ca, 1)}); len(got.errs) > 0 {
		t.Fatalf("the control: the operator uploads: %v", got.errs[0].Message)
	}
	scoped := f.withClaims(&auth.Claims{TokenType: auth.TokenTypeAccess, Tenant: scopeTenant,
		Authorities: []string{string(auth.DeviceRead), string(auth.DeviceWrite)}, ScopeAnchors: []string{"customer:acme"}})
	viewer := f.withClaims(&auth.Claims{TokenType: auth.TokenTypeAccess, Tenant: scopeTenant, Authorities: []string{string(auth.DeviceRead)}})
	is := x509trust.IssuerIDOfCA(ca.cert)
	for who, ctx := range map[string]context.Context{"viewer": viewer, "scoped writer": scoped} {
		if got := serve(t, ctx, wirePutList, map[string]any{"a": "maker", "c": wireCrlPEM(t, ca, 5)}); len(got.errs) == 0 {
			t.Errorf("upload was admitted for a %s", who)
		}
		if got := serve(t, ctx, wireDeleteList, map[string]any{"a": "maker", "k": is.Key, "n": is.Name}); len(got.errs) == 0 {
			t.Errorf("delete was admitted for a %s", who)
		}
		if got := serve(t, ctx, wireSetMode, map[string]any{"t": "maker", "request": map[string]any{"revocationStaleGrace": "1h"}}); len(got.errs) == 0 {
			t.Errorf("a grace change was admitted for a %s", who)
		}
	}
}
