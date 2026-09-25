// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package loadtest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/userclient"
	"github.com/devicechain-io/dc-simulator/sim"
)

// The token a monitored run dials with has to outlive every phase the socket is held for,
// plus the margin.
func TestMonitorLifetimeCoversTheWholeRun(t *testing.T) {
	for _, hold := range []time.Duration{30 * time.Second, 10 * time.Minute, time.Hour} {
		p := Profile{Hold: hold}
		if got, want := monitorLifetimeNeeded(p), hold+monitorDrainWait+tokenMargin; got != want {
			t.Errorf("hold %s: need %s, want %s", hold, got, want)
		}
	}
}

func TestDetectionLifetimeCoversEveryPhaseTheMonitorIsHeldFor(t *testing.T) {
	cfg := DetectionConfig{Cycles: 5, ProbeInterval: time.Second, AlarmTimeout: 2 * time.Minute, AlarmSettle: 5 * time.Second}
	// Subscribe settle, 9 probe intervals (2K-1), the full alarm wait, the settle, the margin.
	want := subscribeSettle + 9*time.Second + 2*time.Minute + 5*time.Second + tokenMargin
	if got := detectionLifetimeNeeded(cfg); got != want {
		t.Fatalf("need %s, want %s", got, want)
	}
	// Each phase counts: lengthening any one lengthens the need by exactly that much.
	longer := cfg
	longer.AlarmTimeout += time.Minute
	if got := detectionLifetimeNeeded(longer); got != want+time.Minute {
		t.Fatalf("a minute more of alarm wait moved the need by %s", got-want)
	}
	longer = cfg
	longer.Cycles++
	if got := detectionLifetimeNeeded(longer); got != want+2*cfg.ProbeInterval {
		t.Fatalf("one more cycle moved the need by %s, want two probe intervals", got-want)
	}
}

// signInOnly stands in for the platform with nothing but sign-in: it issues tokens that
// live ttl and answers every other request with an error, recording that it came.
type signInOnly struct {
	ttl time.Duration

	mu    sync.Mutex
	other []string
}

func (s *signInOnly) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Query string `json:"query"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	exp := time.Now().Add(s.ttl).Format(time.RFC3339)
	w.Header().Set("Content-Type", "application/json")
	switch {
	case r.URL.Path == "/user" && strings.Contains(body.Query, "login("):
		fmt.Fprintf(w, `{"data":{"login":{"identityToken":"id","expiresAt":%q,"superuser":false,"memberships":[{"tenant":"acme","roles":[]}]}}}`, exp)
	case r.URL.Path == "/user" && strings.Contains(body.Query, "selectTenant("):
		fmt.Fprintf(w, `{"data":{"selectTenant":{"accessToken":"a","refreshToken":"r","expiresAt":%q}}}`, exp)
	case r.URL.Path == "/user" && strings.Contains(body.Query, "refresh("):
		fmt.Fprintf(w, `{"data":{"refresh":{"accessToken":"a2","refreshToken":"r2","expiresAt":%q}}}`, exp)
	default:
		s.mu.Lock()
		s.other = append(s.other, r.URL.Path)
		s.mu.Unlock()
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprint(w, `{"errors":[{"message":"this stub serves sign-in only"}]}`)
	}
}

// A monitored run whose hold outlives the server's access tokens refuses BEFORE it touches
// anything else on the platform: no clean-tenant query, no provisioning, no drive. Before,
// it dialled with whatever token was cached, ran the whole hold, and failed at the token's
// expiry on a lost-view violation that said nothing about the platform.
func TestAMonitoredRunLongerThanItsTokenRefusesBeforeDriving(t *testing.T) {
	stub := &signInOnly{ttl: 15 * time.Minute}
	srv := httptest.NewServer(stub)
	defer srv.Close()
	hs := &sim.Handshake{
		Tenant: "acme", SimEmail: "sim@example.com", SimPassword: "pw", InstanceId: "inst",
		Endpoints: sim.Endpoints{
			UserGraphQL:       srv.URL + "/user",
			DeviceMgmtGraphQL: srv.URL + "/device",
			Ingress:           srv.URL + "/ingress",
			EventMgmtWS:       strings.Replace(srv.URL, "http://", "ws://", 1) + "/events",
		},
	}

	_, err := RunMonitored(context.Background(), hs, Profile{Manifest: "devicepulse", Hold: 30 * time.Minute}, 1)
	if !errors.Is(err, userclient.ErrTokenLifetimeTooShort) {
		t.Fatalf("want a refusal wrapping ErrTokenLifetimeTooShort, got %v", err)
	}
	if !strings.Contains(err.Error(), "cannot measure") {
		t.Fatalf("the refusal does not say the run cannot measure: %v", err)
	}
	stub.mu.Lock()
	defer stub.mu.Unlock()
	if len(stub.other) != 0 {
		t.Fatalf("the run reached the platform beyond sign-in before refusing: %v", stub.other)
	}
}

// The counterweight: with tokens that outlive the run, the same run gets PAST the token
// check and goes on to the platform (where this stub, serving sign-in only, stops it).
// Without it, a run that refused unconditionally would satisfy the test above.
func TestAMonitoredRunWithinItsTokenGoesOnToThePlatform(t *testing.T) {
	stub := &signInOnly{ttl: 2 * time.Hour}
	srv := httptest.NewServer(stub)
	defer srv.Close()
	hs := &sim.Handshake{
		Tenant: "acme", SimEmail: "sim@example.com", SimPassword: "pw", InstanceId: "inst",
		Endpoints: sim.Endpoints{
			UserGraphQL:       srv.URL + "/user",
			DeviceMgmtGraphQL: srv.URL + "/device",
			Ingress:           srv.URL + "/ingress",
			EventMgmtWS:       strings.Replace(srv.URL, "http://", "ws://", 1) + "/events",
		},
	}

	_, err := RunMonitored(context.Background(), hs, Profile{Manifest: "devicepulse", Hold: 30 * time.Minute}, 1)
	if err == nil {
		t.Fatal("a stub that serves sign-in only let the run succeed")
	}
	if errors.Is(err, userclient.ErrTokenLifetimeTooShort) {
		t.Fatalf("a two-hour token was refused for a run of about half an hour: %v", err)
	}
	stub.mu.Lock()
	defer stub.mu.Unlock()
	if len(stub.other) == 0 {
		t.Fatalf("the run never went past sign-in, so this proves nothing about the check (error: %v)", err)
	}
}
