// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package natsauth

import (
	"testing"
	"time"

	"github.com/nats-io/jwt/v2"
	"github.com/nats-io/nkeys"
	"golang.org/x/crypto/bcrypt"
)

// GenerateCredentials mints a usable account issuer keypair (public A..., seed
// SA... that round-trips) and a non-empty service password, all distinct per call.
func TestGenerateCredentials(t *testing.T) {
	c, err := GenerateCredentials()
	if err != nil {
		t.Fatalf("GenerateCredentials: %v", err)
	}
	if c.IssuerPublic == "" || c.IssuerPublic[0] != 'A' {
		t.Errorf("issuer public should be an account key (A...), got %q", c.IssuerPublic)
	}
	kp, err := nkeys.FromSeed([]byte(c.IssuerSeed))
	if err != nil {
		t.Fatalf("issuer seed does not load: %v", err)
	}
	pub, _ := kp.PublicKey()
	if pub != c.IssuerPublic {
		t.Errorf("seed public %q != IssuerPublic %q", pub, c.IssuerPublic)
	}
	if len(c.ServicePassword) < 32 {
		t.Errorf("service password too short: %d chars", len(c.ServicePassword))
	}

	// The bcrypt hash placed in the broker config must verify against the plaintext
	// the services present — the whole point of the split. This also pins that we
	// hash the plaintext (not something else) and that the two never drift.
	if err := bcrypt.CompareHashAndPassword([]byte(c.ServicePasswordBcrypt), []byte(c.ServicePassword)); err != nil {
		t.Errorf("bcrypt hash does not verify against the plaintext service password: %v", err)
	}
	// It is a hash, not the plaintext, and carries the bcrypt identifier.
	if c.ServicePasswordBcrypt == c.ServicePassword {
		t.Error("bcrypt field must not equal the plaintext password")
	}
	if len(c.ServicePasswordBcrypt) < 4 || c.ServicePasswordBcrypt[:4] != "$2a$" {
		t.Errorf("bcrypt hash should start with $2a$, got %q", c.ServicePasswordBcrypt)
	}

	c2, _ := GenerateCredentials()
	if c2.IssuerSeed == c.IssuerSeed || c2.ServicePassword == c.ServicePassword {
		t.Error("two generations should not collide")
	}
}

// DevicePermissions confines a device to its own tenant tree for publish, and for
// subscribe additionally allows the level-up subject (MQTT `#` filter mapping) and
// the MQTT QoS-1 delivery subject — but nothing cross-tenant.
func TestDevicePermissions(t *testing.T) {
	p := DevicePermissions("acme-corp")
	if got := []string(p.Pub.Allow); len(got) != 1 || got[0] != "dc.acme-corp.>" {
		t.Errorf("pub allow = %v, want [dc.acme-corp.>]", got)
	}
	sub := map[string]bool{}
	for _, s := range p.Sub.Allow {
		sub[s] = true
	}
	for _, want := range []string{"dc.acme-corp.>", "dc.acme-corp", MqttDeliverySubject} {
		if !sub[want] {
			t.Errorf("sub allow %v missing %q", []string(p.Sub.Allow), want)
		}
	}
	// No other tenant's tree, and the MQTT delivery subject is the shared internal
	// one (guarded by the MQTT connection-type pin) — no `dc.*` wildcard.
	for _, s := range p.Sub.Allow {
		if s == "dc.>" || s == ">" {
			t.Errorf("sub allow %q is too broad", s)
		}
	}
	if len(p.Pub.Deny) != 0 || len(p.Sub.Deny) != 0 {
		t.Error("no deny rules expected")
	}
}

// A signed device user JWT decodes, verifies against the issuer, and carries the
// right subject, target account, tenant-scoped permissions, and expiry.
func TestSignDeviceUserJWT(t *testing.T) {
	c, _ := GenerateCredentials()
	// The server generates the user nkey; simulate it.
	ukp, _ := nkeys.CreateUser()
	userNkey, _ := ukp.PublicKey()
	now := time.Unix(1_780_000_000, 0)

	token, err := SignDeviceUserJWT(c.IssuerSeed, userNkey, "plant_07", now, DefaultUserJWTTTL)
	if err != nil {
		t.Fatalf("SignDeviceUserJWT: %v", err)
	}

	uc, err := jwt.DecodeUserClaims(token)
	if err != nil {
		t.Fatalf("decode user claims: %v", err)
	}
	if uc.Subject != userNkey {
		t.Errorf("subject = %q, want the server user nkey %q", uc.Subject, userNkey)
	}
	if uc.Issuer != c.IssuerPublic {
		t.Errorf("issuer = %q, want %q", uc.Issuer, c.IssuerPublic)
	}
	if uc.Audience != AppAccount {
		t.Errorf("audience = %q, want %q", uc.Audience, AppAccount)
	}
	if got := []string(uc.Permissions.Pub.Allow); len(got) != 1 || got[0] != "dc.plant_07.>" {
		t.Errorf("pub allow = %v, want [dc.plant_07.>]", got)
	}
	if got := []string(uc.AllowedConnectionTypes); len(got) != 1 || got[0] != jwt.ConnectionTypeMqtt {
		t.Errorf("allowed connection types = %v, want [MQTT] (device creds must be MQTT-only)", got)
	}
	if uc.Expires != now.Add(DefaultUserJWTTTL).Unix() {
		t.Errorf("expiry = %d, want %d", uc.Expires, now.Add(DefaultUserJWTTTL).Unix())
	}
	// DecodeUserClaims already verified the signature; check the claim is
	// structurally valid too (IsBlocking(false) skips time checks — the fixed test
	// clock is in the past relative to real now).
	vr := new(jwt.ValidationResults)
	uc.Validate(vr)
	if vr.IsBlocking(false) {
		t.Errorf("user claims failed validation: %v", vr.Errors())
	}
}

// The auth response carries the server id as audience (the server rejects it
// otherwise), the user nkey as subject, and the minted JWT on success / the error
// string on denial — signed by the issuer.
func TestEncodeAuthResponse(t *testing.T) {
	c, _ := GenerateCredentials()
	ukp, _ := nkeys.CreateUser()
	userNkey, _ := ukp.PublicKey()
	const serverID = "NDHEXAMPLESERVERID"

	// success carries the jwt
	resp, err := EncodeAuthResponse(c.IssuerSeed, serverID, userNkey, "the.user.jwt", "")
	if err != nil {
		t.Fatalf("EncodeAuthResponse: %v", err)
	}
	rc, err := jwt.DecodeAuthorizationResponseClaims(resp)
	if err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if rc.Subject != userNkey {
		t.Errorf("subject = %q, want %q", rc.Subject, userNkey)
	}
	if rc.Audience != serverID {
		t.Errorf("audience = %q, want the server id %q", rc.Audience, serverID)
	}
	if rc.Jwt != "the.user.jwt" || rc.Error != "" {
		t.Errorf("expected jwt set / error empty, got jwt=%q error=%q", rc.Jwt, rc.Error)
	}
	if rc.Issuer != c.IssuerPublic {
		t.Errorf("response issuer = %q, want %q", rc.Issuer, c.IssuerPublic)
	}

	// denial carries the error, no jwt
	den, _ := EncodeAuthResponse(c.IssuerSeed, serverID, userNkey, "", "invalid credentials")
	drc, _ := jwt.DecodeAuthorizationResponseClaims(den)
	if drc.Error != "invalid credentials" || drc.Jwt != "" {
		t.Errorf("expected error set / jwt empty, got jwt=%q error=%q", drc.Jwt, drc.Error)
	}
}
