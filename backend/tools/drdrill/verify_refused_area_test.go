// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A flag that lets a caller past a premise check is the shape that was deliberately
// removed from this tool once already: --skip-api existed, and because the rig reads
// only the exit code, a caller who passed it still got a decrypt-failure code and was
// still told the control held. --secret-area-refused must not become that flag again.
//
// What keeps it from becoming that is not the comment on it — it is that the mode
// asserts a PAIR, and one half of the pair FAILS on exactly the instance a misuse
// would be pointed at. These tests are that claim, made executable.

// apiStub serves the two GraphQL endpoints verify talks to. loginOK and channelOK say
// whether each one answers; a false channelOK serves 503, which is what an ingress in
// front of an area that refused to start actually returns.
type apiStub struct {
	loginOK    bool
	channelOK  bool
	membership string
	token      string
}

func (a apiStub) start(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/user-management/graphql", func(w http.ResponseWriter, r *http.Request) {
		if !a.loginOK {
			http.Error(w, "no", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{
			"login": map[string]any{
				"identityToken": "t", "expiresAt": "2099-01-01T00:00:00Z", "superuser": false,
				"memberships": []map[string]any{{"tenant": a.membership, "roles": []string{"tenant-admin"}}},
			},
		}})
	})
	mux.HandleFunc("/api/notification-management/graphql", func(w http.ResponseWriter, r *http.Request) {
		if !a.channelOK {
			http.Error(w, "503 Service Temporarily Unavailable", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{
			"notificationChannelsByToken": []map[string]any{{"id": "1", "token": a.token, "hasSecret": true}},
		}})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func refusedOpts(t *testing.T, srv *httptest.Server) verifyOptions {
	t.Helper()
	return verifyOptions{
		server:            strings.TrimPrefix(srv.URL, "http://"),
		scheme:            "http",
		secretAreaRefused: true,
	}
}

func drillReceipt() Receipt {
	return Receipt{
		Instance: "drdrill", Tenant: "drdrill", Schema: "notification_management",
		SecretName: "channel/1/secret", ChannelToken: "drdrill-channel", ChannelID: 1,
		Identity: "drill@example.test", Password: "pw", Secret: "expected",
	}
}

// TestRefusedAreaModeHoldsWhenTheAreaIsDown is the shape the negative control actually
// runs in: user-management serves (it stores no secrets), the secret-storing area does
// not, and the premise is satisfied.
func TestRefusedAreaModeHoldsWhenTheAreaIsDown(t *testing.T) {
	srv := apiStub{loginOK: true, channelOK: false, membership: "drdrill", token: "drdrill-channel"}.start(t)
	if err := checkRestoredWithoutSecretArea(t.Context(), refusedOpts(t, srv), drillReceipt()); err != nil {
		t.Fatalf("the premise must hold when the secret area is down and the relational store restored: %v", err)
	}
}

// 🔴 TestRefusedAreaModeRefusesAHealthyInstance IS THE ANTI-SKIP PROPERTY.
//
// This is the test that stops the flag from being what --skip-api was. Point the mode
// at an instance where the secret-storing area is serving — i.e. its root key DOES open
// the stored ciphertext — and it must refuse to produce a verdict at all. Without this,
// a caller could pass the flag anywhere and read the decrypt result as a control.
func TestRefusedAreaModeRefusesAHealthyInstance(t *testing.T) {
	srv := apiStub{loginOK: true, channelOK: true, membership: "drdrill", token: "drdrill-channel"}.start(t)
	err := checkRestoredWithoutSecretArea(t.Context(), refusedOpts(t, srv), drillReceipt())
	if err == nil {
		t.Fatal("a serving secret area must be refused: the instance did not refuse its root key, " +
			"so nothing this run produces is a negative control. Passing here would restore the " +
			"exact hole --skip-api was removed for")
	}
	if !strings.Contains(err.Error(), "IS serving") {
		t.Fatalf("the refusal must name the serving area as the cause, got %q", err)
	}
}

// TestRefusedAreaModeStillRequiresTheRelationalRestore is the other half of the pair. An
// instance where nothing came back cannot satisfy the mode either, so "the area is down"
// alone is never enough.
func TestRefusedAreaModeStillRequiresTheRelationalRestore(t *testing.T) {
	srv := apiStub{loginOK: false, channelOK: false}.start(t)
	err := checkRestoredWithoutSecretArea(t.Context(), refusedOpts(t, srv), drillReceipt())
	if err == nil {
		t.Fatal("with user-management down too, nothing shows the restore landed; the mode must refuse")
	}
	if !strings.Contains(err.Error(), "logging in") {
		t.Fatalf("the refusal must name the login as the cause, got %q", err)
	}
}

// TestRefusedAreaModeRejectsARestoreOfADifferentInstance pins the membership check. A
// login that succeeds against some OTHER instance's user-management — the wrong cluster,
// a survivor — must not vouch for this receipt's restore.
func TestRefusedAreaModeRejectsARestoreOfADifferentInstance(t *testing.T) {
	srv := apiStub{loginOK: true, channelOK: false, membership: "some-other-tenant"}.start(t)
	err := checkRestoredWithoutSecretArea(t.Context(), refusedOpts(t, srv), drillReceipt())
	if err == nil {
		t.Fatal("an identity with no membership of this run's tenant does not show THIS restore landed")
	}
	if !strings.Contains(err.Error(), "not a member") {
		t.Fatalf("the refusal must name the missing membership, got %q", err)
	}
}

// TestRefusedAreaModeRefusesAnIncompleteReceipt keeps the mode from being reachable with
// no credentials at all, which would make the login half unrunnable and silently absent.
func TestRefusedAreaModeRefusesAnIncompleteReceipt(t *testing.T) {
	srv := apiStub{loginOK: true, channelOK: false, membership: "drdrill"}.start(t)
	r := drillReceipt()
	r.Identity, r.Password = "", ""
	if err := checkRestoredWithoutSecretArea(t.Context(), refusedOpts(t, srv), r); err == nil {
		t.Fatal("a receipt with no identity leaves the positive half unrunnable; the mode must refuse")
	}
}
