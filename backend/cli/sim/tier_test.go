// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

// tierStub answers login, the tier listing (existing is the tenantTiers JSON array) and records
// every tier mutation's variables. failMutation, when set, is the errors array a mutation gets.
type tierStub struct {
	mu           sync.Mutex
	mutations    []string
	vars         []map[string]any
	existing     string
	failMutation string
}

func (s *tierStub) server(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		_ = json.Unmarshal(raw, &body)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(body.Query, "login("):
			_, _ = fmt.Fprintf(w, `{"data":{"login":{"identityToken":"t","expiresAt":%q,"superuser":true,"memberships":[]}}}`,
				time.Now().Add(time.Hour).Format(time.RFC3339))
		case strings.Contains(body.Query, "tenantTiers"):
			_, _ = fmt.Fprintf(w, `{"data":{"tenantTiers":%s}}`, s.existing)
		default:
			s.mu.Lock()
			s.mutations = append(s.mutations, body.Query)
			s.vars = append(s.vars, body.Variables)
			s.mu.Unlock()
			if s.failMutation != "" {
				_, _ = fmt.Fprintf(w, `{"errors":%s}`, s.failMutation)
				return
			}
			_, _ = w.Write([]byte(`{"data":{"ok":{"token":"tp-showcase"}}}`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func (s *tierStub) ensure(t *testing.T) error {
	srv := s.server(t)
	return NewAdmin(srv.URL, srv.URL, "admin@example.com", "pw").EnsureTier(context.Background(), TowerpulseShowcaseTier)
}

func configOf(t *testing.T, v map[string]any) map[string]float64 {
	t.Helper()
	var got map[string]float64
	if err := json.Unmarshal([]byte(v["config"].(string)), &got); err != nil {
		t.Fatalf("config is not a JSON object: %v", err)
	}
	return got
}

func TestEnsureTierCreatesAnAbsentTierWithTheScenariosFloors(t *testing.T) {
	s := &tierStub{existing: `[{"token":"silver","config":null}]`}
	if err := s.ensure(t); err != nil {
		t.Fatal(err)
	}
	if len(s.mutations) != 1 || !strings.Contains(s.mutations[0], "createTenantTier") {
		t.Fatalf("want one createTenantTier, got %v", s.mutations)
	}
	got := configOf(t, s.vars[0])
	for k, want := range TowerpulseShowcaseTier.Config {
		if got[k] != float64(want) {
			t.Errorf("%s = %v, want %d", k, got[k], want)
		}
	}
	if s.vars[0]["token"] != "tp-showcase" {
		t.Errorf("token = %v", s.vars[0]["token"])
	}
}

// Idempotent: a tier already meeting every floor is not written to, so a re-run touches nothing.
func TestEnsureTierLeavesAMeetingTierAlone(t *testing.T) {
	s := &tierStub{existing: `[{"token":"tp-showcase","config":"{\"calculatedMetricsPerProfile\":8,\"ingestReadingsPerSecond\":5000,\"ingestBurst\":9000,\"shedPriority\":10}"}]`}
	if err := s.ensure(t); err != nil {
		t.Fatal(err)
	}
	if len(s.mutations) != 0 {
		t.Fatalf("a tier above every floor was written: %v", s.mutations)
	}
}

// A tier below a floor is raised on that key only; every other key, and any key above its floor, is kept.
func TestEnsureTierRaisesOnlyWhatIsBelowItsFloor(t *testing.T) {
	s := &tierStub{existing: `[{"token":"tp-showcase","config":"{\"calculatedMetricsPerProfile\":0,\"ingestReadingsPerSecond\":5000,\"ingestBurst\":1000,\"shedPriority\":10}"}]`}
	if err := s.ensure(t); err != nil {
		t.Fatal(err)
	}
	if len(s.mutations) != 1 || !strings.Contains(s.mutations[0], "updateTenantTier") {
		t.Fatalf("want one updateTenantTier, got %v", s.mutations)
	}
	got := configOf(t, s.vars[0])
	if got["calculatedMetricsPerProfile"] != float64(TowerpulseShowcaseTier.Config["calculatedMetricsPerProfile"]) {
		t.Errorf("calculatedMetricsPerProfile not raised: %v", got)
	}
	if got["ingestReadingsPerSecond"] != 5000 || got["shedPriority"] != 10 {
		t.Errorf("an operator's higher or unrelated key was lost: %v", got)
	}
	if got["ingestBurst"] != 2000 {
		t.Errorf("a stated key below its floor was not raised: %v", got)
	}
}

// 🔴 A KEY THE TIER OMITS IS INHERITED, AND DCCTL CANNOT SEE THE VALUE. Writing the floor could
// LOWER the limit for every tenant on the tier; leaving it could leave it BELOW the floor (a missing
// calculatedMetricsPerProfile inherits 0). So the tier is refused, naming the key, and nothing is
// written. A key that is null counts as unstated.
//
// Negative control: with the earlier rule (an unstated key is skipped silently) err is nil for every
// case; with the rule before that (an unstated key is written) the tier is also rewritten.
func TestEnsureTierRefusesAnExistingTierThatDoesNotStateAFloorKey(t *testing.T) {
	for name, c := range map[string]struct{ existing, want string }{
		"config omits every floor key": {`[{"token":"tp-showcase","config":"{\"shedPriority\":10}"}]`, `"calculatedMetricsPerProfile"`},
		"config is empty":              {`[{"token":"tp-showcase","config":null}]`, `"ingestBurst"`},
		"floor key is null":            {`[{"token":"tp-showcase","config":"{\"calculatedMetricsPerProfile\":8,\"ingestReadingsPerSecond\":5000,\"ingestBurst\":null}"}]`, `"ingestBurst"`},
		"one of three omitted":         {`[{"token":"tp-showcase","config":"{\"calculatedMetricsPerProfile\":8,\"ingestReadingsPerSecond\":5000}"}]`, `"ingestBurst"`},
	} {
		t.Run(name, func(t *testing.T) {
			s := &tierStub{existing: c.existing}
			err := s.ensure(t)
			var refused *TierRefusedError
			if !errors.As(err, &refused) {
				t.Fatalf("want a TierRefusedError, got %v", err)
			}
			if !strings.Contains(err.Error(), c.want) || !strings.Contains(err.Error(), "does not state") {
				t.Fatalf("the refusal does not name the key %s: %v", c.want, err)
			}
			if len(s.mutations) != 0 {
				t.Fatalf("a tier with an unstated key was written: %v %v", s.mutations, s.vars)
			}
		})
	}
}

// Counterweight: a tier that states every key is never refused, and one that states them all at or
// above the floors is left alone (TestEnsureTierLeavesAMeetingTierAlone).
func TestRaiseToFloorsReportsExactlyTheUnstatedKeys(t *testing.T) {
	_, changed, unstated := raiseToFloors(map[string]any{"a": float64(5), "b": float64(1)}, map[string]int{"a": 3, "b": 4, "c": 9, "d": 1})
	if !changed {
		t.Error("b (1 < 4) was not raised")
	}
	if !reflect.DeepEqual(unstated, []string{"c", "d"}) {
		t.Errorf("unstated = %v", unstated)
	}
}

// A create that loses a race to another create is the goal reached, not a failure.
func TestEnsureTierToleratesAConflictingCreate(t *testing.T) {
	s := &tierStub{existing: `[]`, failMutation: `[{"message":"m","extensions":{"code":"CONFLICT"}}]`}
	if err := s.ensure(t); err != nil {
		t.Fatalf("a CONFLICT on create must count as present: %v", err)
	}
}

// The refusal path: the failure surfaces at create time as a TierRefusedError that names the tier,
// its config and the way out, not later as a refused publish.
func TestEnsureTierRefusalNamesTheTierAndTheFix(t *testing.T) {
	s := &tierStub{existing: `[]`, failMutation: `[{"message":"permission denied: tenant:write required","extensions":{"code":"FORBIDDEN"}}]`}
	err := s.ensure(t)
	var refused *TierRefusedError
	if !errors.As(err, &refused) {
		t.Fatalf("want *TierRefusedError, got %v", err)
	}
	msg := err.Error()
	for _, want := range []string{"tp-showcase", "calculatedMetricsPerProfile", "createTenantTier", "--tier", "permission denied"} {
		if !strings.Contains(msg, want) {
			t.Errorf("refusal message lacks %q:\n%s", want, msg)
		}
	}
}

func TestOnlyScenariosThatNeedATierAreMapped(t *testing.T) {
	if spec, ok := TierForManifest("towerpulse"); !ok || spec.Token != "tp-showcase" {
		t.Fatalf("towerpulse: %v %v", spec, ok)
	}
	for _, id := range KnownManifestIds {
		if id == "towerpulse" {
			continue
		}
		if _, ok := TierForManifest(id); ok {
			t.Errorf("%s is mapped to a tier of its own", id)
		}
	}
	if TowerpulseShowcaseTier.Token == DefaultTenantTier {
		t.Error("the showcase tier must not be a platform default tier")
	}
}
