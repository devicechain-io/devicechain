// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
	"gorm.io/gorm"
)

// seedDevicesSharingAType wires ONE profile + ONE device type carrying `commands`, then
// hangs n devices off that single type, returning their tokens.
//
// 🔴 IT EXISTS BECAUSE seedDeviceWithCommands GIVES EVERY DEVICE ITS OWN TYPE, which makes
// it useless for testing anything about per-TYPE work: with one device per type there is
// never a second device for a memoized decision to be reused by, so a batch that resolved
// the vocabulary once per DEVICE and one that resolved it once per TYPE do exactly the
// same amount of work. A mutation removing memoization altogether survived against that
// fixture — the test was measuring nothing.
func seedDevicesSharingAType(t *testing.T, api *Api, ctx context.Context, family string,
	n int, commands []*CommandDefinition) []string {
	t.Helper()

	profile := &DeviceProfile{}
	profile.Token = "prof-" + family
	if err := api.RDB.DB(ctx).Create(profile).Error; err != nil {
		t.Fatalf("seed profile: %v", err)
	}
	dtype := &DeviceType{}
	dtype.Token = "type-" + family
	dtype.ProfileId = &profile.ID
	if err := api.RDB.DB(ctx).Create(dtype).Error; err != nil {
		t.Fatalf("seed device type: %v", err)
	}
	for _, cd := range commands {
		cd.DeviceProfileId = profile.ID
		if cd.Token == "" {
			cd.Token = "cd-" + cd.CommandKey + "-" + family
		}
		if err := api.RDB.DB(ctx).Create(cd).Error; err != nil {
			t.Fatalf("seed command definition: %v", err)
		}
	}
	if len(commands) > 0 {
		if _, err := api.PublishDeviceProfile(ctx, profile.Token, nil, nil, "test"); err != nil {
			t.Fatalf("publish profile: %v", err)
		}
	}

	tokens := make([]string, 0, n)
	for i := 0; i < n; i++ {
		device := &Device{DeviceTypeId: dtype.ID}
		device.Token = fmt.Sprintf("%s-%d", family, i)
		if err := api.RDB.DB(ctx).Create(device).Error; err != nil {
			t.Fatalf("seed device: %v", err)
		}
		tokens = append(tokens, device.Token)
	}
	return tokens
}

// refusalFor finds the refusal naming a device, or nil if the batch allowed it.
func refusalFor(refusals []*BatchEnqueueRefusal, deviceToken string) *BatchEnqueueRefusal {
	for _, r := range refusals {
		if r != nil && r.DeviceToken == deviceToken {
			return r
		}
	}
	return nil
}

// TestTheBatchGateAgreesWithTheSingleGate drives BOTH gates over the same scenarios and
// requires the same verdict from each.
//
// 🔑 THE LEGS THIS ACTUALLY GUARDS ARE THE ONES THAT ARE NOT SHARED. The vocabulary and
// payload legs agree by construction — both call decideAgainstVocabulary — so this test
// cannot fail on them, and pretending otherwise would make it a control that cannot fire.
// What it genuinely checks is the seam: the batch gate decides DEVICE_NOT_FOUND from a
// token's ABSENCE in a bulk result while the single gate decides it from a vocabulary
// lookup, and it renders reasons through a different path. Those are separate code, and
// this is what holds them to one answer.
func TestTheBatchGateAgreesWithTheSingleGate(t *testing.T) {
	ctx := core.WithTenant(context.Background(), "A")

	cases := []struct {
		name    string
		device  string
		seed    []*CommandDefinition
		key     string
		payload string
	}{
		{"nonexistent device", "ghost", nil, "reboot", ""},
		{"free-form profile", "dev-freeform", nil, "anything", `{"whatever":123}`},
		{"unknown command key", "dev-strict", []*CommandDefinition{defWithSchema(t, "reboot", nil)}, "self-destruct", ""},
		{"empty schema accepts anything", "dev-empty", []*CommandDefinition{defWithSchema(t, "reboot", nil)}, "reboot", `{"force":true}`},
		{"payload violates schema", "dev-typed", []*CommandDefinition{
			defWithSchema(t, "drive", []ParameterSpec{{Name: "speed", DataType: MetricInt, Required: true, MinValue: f64(0), MaxValue: f64(100)}}),
		}, "drive", `{"speed":500}`},
		{"payload satisfies schema", "dev-ok", []*CommandDefinition{
			defWithSchema(t, "drive", []ParameterSpec{{Name: "speed", DataType: MetricInt, Required: true, MinValue: f64(0), MaxValue: f64(100)}}),
		}, "drive", `{"speed":50}`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			api := newEnqueueTestApi(t)
			// "ghost" is deliberately never seeded — that IS the case.
			if tc.device != "ghost" {
				seedDeviceWithCommands(t, api, ctx, tc.device, tc.seed)
			}
			var payload []byte
			if tc.payload != "" {
				payload = []byte(tc.payload)
			}

			single, err := api.ValidateCommandEnqueue(ctx, tc.device, tc.key, payload)
			if err != nil {
				t.Fatalf("single gate errored: %v", err)
			}
			refusals, err := api.ValidateCommandEnqueueBatch(ctx, []string{tc.device}, tc.key, payload)
			if err != nil {
				t.Fatalf("batch gate errored: %v", err)
			}

			batched := refusalFor(refusals, tc.device)
			if single.Allowed != (batched == nil) {
				t.Fatalf("the gates disagree: single allowed=%v, batch refused=%v (%+v)",
					single.Allowed, batched != nil, batched)
			}
			if batched == nil {
				return
			}
			if batched.Code != single.Code {
				t.Fatalf("code mismatch: single %q, batch %q", single.Code, batched.Code)
			}
			if batched.Reason != single.Reason {
				t.Fatalf("reason mismatch:\n single: %q\n  batch: %q", single.Reason, batched.Reason)
			}
		})
	}
}

// 🔴 TestOneBatchSpanningTwoDeviceTypesDecidesEachSeparately is the memoization test, and
// it is the one most likely to catch a real defect.
//
// The batch gate resolves each distinct device type ONCE and reuses the answer. That is
// the optimization the whole design rests on, and its failure mode is silent and severe:
// a cache keyed too coarsely (per batch rather than per type) gives every device the FIRST
// type's verdict. A fleet push would then be admitted for devices whose profile never
// declared the command — the platform enqueuing an actuation the enqueue gate exists to
// refuse — or refused for devices that accept it perfectly well.
//
// Every same-type test passes under that bug. Only a batch spanning two types with
// DIFFERENT vocabularies can see it, which is why it gets its own test rather than a case.
func TestOneBatchSpanningTwoDeviceTypesDecidesEachSeparately(t *testing.T) {
	api := newEnqueueTestApi(t)
	ctx := core.WithTenant(context.Background(), "A")

	// Accepts "reboot".
	seedDeviceWithCommands(t, api, ctx, "pump-1", []*CommandDefinition{defWithSchema(t, "reboot", nil)})
	// Does NOT accept "reboot" — its vocabulary is a different command entirely.
	seedDeviceWithCommands(t, api, ctx, "hvac-1", []*CommandDefinition{defWithSchema(t, "setpoint", nil)})

	refusals, err := api.ValidateCommandEnqueueBatch(ctx, []string{"pump-1", "hvac-1"}, "reboot", nil)
	if err != nil {
		t.Fatalf("batch gate errored: %v", err)
	}

	if r := refusalFor(refusals, "pump-1"); r != nil {
		t.Fatalf("pump-1 declares reboot and must be allowed, got %q: %s", r.Code, r.Reason)
	}
	r := refusalFor(refusals, "hvac-1")
	if r == nil {
		t.Fatal("hvac-1 does NOT declare reboot and must be refused — the per-type decision " +
			"leaked from the first device type to the second")
	}
	if r.Code != RejectCommandNotInVocabulary {
		t.Fatalf("hvac-1 refused as %q, want %q", r.Code, RejectCommandNotInVocabulary)
	}
	// The reason must name the device it is about, not the one whose type was resolved first.
	if !strings.Contains(r.Reason, "hvac-1") {
		t.Fatalf("the refusal names the wrong device: %q", r.Reason)
	}
}

// TestTheBatchGateResolvesEachDeviceTypeOnceNotEachDevice pins the performance claim the
// design rests on, by MEASUREMENT rather than assertion.
//
// The oracle is a comparison between two runs rather than an absolute query count, so it
// encodes no internal detail of how a vocabulary is resolved: six devices across two types
// must cost the same vocabulary reads as two devices across the same two types. Under a
// per-device resolution the first number is three times the second, and the test fails by
// the exact ratio of the defect.
//
// Without this, "one vocabulary read per TYPE" is a comment, and a refactor to a
// per-device loop would keep every other test in this file green while turning a
// ten-thousand-device batch into ten thousand profile resolutions.
func TestTheBatchGateResolvesEachDeviceTypeOnceNotEachDevice(t *testing.T) {
	ctx := core.WithTenant(context.Background(), "A")

	// countVocabularyReads runs one batch and reports how many queries resolved a
	// published vocabulary.
	countVocabularyReads := func(t *testing.T, devicesPerType int) int64 {
		t.Helper()
		api := newEnqueueTestApi(t)
		tokens := make([]string, 0, devicesPerType*2)
		for _, family := range []string{"pump", "hvac"} {
			tokens = append(tokens, seedDevicesSharingAType(t, api, ctx, family, devicesPerType,
				[]*CommandDefinition{defWithSchema(t, "reboot", nil)})...)
		}

		// 🔴 COUNT THE TABLE THE VOCABULARY IS ACTUALLY READ FROM. The first version of
		// this instrument counted `command_definitions` and observed ZERO reads — the
		// published vocabulary is served from the profile VERSION snapshot
		// (activeProfileSnapshot), not from the draft definition rows. The guard below
		// caught it; without that guard this test would have compared two zeros and
		// passed while measuring nothing at all.
		var reads int64
		err := api.RDB.Database.Callback().Query().After("gorm:query").
			Register("test:count_vocabulary_reads", func(tx *gorm.DB) {
				if strings.Contains(strings.ToLower(tx.Statement.SQL.String()), "device_profile_versions") {
					atomic.AddInt64(&reads, 1)
				}
			})
		if err != nil {
			t.Fatalf("register counting callback: %v", err)
		}

		if _, err := api.ValidateCommandEnqueueBatch(ctx, tokens, "reboot", nil); err != nil {
			t.Fatalf("batch gate errored: %v", err)
		}
		return atomic.LoadInt64(&reads)
	}

	// 🔑 BOTH RUNS SPAN EXACTLY TWO DEVICE TYPES; only the device count differs. So under
	// a per-TYPE resolution the two numbers are EQUAL, and under a per-DEVICE resolution
	// the second is three times the first. Equality is therefore the whole assertion, and
	// it encodes no internal detail of how many queries one resolution happens to take.
	few := countVocabularyReads(t, 1)  // 2 devices, 2 types
	many := countVocabularyReads(t, 3) // 6 devices, 2 types

	if few == 0 {
		t.Fatal("the counting callback observed no vocabulary reads at all — the instrument " +
			"is broken, and a comparison between two zeros would pass while measuring nothing")
	}
	if many != few {
		t.Fatalf("vocabulary reads grew with the DEVICE count: %d for 2 devices across 2 types, "+
			"%d for 6 devices across the same 2 types. The per-type decision is not being "+
			"reused, so a ten-thousand-device batch costs ten thousand profile resolutions.",
			few, many)
	}
}

// TestABatchNamesOnlyRefusals. A healthy fleet's answer is the EMPTY LIST — that is what
// keeps the response proportional to the problems rather than to the fleet. A gate that
// returned a verdict per device would make a ten-thousand-device batch's normal answer a
// ten-thousand-element payload saying "fine" ten thousand times.
func TestABatchNamesOnlyRefusals(t *testing.T) {
	api := newEnqueueTestApi(t)
	ctx := core.WithTenant(context.Background(), "A")
	tokens := []string{"pump-1", "pump-2", "pump-3"}
	for _, token := range tokens {
		seedDeviceWithCommands(t, api, ctx, token, []*CommandDefinition{defWithSchema(t, "reboot", nil)})
	}

	refusals, err := api.ValidateCommandEnqueueBatch(ctx, tokens, "reboot", nil)
	if err != nil {
		t.Fatalf("batch gate errored: %v", err)
	}
	if len(refusals) != 0 {
		t.Fatalf("a fleet that accepts the command must produce no refusals, got %d: %+v",
			len(refusals), refusals)
	}
	// The empty answer is an empty slice rather than nil. ⚠️ NOT because nil would
	// marshal differently — an earlier version of this comment claimed that and it is
	// FALSE, verified by mutation: graphql-go renders a nil Go slice as [] on a [X!]!
	// field, so the wire is identical either way. The reason is the Go boundary: this is
	// an exported API whose result callers range over, append to and length-check, and
	// "always a slice" is one less thing for each of them to reason about.
	if refusals == nil {
		t.Fatal("the empty answer must be an empty slice, not nil")
	}
}

// TestABatchCollapsesDuplicatesAndKeepsTheCallersOrder.
//
// Both halves are load-bearing. A device named twice must be asked about once and named
// once, or a caller that built its list by concatenating two groups sees the same device
// refused twice and counts its fleet wrong. And the ORDER must be the caller's, because
// that is the order a partially-admitted batch admits in — a refusal list sorted here
// would have to be re-sorted there, and the two orderings could disagree.
func TestABatchCollapsesDuplicatesAndKeepsTheCallersOrder(t *testing.T) {
	api := newEnqueueTestApi(t)
	ctx := core.WithTenant(context.Background(), "A")

	refusals, err := api.ValidateCommandEnqueueBatch(ctx,
		[]string{"zeta", "alpha", "zeta", "", "mid"}, "reboot", nil)
	if err != nil {
		t.Fatalf("batch gate errored: %v", err)
	}

	got := make([]string, 0, len(refusals))
	for _, r := range refusals {
		got = append(got, r.DeviceToken)
	}
	// 🔴 THE EMPTY TOKEN IS REFUSED, NOT DROPPED. This gate reports refusals only, so a
	// caller reads "asked about, and not named here" as ALLOWED — and an earlier version
	// discarded "" silently, which answered *allowed* for a token that can never be
	// enqueued to, with nothing in the response to say it had even been seen.
	want := []string{"zeta", "alpha", "", "mid"}
	if len(got) != len(want) {
		t.Fatalf("refusals = %v, want %v (duplicates collapsed, the empty token REFUSED)", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("refusals = %v, want %v — first-seen caller order is not preserved", got, want)
		}
	}
	for _, r := range refusals {
		if r.Code != RejectDeviceNotFound {
			t.Fatalf("%s refused as %q, want %q", r.DeviceToken, r.Code, RejectDeviceNotFound)
		}
	}
}

// 🔴 TestABatchCannotSeeAnotherTenantsDevice. Device tokens are unique per tenant, not
// globally, so two tenants can each own a "pump-1". The bulk lookup resolves tokens
// through one IN predicate, and a lost tenant scope there would not error — it would
// silently ADMIT another tenant's device into this tenant's fleet write, which is the
// worst possible outcome for a gate whose job is to refuse.
//
// The assertion is the refusal: from tenant B's view, tenant A's device does not exist.
func TestABatchCannotSeeAnotherTenantsDevice(t *testing.T) {
	api := newEnqueueTestApi(t)
	acme := core.WithTenant(context.Background(), "acme")
	other := core.WithTenant(context.Background(), "other")
	seedDeviceWithCommands(t, api, acme, "pump-1", []*CommandDefinition{defWithSchema(t, "reboot", nil)})

	refusals, err := api.ValidateCommandEnqueueBatch(other, []string{"pump-1"}, "reboot", nil)
	if err != nil {
		t.Fatalf("batch gate errored: %v", err)
	}
	r := refusalFor(refusals, "pump-1")
	if r == nil {
		t.Fatal("another tenant's device was admitted to this tenant's batch — the bulk " +
			"lookup is not tenant-scoped")
	}
	if r.Code != RejectDeviceNotFound {
		t.Fatalf("refused as %q, want %q", r.Code, RejectDeviceNotFound)
	}
}

// TestAnEmptyBatchIsAnEmptyAnswer, not an error and not a nil slice. The caller reaches
// this when a group resolves to no members, which is an ordinary state (a dynamic group
// whose selector currently matches nothing), not a fault.
func TestAnEmptyBatchIsAnEmptyAnswer(t *testing.T) {
	api := newEnqueueTestApi(t)
	ctx := core.WithTenant(context.Background(), "A")

	// 🔑 Only nil and [] are EMPTY. [""] is not — it is a batch of one token that cannot
	// name a device, and it must produce a refusal rather than silence (see
	// TestABatchCollapsesDuplicatesAndKeepsTheCallersOrder). Folding it in here, as an
	// earlier version did, made "the caller sent nothing" and "the caller sent something
	// unusable" the same case, which is precisely the conflation that let an unusable
	// token read as allowed.
	for _, tokens := range [][]string{nil, {}} {
		refusals, err := api.ValidateCommandEnqueueBatch(ctx, tokens, "reboot", nil)
		if err != nil {
			t.Fatalf("an empty batch must not error (%v): %v", tokens, err)
		}
		if refusals == nil {
			t.Fatalf("an empty batch must answer with an empty slice, not nil (%v)", tokens)
		}
		if len(refusals) != 0 {
			t.Fatalf("an empty batch produced %d refusals", len(refusals))
		}
	}

	deduped, err := api.ValidateCommandEnqueueBatch(ctx, []string{"", ""}, "reboot", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(deduped) != 1 || deduped[0].Code != RejectDeviceNotFound {
		t.Fatalf("[\"\", \"\"] = %+v; want exactly one DEVICE_NOT_FOUND refusal", deduped)
	}
}
