// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"strings"
	"testing"
	"time"

	dmmodel "github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-event-processing/internal/rules"
	"github.com/devicechain-io/dc-event-processing/internal/runtime"
	"github.com/devicechain-io/dc-microservice/auth"
)

// The two compile sites the rule-duration ceiling must agree on, driven from the SAME definition
// string: the ADR-044 publish gate a console/SDK author hits, and the fact consumer that loads a
// published rule into the running engine. Both resolve their budget from rules.DefaultLimits.
//
// Disagreement is not a cosmetic inconsistency — it is the failure mode DefaultLimits' own doc
// warns about, and it is silent in BOTH directions. Gate stricter than runtime: an author is
// refused a rule the engine would happily run. Runtime stricter than gate: the rule is accepted,
// the version is frozen, and then the engine drops it on load — the author sees a published rule
// that never fires, with the only evidence in a service log.
//
// The rule is otherwise valid, so the only thing either site can be reacting to is its window.
func ceilingAgreementRule(window time.Duration) string {
	return `{"name":"slide","type":"aggregate","windowMode":"sliding","metric":"t","agg":"max","op":"gt","threshold":50,"window":"` + window.String() + `"}`
}

func publishGateAccepts(t *testing.T, def string) bool {
	t.Helper()
	res, err := (&SchemaResolver{}).ValidateDetectionRules(authedContext(auth.DeviceRead),
		struct{ Rules []detectionRuleInput }{Rules: []detectionRuleInput{{Token: "slide", Definition: def}}})
	if err != nil {
		t.Fatalf("publish gate returned a transport error: %v", err)
	}
	return res.Valid()
}

func runtimeConsumerAccepts(t *testing.T, def string) bool {
	t.Helper()
	scoped, failed := runtime.CompilePublishedRules("acme", "prof@1", []dmmodel.PublishedDetectionRule{
		{Token: "slide", Definition: def},
	})
	return failed == 0 && len(scoped) == 1
}

// TestDurationCeilingAgreesAcrossPublishGateAndRuntimeConsumer is the cross-site negative control.
// It asserts agreement at three points — comfortably inside, exactly at, and past the ceiling —
// rather than only on the rejection, because a site that refuses EVERYTHING also "agrees" on the
// over-long case.
func TestDurationCeilingAgreesAcrossPublishGateAndRuntimeConsumer(t *testing.T) {
	// Pin the process-wide ceiling so the test does not depend on deployment config, and restore
	// it so a later test in this package sees the unconfigured default.
	rules.SetPlatformMaxRuleDuration(2 * time.Hour)
	t.Cleanup(func() { rules.SetPlatformMaxRuleDuration(0) })

	for _, tc := range []struct {
		name       string
		window     time.Duration
		wantAccept bool
	}{
		{"well inside the ceiling", time.Hour, true},
		{"exactly at the ceiling", 2 * time.Hour, true},
		{"one second past the ceiling", 2*time.Hour + time.Second, false},
		{"far past the ceiling", 30 * 24 * time.Hour, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			def := ceilingAgreementRule(tc.window)
			gate := publishGateAccepts(t, def)
			rt := runtimeConsumerAccepts(t, def)
			if gate != rt {
				t.Fatalf("the publish gate and the runtime consumer DISAGREE on a %s window "+
					"(gate accepts=%v, runtime accepts=%v) — a rule can pass publish and then be dropped "+
					"on load, or be refused to an author the engine would have run", tc.window, gate, rt)
			}
			if gate != tc.wantAccept {
				t.Fatalf("window %s: both sites accept=%v, want %v", tc.window, gate, tc.wantAccept)
			}
		})
	}
}

// TestDurationCeilingIsReportedToTheAuthor pins the author-facing half: a refusal is only useful
// if the console can anchor it to the field that caused it and show a message naming the limit.
func TestDurationCeilingIsReportedToTheAuthor(t *testing.T) {
	rules.SetPlatformMaxRuleDuration(2 * time.Hour)
	t.Cleanup(func() { rules.SetPlatformMaxRuleDuration(0) })

	res, err := (&SchemaResolver{}).ValidateDetectionRules(authedContext(auth.DeviceRead),
		struct{ Rules []detectionRuleInput }{Rules: []detectionRuleInput{
			{Token: "slide", Definition: ceilingAgreementRule(30 * 24 * time.Hour)},
		}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Valid() {
		t.Fatal("a 30-day window must be refused")
	}
	errs := res.Errors()
	if len(errs) != 1 {
		t.Fatalf("want exactly one anchored error, got %d", len(errs))
	}
	// This layer carries the anchor inside the message (ValidationError.Error formats
	// "…: <field>: <msg>"), so the message is where both halves must be legible: WHICH input
	// to change, and WHAT the limit is. Without the limit an author can only guess downward.
	msg := errs[0].Message()
	if !strings.Contains(msg, "window") {
		t.Fatalf("the message must name the offending field so the console can anchor it, got: %q", msg)
	}
	if !strings.Contains(msg, "2h0m0s") {
		t.Fatalf("the message must state the ceiling the author has to get under, got: %q", msg)
	}
	if errs[0].Token() != "slide" {
		t.Fatalf("the rejection must carry the rule token, got %q", errs[0].Token())
	}
}
