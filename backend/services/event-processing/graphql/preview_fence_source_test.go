// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-event-processing/internal/geofence"
	"github.com/devicechain-io/dc-event-processing/internal/runtime"
	"github.com/devicechain-io/dc-event-processing/model"
	"github.com/devicechain-io/dc-microservice/auth"
	dccore "github.com/devicechain-io/dc-microservice/core"
)

// A geofence preview resolves each replayed event's STAMPED fence-set version through the
// fence-set source (ADR-078) — the archive door on device-management. That source is a field on
// this resolver, wired in main, and these tests hold the two things a caller can observe about it
// without a broker: that a draft which needs it and does not have it is told so, and that a draft
// which has it gets past that gate.

// previewCtx builds an authorized, tenant-scoped context for previewRule.
func previewCtx(authorities ...auth.Authority) context.Context {
	strs := make([]string, 0, len(authorities))
	for _, a := range authorities {
		strs = append(strs, string(a))
	}
	ctx := dccore.WithTenant(context.Background(), "acme")
	return auth.WithClaims(ctx, &auth.Claims{Tenant: "acme", Authorities: strs})
}

// fenceDraft is a rule definition whose leaf is pure containment — the draft that cannot be
// previewed without the archive.
const fenceDraft = `{"id":"draft","name":"in the yard","type":"threshold","when":{"cel":"geo.inFence(\"yard\")"}}`

// plainDraft is a rule definition with no containment at all — the counterweight: it must not be
// blocked by a missing fence source, since it never asks a fence anything.
const plainDraft = `{"id":"draft","name":"hot","type":"threshold","when":{"metric":"temperature","op":"gt","threshold":80}}`

// nilFenceSource is a source that is present but resolves nothing. It stands in for a wired seam in
// the tests that only need to get PAST the availability gate.
type nilFenceSource struct{}

func (nilFenceSource) FenceSetAt(context.Context, string, int32) (*geofence.FenceSet, error) {
	return nil, context.Canceled
}

var _ runtime.FenceSetSource = nilFenceSource{}

// previewInput builds a previewRule input over a rule definition, with a window the caller chooses
// so a test can steer where execution stops.
func previewInput(definition, start, end string) struct{ Input previewRuleInput } {
	def := definition
	return struct{ Input previewRuleInput }{Input: previewRuleInput{
		RuleDefinition: &def,
		ProfileToken:   "p",
		Start:          start,
		End:            end,
	}}
}

// A draft that tests containment, previewed with no fence-set source, is told the archive is
// unreachable — rather than replaying a whole window to produce an empty timeline and an
// eval-error count, which an author reads as "my rule never fires".
//
// The two controls are in the same file and matter equally: the SAME draft with a source wired gets
// past this gate (proving the message is about the source, not about the draft), and a draft that
// never calls inFence is not blocked by a missing source (proving the gate is about containment,
// not about previews in general).
func TestPreviewOfAFenceRuleWithNoSourceIsHonestlyDegraded(t *testing.T) {
	r := &SchemaResolver{Profiles: &model.ProfileActiveStore{}}

	res, err := r.PreviewRule(previewCtx(auth.DeviceRead, auth.LocationRead),
		previewInput(fenceDraft, "2026-08-01T00:00:00Z", "2026-08-02T00:00:00Z"))
	if err != nil {
		t.Fatalf("previewRule: %v", err)
	}
	if res.Degraded() == nil {
		t.Fatal("a geofence preview with no fence-set source reported no degraded reason at all")
	}
	if !strings.Contains(*res.Degraded(), "geofence") {
		t.Errorf("degraded reason %q does not mention geofences; an author cannot act on it", *res.Degraded())
	}
	if len(res.Firings()) != 0 {
		t.Errorf("a preview that could not evaluate containment reported %d firings", len(res.Firings()))
	}
}

// CONTROL 1: the same draft with a source wired gets PAST the availability gate — execution
// continues to the window check, which rejects an inverted window with a compile-style diagnostic.
// A resolver that ignored its FenceSets field and passed nil onward would still stop at the gate
// above and never reach here.
func TestPreviewOfAFenceRuleWithASourceGetsPastTheGate(t *testing.T) {
	r := &SchemaResolver{Profiles: &model.ProfileActiveStore{}, FenceSets: nilFenceSource{}}

	// An inverted window: the next check after the fence gate.
	res, err := r.PreviewRule(previewCtx(auth.DeviceRead, auth.LocationRead),
		previewInput(fenceDraft, "2026-08-02T00:00:00Z", "2026-08-01T00:00:00Z"))
	if err != nil {
		t.Fatalf("previewRule: %v", err)
	}
	if res.Ok() {
		t.Fatal("an inverted window must be rejected; the test's stopping point moved")
	}
	diags := res.Diagnostics()
	if len(diags) != 1 || !strings.Contains(diags[0].Message(), "end of the window") {
		t.Fatalf("expected the window diagnostic (proving the fence gate was passed), got %v", diags)
	}
}

// CONTROL 2: a draft with no containment is never blocked by a missing fence source. Without this,
// the gate could be refusing every preview and the first test would still pass.
func TestPreviewOfANonFenceRuleIsNotBlockedByAMissingSource(t *testing.T) {
	r := &SchemaResolver{Profiles: &model.ProfileActiveStore{}}

	res, err := r.PreviewRule(previewCtx(auth.DeviceRead),
		previewInput(plainDraft, "2026-08-02T00:00:00Z", "2026-08-01T00:00:00Z"))
	if err != nil {
		t.Fatalf("previewRule: %v", err)
	}
	diags := res.Diagnostics()
	if len(diags) != 1 || !strings.Contains(diags[0].Message(), "end of the window") {
		t.Fatalf("a non-containment draft was stopped before the window check with no fence source: %v", diags)
	}
}
