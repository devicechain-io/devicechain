// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package premises

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

func fixture() Record {
	r := Record{SchemaVersion: 1}
	for i, bind := range []string{"provider", "tofu", "kubernetes", "cloud"} {
		r.Experiments = append(r.Experiments, Premise{
			ID: []string{"E-b", "E-j-1", "E-l", "E-a"}[i], Verdict: Holds,
			Measured: map[string]any{"observed": true}, TofuVersion: "1.10.10",
			ProviderVersion: "6.49.0", KubernetesVersion: "1.34.1",
			Date: "2026-11-21", TranscriptSHA256: strings.Repeat("a", 64),
			Binds: []string{bind},
		})
	}
	return r
}

func TestStalenessScopesMismatch(t *testing.T) {
	base := Pins{Provider: "6.49.0", Tofu: "1.10.10", Kubernetes: "1.34.1"}
	now := time.Date(2026, 11, 21, 0, 0, 0, 0, time.UTC)
	for _, component := range []string{"provider", "tofu", "kubernetes", ""} {
		pins := base
		switch component {
		case "provider":
			pins.Provider = "changed"
		case "tofu":
			pins.Tofu = "changed"
		case "kubernetes":
			pins.Kubernetes = "changed"
		}
		result := Staleness(fixture(), pins, now)
		for _, p := range fixture().Experiments {
			want := OK
			if p.Binds[0] == component {
				want = Stale
			}
			if result[p.ID] != want {
				t.Errorf("%s mismatch: %s = %s, want %s", component, p.ID, result[p.ID], want)
			}
		}
	}
	r := fixture()
	r.Experiments[0].Binds = []string{"provider", "tofu", "cloud"}
	r.Experiments[0].TofuVersion = "changed"
	if Staleness(r, base, now)["E-b"] != Stale {
		t.Fatal("second bound version ignored")
	}
	for _, age := range []int{-1, 180, 181} {
		want := OK
		if age < 0 || age > 180 {
			want = Stale
		}
		if Staleness(fixture(), base, now.AddDate(0, 0, age))["E-a"] != want {
			t.Errorf("cloud age %d accepted incorrectly", age)
		}
	}
	for _, bindings := range [][]string{nil, {"unknown"}} {
		r.Experiments[0].Binds = bindings
		if Staleness(r, base, now)["E-b"] != Stale {
			t.Fatal("invalid bindings accepted")
		}
	}
	r = fixture()
	r.Experiments[3].Date = "bad"
	if Staleness(r, base, now)["E-a"] != Stale {
		t.Fatal("invalid cloud date accepted")
	}
}

func TestRequireHolds(t *testing.T) {
	r := fixture()
	if err := r.requireHolds("E-b", "E-a"); err != nil {
		t.Fatal(err)
	}
	if err := r.requireHolds("absent"); !errors.Is(err, ErrPremiseNotHeld) {
		t.Fatalf("absent id accepted: %v", err)
	}
	for _, v := range []Verdict{Fails, Inconclusive, ""} {
		r.Experiments[0].Verdict = v
		if err := r.RequireHolds("E-b"); !errors.Is(err, ErrPremiseNotHeld) {
			t.Fatalf("non-HOLDS accepted: %v", err)
		}
	}
}

func TestValidation(t *testing.T) {
	for _, edit := range []func(*Record){
		func(r *Record) { r.SchemaVersion = 2 },
		func(r *Record) { r.Experiments = append(r.Experiments, r.Experiments[0]) },
		func(r *Record) { r.Experiments[0].ID = "unknown" },
		func(r *Record) { r.Experiments[0].Verdict = "OK" },
		func(r *Record) { r.Experiments[0].Binds = nil },
		func(r *Record) { r.Experiments[0].Binds = []string{"unknown"} },
		func(r *Record) { r.Experiments[0].Binds = []string{"cloud", "cloud"} },
		func(r *Record) { r.Experiments[0].Date = "yesterday" },
		func(r *Record) { r.Experiments[0].TranscriptSHA256 = "missing" },
		func(r *Record) { r.Experiments[0].Measured = nil },
		func(r *Record) { r.Experiments[0].TofuVersion = "" },
	} {
		r := fixture()
		edit(&r)
		if err := r.Validate(); !errors.Is(err, ErrInvalidRecord) {
			t.Fatalf("invalid record accepted: %v", err)
		}
	}
	if err := fixture().Validate(); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{
		`{"schema_version":1,"experiments":[],"unknown":1}`,
		`{"schema_version":1,"experiments":[]} {}`,
	} {
		if _, err := Parse(strings.NewReader(raw)); !errors.Is(err, ErrInvalidRecord) {
			t.Fatalf("invalid envelope accepted: %v", err)
		}
	}
}

func writeRoot(t *testing.T, base, name, provider string) {
	t.Helper()
	dir := base + "/" + name
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "terraform {\n  required_version = \">= 1.10\"\n  required_providers {\n    google = {\n      source  = \"hashicorp/google\"\n      version = \"" + provider + "\"\n    }\n  }\n}\n"
	if err := os.WriteFile(dir+"/versions.tf", []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestProviderPinFromRoots(t *testing.T) {
	base := t.TempDir()
	if _, err := ProviderPinFromRoots(base); !errors.Is(err, ErrNoFleetRoots) {
		t.Fatalf("no roots must be reported, not guessed: %v", err)
	}
	writeRoot(t, base, "gke-gcs", "8.4.0")
	writeRoot(t, base, "gke-pg", "8.4.0")
	pin, err := ProviderPinFromRoots(base)
	if err != nil || pin != "8.4.0" {
		t.Fatalf("pin = %q, %v", pin, err)
	}
	// The roots must agree: one premise record cannot bind to two providers.
	writeRoot(t, base, "gke-pg", "8.5.0")
	if _, err := ProviderPinFromRoots(base); err == nil {
		t.Fatal("disagreeing roots accepted")
	}
	// A range is not a pin.
	writeRoot(t, base, "gke-pg", "~> 8.4")
	writeRoot(t, base, "gke-gcs", "~> 8.4")
	if _, err := ProviderPinFromRoots(base); err == nil {
		t.Fatal("a version range was accepted as an exact pin")
	}
}

// The negative control the spec asks for: a recorded version that moves must turn the premise STALE.
func TestRecordedVersionMovingMarksPremiseStale(t *testing.T) {
	base := t.TempDir()
	writeRoot(t, base, "gke-gcs", "8.4.0")
	writeRoot(t, base, "gke-pg", "8.4.0")
	r := Record{SchemaVersion: 1, Experiments: []Premise{{
		ID: "E-j-3", Verdict: Holds, Measured: map[string]any{"x": true}, TofuVersion: "1.10.10",
		ProviderVersion: "8.4.0", Date: "2026-11-21", TranscriptSHA256: strings.Repeat("b", 64),
		Binds: []string{"tofu", "provider"},
	}}}
	now := time.Date(2026, 11, 22, 0, 0, 0, 0, time.UTC)
	pin, err := ProviderPinFromRoots(base)
	if err != nil {
		t.Fatal(err)
	}
	if got := Staleness(r, Pins{Provider: pin, Tofu: "1.10.10"}, now)["E-j-3"]; got != OK {
		t.Fatalf("matching pins reported %s", got)
	}
	writeRoot(t, base, "gke-gcs", "8.5.0")
	writeRoot(t, base, "gke-pg", "8.5.0")
	pin, _ = ProviderPinFromRoots(base)
	if got := Staleness(r, Pins{Provider: pin, Tofu: "1.10.10"}, now)["E-j-3"]; got != Stale {
		t.Fatalf("a moved provider pin left the premise %s", got)
	}
	if got := Staleness(r, Pins{Provider: "8.4.0", Tofu: "1.10.11"}, now)["E-j-3"]; got != Stale {
		t.Fatalf("a moved tofu pin left the premise %s", got)
	}
}

func TestBlockedPremisesNeverPass(t *testing.T) {
	r := fixture()
	r.Experiments[3].Verdict = Holds
	r.Experiments[3].Measured = map[string]any{"blocked": "U2"}
	if err := r.Validate(); !errors.Is(err, ErrInvalidRecord) {
		t.Fatalf("a BLOCKED premise claiming HOLDS was accepted: %v", err)
	}
	r.Experiments[3].Verdict = Inconclusive
	if err := r.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := r.RequireHolds("E-a"); !errors.Is(err, ErrPremiseNotHeld) {
		t.Fatalf("a blocked premise satisfied a gate: %v", err)
	}
}

// The committed record is what the held PRs' gates read.
func TestCommittedRecordTruthfulness(t *testing.T) {
	f, err := os.Open("gke.json")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	r, err := Parse(f)
	if err != nil {
		t.Fatal(err)
	}
	// Tier C experiments need a throwaway cloud project. Only the C0a set (E-d, E-f, E-h, E-i) was approved
	// to run; every other one stays blocked on U2. A C0a experiment either carries a measured verdict, or,
	// when the attempt produced none, stays INCONCLUSIVE with the attempt's own cause (never U2, never a pass).
	cloud := map[string]bool{"E-a": true, "E-b": true, "E-c": true, "E-d": true, "E-e": true, "E-f": true, "E-f2": true,
		"E-g-gcs": true, "E-h": true, "E-i": true, "E-k": true}
	c0a := map[string]bool{"E-d": true, "E-f": true, "E-h": true, "E-i": true}
	for _, p := range r.Experiments {
		switch {
		case !cloud[p.ID]:
		case !c0a[p.ID]:
			if p.Verdict != Inconclusive || p.Measured["blocked"] != "U2" {
				t.Errorf("%s needs cloud but is recorded as %s without a U2 block", p.ID, p.Verdict)
			}
		case p.Verdict == Inconclusive:
			if b, _ := p.Measured["blocked"].(string); b == "" || b == "U2" || p.Measured["reason"] == nil {
				t.Errorf("%s was attempted without a verdict: it must name its own cause, not U2", p.ID)
			}
		default:
			if p.Measured["blocked"] != nil {
				t.Errorf("%s carries a verdict and a block", p.ID)
			}
		}
	}
	if err := r.RequireHolds("E-j-1", "E-j-2", "E-j-4"); err != nil {
		t.Errorf("the S1-6 premises: %v", err)
	}
}

// The committed record, against synthetic fleet roots: moving the roots' pin must stale the
// provider-bound E-j premise and nothing that is not bound to the provider.
func TestCommittedRecordGoesStaleWhenRootPinMoves(t *testing.T) {
	f, err := os.Open("gke.json")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	r, err := Parse(f)
	if err != nil {
		t.Fatal(err)
	}
	base := t.TempDir()
	writeRoot(t, base, "gke-gcs", "8.4.0")
	writeRoot(t, base, "gke-pg", "8.4.0")
	now := time.Date(2026, 11, 22, 0, 0, 0, 0, time.UTC)
	pins := func() Pins {
		p, err := ProviderPinFromRoots(base)
		if err != nil {
			t.Fatal(err)
		}
		return Pins{Provider: p, Tofu: "1.10.10", Kubernetes: "1.37.0"}
	}
	if got := Staleness(r, pins(), now); got["E-j-3"] != OK || got["E-j-1"] != OK || got["E-l"] != OK {
		t.Fatalf("freshly measured premises already stale: %v", got)
	}
	writeRoot(t, base, "gke-gcs", "8.4.1")
	writeRoot(t, base, "gke-pg", "8.4.1")
	got := Staleness(r, pins(), now)
	if got["E-j-3"] != Stale {
		t.Fatal("E-j-3 binds the provider and must go stale when the roots' pin moves")
	}
	for _, id := range []string{"E-j-1", "E-j-2", "E-j-4", "E-j-5", "E-l"} {
		if got[id] != OK {
			t.Errorf("%s does not bind the provider but went %s", id, got[id])
		}
	}
}

func TestProviderPinIgnoresComments(t *testing.T) {
	base := t.TempDir()
	writeRoot(t, base, "gke-gcs", "8.4.0")
	body := "terraform {\n  required_providers {\n    # google = {\n    #   version = \"8.4.0\"\n    # }\n    /* google = { version = \"8.3.0\" } */\n    google = {\n      source  = \"hashicorp/google\" # was 8.4.0\n      version = \"8.5.0\"\n    }\n  }\n}\n"
	if err := os.WriteFile(base+"/gke-gcs/versions.tf", []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	pin, err := ProviderPinFromRoots(base)
	if err != nil || pin != "8.5.0" {
		t.Fatalf("a commented-out pin masked the live one: %q, %v", pin, err)
	}
}

func TestIsExactVersion(t *testing.T) {
	for v, want := range map[string]bool{"1.10.10": true, "1.10.10-rc1": true, "1.10": false, ">= 1.10": false, "": false, "1.10.10 ": false} {
		if IsExactVersion(v) != want {
			t.Errorf("IsExactVersion(%q) = %v, want %v", v, !want, want)
		}
	}
}
