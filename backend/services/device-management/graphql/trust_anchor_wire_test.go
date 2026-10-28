// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"testing"
	"time"

	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const wireCreateCredential = `mutation ($request: DeviceCredentialCreateRequest) {
  createDeviceCredential(request: $request) { token } }`

// The trust-anchor and certificate-credential surface, served end to end.

const (
	wireCreateAnchor = `mutation ($request: DeviceTrustAnchorCreateRequest!) {
  createDeviceTrustAnchor(request: $request) { token fingerprintSha256 subject enabled credentialCount profileCount } }`
	wireUpdateAnchor = `mutation ($t: String!, $request: DeviceTrustAnchorUpdateRequest!) {
  updateDeviceTrustAnchor(token: $t, request: $request) { token enabled } }`
	wireDeleteAnchor = `mutation ($t: String!) { deleteDeviceTrustAnchor(token: $t) }`
	wireReadAnchors  = `query { deviceTrustAnchors(criteria: {pageNumber: 1, pageSize: 10}) {
  results { token enabled credentialCount profileCount } pagination { totalRecords } } }`
	wireReadAnchorByToken = `query ($t: [String!]!) { deviceTrustAnchorsByToken(tokens: $t) { token } }`
	wireReadCert          = `query ($t: [String!]!) { deviceCredentialsByToken(tokens: $t) {
  token credentialType credentialId expiresAt
  certificate { fingerprintSha256 subjectCn serialHex notAfter trustAnchor { token } } } }`
)

type wirePki struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
}

func (p wirePki) pem() string {
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: p.cert.Raw}))
}

func newWirePki(t *testing.T, parent *wirePki, cn string, ca bool) wirePki {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: cn},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
	}
	if ca {
		tmpl.IsCA, tmpl.BasicConstraintsValid, tmpl.MaxPathLen = true, true, -1
		tmpl.KeyUsage = x509.KeyUsageCertSign
	} else {
		tmpl.KeyUsage = x509.KeyUsageDigitalSignature
		tmpl.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}
	}
	issuer, signer := tmpl, key
	if parent != nil {
		issuer, signer = parent.cert, parent.key
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, issuer, &key.PublicKey, signer)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return wirePki{cert: cert, key: key}
}

func newTrustAnchorWireFixture(t *testing.T) scopeFixture {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	if err := rdb.RegisterTenantScoping(db); err != nil {
		t.Fatal(err)
	}
	return seedScopeFixture(t, db, &model.ProvisioningProfile{}, &model.DeviceCredential{},
		&model.DeviceTrustAnchor{}, &model.PendingSessionKick{})
}

func errCode(t *testing.T, s served) any {
	t.Helper()
	if len(s.errs) != 1 {
		t.Fatalf("want exactly one error, got %v", s.errs)
	}
	return s.errs[0].Extensions["code"]
}

func TestTrustAnchorAndCertificateCredentialOverTheWire(t *testing.T) {
	f := newTrustAnchorWireFixture(t)
	ctx := f.operator()
	ca := newWirePki(t, nil, "Maker Root", true)

	created := serve(t, ctx, wireCreateAnchor, map[string]any{"request": map[string]any{"token": "maker", "certificatePem": ca.pem()}})
	if len(created.errs) > 0 {
		t.Fatalf("create anchor: %v", created.errs[0].Message)
	}
	anchor := created.data["createDeviceTrustAnchor"].(map[string]any)
	if anchor["enabled"] != true || anchor["subject"] != "CN=Maker Root" || anchor["credentialCount"] != float64(0) {
		t.Fatalf("anchor = %v", anchor)
	}

	// A device certificate credential is created with the id left empty (it is computed).
	leaf := newWirePki(t, &ca, "SN-0001", false)
	made := serve(t, ctx, wireCreateCredential, map[string]any{"request": map[string]any{
		"token": "c1", "deviceToken": "acme-pump", "credentialType": "X509_CERTIFICATE", "credentialId": "",
		"enabled": true, "certificatePem": leaf.pem(), "trustAnchorToken": "maker"}})
	if len(made.errs) > 0 {
		t.Fatalf("create credential: %v", made.errs[0].Message)
	}
	read := serve(t, ctx, wireReadCert, map[string]any{"t": []any{"c1"}})
	if len(read.errs) > 0 {
		t.Fatalf("read credential: %v", read.errs[0].Message)
	}
	cred := read.data["deviceCredentialsByToken"].([]any)[0].(map[string]any)
	cert := cred["certificate"].(map[string]any)
	if cert["subjectCn"] != "SN-0001" || cert["fingerprintSha256"] != cred["credentialId"] ||
		cert["trustAnchor"].(map[string]any)["token"] != "maker" {
		t.Fatalf("certificate = %v", cert)
	}

	// The anchor now reports what relies on it, and refuses to be deleted.
	list := serve(t, ctx, wireReadAnchors, nil)
	if len(list.errs) > 0 {
		t.Fatalf("list: %v", list.errs[0].Message)
	}
	row := list.data["deviceTrustAnchors"].(map[string]any)["results"].([]any)[0].(map[string]any)
	if row["credentialCount"] != float64(1) {
		t.Fatalf("anchor row = %v", row)
	}
	if got := errCode(t, serve(t, ctx, wireDeleteAnchor, map[string]any{"t": "maker"})); got != "REFERENCE_VIOLATION" {
		t.Fatalf("deleting an anchor in use: extensions.code = %v", got)
	}

	// A chain that does not reach the anchor is INVALID_VALUE, and stores nothing.
	other := newWirePki(t, nil, "Other Root", true)
	bad := serve(t, ctx, wireCreateCredential, map[string]any{"request": map[string]any{
		"token": "c2", "deviceToken": "acme-pump", "credentialType": "X509_CERTIFICATE", "credentialId": "",
		"enabled": true, "certificatePem": newWirePki(t, &other, "SN-0002", false).pem(), "trustAnchorToken": "maker"}})
	if got := errCode(t, bad); got != "INVALID_VALUE" {
		t.Fatalf("a foreign chain: extensions.code = %v", got)
	}

	// A certificate profile has no secret on the wire.
	prof := serve(t, ctx, `mutation ($request: ProvisioningProfileCreateRequest) { createProvisioningProfile(request: $request) {
	  issuedSecret profile { token secretIssued credentialType trustAnchor { token } } } }`,
		createProfileVars("pp-x", map[string]any{"trustAnchorToken": "maker"}))
	if len(prof.errs) > 0 {
		t.Fatalf("create certificate profile: %v", prof.errs[0].Message)
	}
	res := prof.data["createProvisioningProfile"].(map[string]any)
	p := res["profile"].(map[string]any)
	if res["issuedSecret"] != nil || p["secretIssued"] != false || p["credentialType"] != "X509_CERTIFICATE" ||
		p["trustAnchor"].(map[string]any)["token"] != "maker" {
		t.Fatalf("certificate profile = %v", res)
	}

	// Disabling the anchor is a plain update on the wire.
	off := serve(t, ctx, wireUpdateAnchor, map[string]any{"t": "maker", "request": map[string]any{"enabled": false}})
	if len(off.errs) > 0 || off.data["updateDeviceTrustAnchor"].(map[string]any)["enabled"] != false {
		t.Fatalf("disable: %v %v", off.errs, off.data)
	}
}

// Every write needs device:write UNSCOPED, and every read needs device:read UNSCOPED: a viewer
// is refused the writes, and a customer-scoped member is refused all of them, whatever
// authorities it holds.
func TestTrustAnchorOperationsNeedUnscopedDeviceAuthority(t *testing.T) {
	f := newTrustAnchorWireFixture(t)
	admin := f.operator()
	ca := newWirePki(t, nil, "Maker Root", true)
	serve(t, admin, wireCreateAnchor, map[string]any{"request": map[string]any{"token": "maker", "certificatePem": ca.pem()}})

	scopedWriter := f.withClaims(&auth.Claims{TokenType: auth.TokenTypeAccess, Tenant: scopeTenant,
		Authorities: []string{string(auth.DeviceRead), string(auth.DeviceWrite)}, ScopeAnchors: []string{"customer:acme"}})
	viewer := f.withClaims(&auth.Claims{TokenType: auth.TokenTypeAccess, Tenant: scopeTenant,
		Authorities: []string{string(auth.DeviceRead)}})

	other := newWirePki(t, nil, "Other Root", true)
	writes := map[string]struct {
		query string
		vars  map[string]any
	}{
		"create": {wireCreateAnchor, map[string]any{"request": map[string]any{"token": "x", "certificatePem": other.pem()}}},
		"update": {wireUpdateAnchor, map[string]any{"t": "maker", "request": map[string]any{"enabled": false}}},
		"delete": {wireDeleteAnchor, map[string]any{"t": "maker"}},
	}
	for name, call := range writes {
		for who, ctx := range map[string]context.Context{"viewer": viewer, "scoped writer": scopedWriter} {
			if got := serve(t, ctx, call.query, call.vars); len(got.errs) == 0 {
				t.Errorf("%s was admitted for a %s", name, who)
			}
		}
	}
	if rows, err := f.api.DeviceTrustAnchorsByToken(admin, []string{"x"}); err != nil || len(rows) != 0 {
		t.Fatalf("a refused create stored an anchor: %v %v", rows, err)
	}
	if rows, _ := f.api.DeviceTrustAnchorsByToken(admin, []string{"maker"}); len(rows) != 1 || !rows[0].Enabled {
		t.Fatal("a refused update or delete changed the anchor")
	}

	reads := map[string]struct {
		query string
		vars  map[string]any
	}{
		"list":    {wireReadAnchors, nil},
		"byToken": {wireReadAnchorByToken, map[string]any{"t": []any{"maker"}}},
	}
	for name, call := range reads {
		if got := serve(t, scopedWriter, call.query, call.vars); len(got.errs) == 0 {
			t.Errorf("read %s was admitted for a scoped member", name)
		}
		if got := serve(t, viewer, call.query, call.vars); len(got.errs) > 0 {
			t.Errorf("the control: read %s was refused for a viewer: %v", name, got.errs[0].Message)
		}
	}
}

// A misnamed input field is a request error, not a half-configured anchor.
func TestTrustAnchorInputRejectsUnknownFields(t *testing.T) {
	f := newTrustAnchorWireFixture(t)
	ctx := f.operator()
	ca := newWirePki(t, nil, "Maker Root", true)
	bad := serve(t, ctx, wireCreateAnchor, map[string]any{"request": map[string]any{
		"token": "maker", "certificate": ca.pem()}})
	if len(bad.errs) == 0 {
		t.Fatal("createDeviceTrustAnchor accepted a misnamed certificate field")
	}
}
