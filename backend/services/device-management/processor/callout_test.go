// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-microservice/natsauth"
	"github.com/nats-io/jwt/v2"
	"github.com/nats-io/nkeys"
)

// fakeAuthApi implements just the AuthenticateDevice method of the API interface;
// the embedded (nil) interface satisfies the rest, none of which the responder
// calls. authFn drives the outcome and captures what was presented.
type fakeAuthApi struct {
	model.DeviceManagementApi
	authFn func(ctx context.Context, p *model.PresentedCredential) (*model.Device, error)
}

func (f fakeAuthApi) AuthenticateDevice(ctx context.Context, p *model.PresentedCredential, _ time.Time) (*model.Device, error) {
	return f.authFn(ctx, p)
}

func TestParseDeviceCredential(t *testing.T) {
	cases := []struct {
		name, user, pass string
		wantTenant       string
		wantType         string
		wantSecret       bool // whether Secret should be set
		wantOK           bool
	}{
		{"mqtt basic", "acme-corp:dev1", "s3cret", "acme-corp", string(model.CredentialMqttBasic), true, true},
		{"access token (no password)", "plant_07:tok-abc", "", "plant_07", string(model.CredentialAccessToken), false, true},
		{"no colon", "acme-corp", "x", "", "", false, false},
		{"empty tenant", ":dev1", "x", "", "", false, false},
		{"empty credential", "acme-corp:", "x", "", "", false, false},
		{"empty username", "", "x", "", "", false, false},
		{"credential id may not be split further", "acme:a:b", "", "acme", string(model.CredentialAccessToken), false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tenant, cred, ok := parseDeviceCredential(tc.user, tc.pass)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if !ok {
				return
			}
			if tenant != tc.wantTenant {
				t.Errorf("tenant = %q, want %q", tenant, tc.wantTenant)
			}
			if cred.CredentialType != tc.wantType {
				t.Errorf("type = %q, want %q", cred.CredentialType, tc.wantType)
			}
			if (cred.Secret != nil) != tc.wantSecret {
				t.Errorf("secret set = %v, want %v", cred.Secret != nil, tc.wantSecret)
			}
			// the "a:b" case: credential id keeps everything after the first colon
			if tc.name == "credential id may not be split further" && cred.CredentialId != "a:b" {
				t.Errorf("credential id = %q, want a:b", cred.CredentialId)
			}
		})
	}
}

// newResponder builds a responder with a fresh issuer over a fake API. Returns the
// issuer public key so tests can verify signatures.
func newTestResponder(t *testing.T, authFn func(context.Context, *model.PresentedCredential) (*model.Device, error)) (*CalloutResponder, string) {
	t.Helper()
	creds, err := natsauth.GenerateCredentials()
	if err != nil {
		t.Fatal(err)
	}
	r := NewCalloutResponder(nil, fakeAuthApi{authFn: authFn}, creds.IssuerSeed, "inst-1")
	r.now = func() time.Time { return time.Unix(1_780_000_000, 0) }
	return r, creds.IssuerPublic
}

func testRequest(t *testing.T, user, pass string) jwt.AuthorizationRequest {
	t.Helper()
	ukp, _ := nkeys.CreateUser()
	userNkey, _ := ukp.PublicKey()
	var req jwt.AuthorizationRequest
	req.UserNkey = userNkey
	req.Server.ID = "NTESTSERVER"
	req.ConnectOptions.Username = user
	req.ConnectOptions.Password = pass
	return req
}

// A valid device connect is granted a JWT bound to its own tenant, signed by the
// issuer, with the request's user nkey as subject.
func TestAuthorizeGrant(t *testing.T) {
	var gotCred *model.PresentedCredential
	r, issuerPub := newTestResponder(t, func(_ context.Context, p *model.PresentedCredential) (*model.Device, error) {
		gotCred = p
		// A real authenticated device always has a token — the grant is now built
		// from it, so a fixture returning a tokenless device would be a device that
		// cannot exist, and would test a path production never takes.
		d := &model.Device{}
		d.Token = "sensor-001"
		return d, nil
	})

	req := testRequest(t, "acme-corp:dev1", "s3cret")
	userJWT, errMsg := r.authorize(req)
	if errMsg != "" {
		t.Fatalf("expected grant, got denial %q", errMsg)
	}
	if gotCred == nil || gotCred.CredentialType != string(model.CredentialMqttBasic) {
		t.Fatalf("expected MQTT_BASIC presented, got %+v", gotCred)
	}

	uc, err := jwt.DecodeUserClaims(userJWT)
	if err != nil {
		t.Fatalf("decode granted jwt: %v", err)
	}
	if uc.Subject != req.UserNkey {
		t.Errorf("subject = %q, want the request user nkey %q", uc.Subject, req.UserNkey)
	}
	if uc.Issuer != issuerPub {
		t.Errorf("issuer = %q, want %q", uc.Issuer, issuerPub)
	}
	if uc.Audience != natsauth.AppAccount {
		t.Errorf("audience = %q, want %q", uc.Audience, natsauth.AppAccount)
	}
	// The grant is per-device, not tenant-wide: the device may publish its own events
	// and command responses, and nothing else.
	want := []string{
		"inst-1.acme-corp.devices.sensor-001.events",
		"inst-1.acme-corp.command-responses",
	}
	if got := []string(uc.Permissions.Pub.Allow); !slices.Equal(got, want) {
		t.Errorf("pub allow = %v, want %v", got, want)
	}
}

// The granted JWT is confined to the AUTHENTICATED DEVICE, not its tenant: it may
// subscribe to its own command subject and nothing that reaches another device's.
func TestAuthorizeGrantIsPerDevice(t *testing.T) {
	r, _ := newTestResponder(t, func(_ context.Context, _ *model.PresentedCredential) (*model.Device, error) {
		d := &model.Device{}
		d.Token = "sensor-001"
		return d, nil
	})

	userJWT, errMsg := r.authorize(testRequest(t, "acme-corp:dev1", "s3cret"))
	if errMsg != "" {
		t.Fatalf("expected grant, got denial %q", errMsg)
	}
	uc, err := jwt.DecodeUserClaims(userJWT)
	if err != nil {
		t.Fatalf("decode user claims: %v", err)
	}

	var ownCommands bool
	for _, granted := range uc.Permissions.Sub.Allow {
		if granted == "inst-1.acme-corp.device-commands.sensor-001" {
			ownCommands = true
		}
		// A grant that reaches another device by wildcard is the failure that
		// matters; comparing literals alone would not see it.
		if strings.HasSuffix(granted, ">") && strings.HasPrefix(granted, "inst-1.acme-corp") {
			t.Errorf("sub grant %q is a tenant-wide wildcard; it reaches other devices", granted)
		}
	}
	if !ownCommands {
		t.Errorf("sub allow %v is missing this device's own command subject",
			[]string(uc.Permissions.Sub.Allow))
	}
}

// A credential that authenticates but resolves to no device token cannot be given a
// grant at all: the only grant expressible without a device would be the tenant-wide
// one this change removes, so it fails closed instead.
func TestAuthorizeDeniesDeviceWithoutToken(t *testing.T) {
	r, _ := newTestResponder(t, func(_ context.Context, _ *model.PresentedCredential) (*model.Device, error) {
		return &model.Device{}, nil
	})

	userJWT, errMsg := r.authorize(testRequest(t, "acme-corp:dev1", "s3cret"))
	if errMsg == "" {
		t.Fatalf("a tokenless device must be denied, got a grant: %q", userJWT)
	}
}

// Malformed usernames and failed authentication both deny with the same generic
// message (no oracle), and a malformed username never reaches AuthenticateDevice.
func TestAuthorizeDeny(t *testing.T) {
	t.Run("malformed username short-circuits", func(t *testing.T) {
		called := false
		r, _ := newTestResponder(t, func(context.Context, *model.PresentedCredential) (*model.Device, error) {
			called = true
			return &model.Device{}, nil
		})
		jwt, errMsg := r.authorize(testRequest(t, "no-colon", "x"))
		if jwt != "" || errMsg != genericAuthFailure {
			t.Errorf("expected generic denial, got jwt=%q err=%q", jwt, errMsg)
		}
		if called {
			t.Error("AuthenticateDevice should not be called for a malformed username")
		}
	})

	t.Run("auth failure denies generically", func(t *testing.T) {
		r, _ := newTestResponder(t, func(context.Context, *model.PresentedCredential) (*model.Device, error) {
			return nil, errors.New("credential did not resolve")
		})
		token, errMsg := r.authorize(testRequest(t, "acme-corp:dev1", "s3cret"))
		if token != "" || errMsg != genericAuthFailure {
			t.Errorf("expected generic denial, got jwt=%q err=%q", token, errMsg)
		}
	})
}
