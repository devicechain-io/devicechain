// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package natsauth

import (
	"slices"
	"strings"
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

// DevicePermissions confines a device to ITS OWN subjects — not its tenant's. The
// grant is the only place these properties can be enforced against a device that
// misbehaves on purpose, so each is asserted as a capability, not as a string shape.
func TestDevicePermissions(t *testing.T) {
	p, err := DevicePermissions("inst-1", "acme-corp", "sensor-001")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	pub := map[string]bool{}
	for _, s := range p.Pub.Allow {
		pub[s] = true
	}
	sub := map[string]bool{}
	for _, s := range p.Sub.Allow {
		sub[s] = true
	}

	// What the device MUST be able to do.
	for _, want := range []string{
		"inst-1.acme-corp.devices.sensor-001.events",
		"inst-1.acme-corp.command-responses",
	} {
		if !pub[want] {
			t.Errorf("pub allow %v missing %q", []string(p.Pub.Allow), want)
		}
	}
	for _, want := range []string{
		"inst-1.acme-corp.device-commands.sensor-001",
		MqttDeliverySubject,
	} {
		if !sub[want] {
			t.Errorf("sub allow %v missing %q", []string(p.Sub.Allow), want)
		}
	}

	// The point of the change: another device's commands are unreachable. Asserting
	// the absence of a literal is not enough — a surviving wildcard would still
	// cover it — so check that NOTHING granted matches the other device's subject.
	other := "inst-1.acme-corp.device-commands.sensor-002"
	for _, granted := range p.Sub.Allow {
		if natsSubjectMatches(granted, other) {
			t.Errorf("sub grant %q reaches another device's commands (%q)", granted, other)
		}
	}

	// Likewise the device must not be able to publish as another device, or onto an
	// arbitrary platform topic.
	for _, forbidden := range []string{
		"inst-1.acme-corp.devices.sensor-002.events",
		"inst-1.acme-corp.device-commands.sensor-001",
		"inst-1.acme-corp.anything-else",
	} {
		for _, granted := range p.Pub.Allow {
			if natsSubjectMatches(granted, forbidden) {
				t.Errorf("pub grant %q reaches %q", granted, forbidden)
			}
		}
	}

	// Cross-tenant and cross-instance stay closed (ADR-025 / ADR-048).
	for _, forbidden := range []string{
		"inst-1.other-tenant.device-commands.sensor-001",
		"inst-2.acme-corp.device-commands.sensor-001",
	} {
		for _, granted := range append(append([]string{}, p.Sub.Allow...), p.Pub.Allow...) {
			if natsSubjectMatches(granted, forbidden) {
				t.Errorf("grant %q crosses a boundary to %q", granted, forbidden)
			}
		}
	}

	if len(p.Pub.Deny) != 0 || len(p.Sub.Deny) != 0 {
		t.Error("no deny rules expected")
	}
}

// natsSubjectMatches implements NATS subject matching ("*" = one token, ">" = one
// or more trailing tokens) so the assertions above test REACHABILITY rather than
// string equality — a grant that reaches a subject by wildcard is the failure mode
// that matters, and comparing literals would miss it entirely.
func natsSubjectMatches(pattern, subject string) bool {
	p := strings.Split(pattern, ".")
	sub := strings.Split(subject, ".")
	for i, tok := range p {
		if tok == ">" {
			return i < len(sub)
		}
		if i >= len(sub) {
			return false
		}
		if tok != "*" && tok != sub[i] {
			return false
		}
	}
	return len(p) == len(sub)
}

// A signed device user JWT decodes, verifies against the issuer, and carries the
// right subject, target account, tenant-scoped permissions, and expiry.
func TestSignDeviceUserJWT(t *testing.T) {
	c, _ := GenerateCredentials()
	// The server generates the user nkey; simulate it.
	ukp, _ := nkeys.CreateUser()
	userNkey, _ := ukp.PublicKey()
	now := time.Unix(1_780_000_000, 0)

	token, err := SignDeviceUserJWT(c.IssuerSeed, userNkey, "inst-1", "plant_07", "sensor-001", now, DefaultUserJWTTTL)
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
	// The signed claim must carry the SAME per-device grant DevicePermissions builds,
	// so a device's JWT cannot be broader than the policy. Checked by reachability:
	// nothing in the signed grant may reach another device's command subject.
	if got := []string(uc.Permissions.Pub.Allow); len(got) != 2 {
		t.Errorf("pub allow = %v, want the device's events topic and command-responses", got)
	}
	for _, granted := range uc.Permissions.Sub.Allow {
		if natsSubjectMatches(granted, "inst-1.plant_07.device-commands.other-device") {
			t.Errorf("signed sub grant %q reaches another device's commands", granted)
		}
	}
	if !slices.Contains(uc.Permissions.Sub.Allow, "inst-1.plant_07.device-commands.sensor-001") {
		t.Errorf("sub allow = %v, missing this device's own command subject",
			[]string(uc.Permissions.Sub.Allow))
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

// A device token carrying subject metacharacters would mint live wildcards inside a
// device's own signed grant — "x.*" yields SUB "…device-commands.x.*", reaching every
// device whose token starts with "x.". The DB grammar guard should make such a token
// unreachable, but the grant is the higher-blast-radius splice and gets its own check.
func TestDevicePermissionsRejectsUnsafeDeviceToken(t *testing.T) {
	for _, bad := range []string{"x.*", "x.>", "*", ">", "a.b", ""} {
		if _, err := DevicePermissions("inst-1", "acme-corp", bad); err == nil {
			t.Errorf("device token %q must be refused, not spliced into a grant", bad)
		}
	}
}
