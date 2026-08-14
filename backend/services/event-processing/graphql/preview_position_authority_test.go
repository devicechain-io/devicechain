// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"errors"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-event-processing/model"
	"github.com/devicechain-io/dc-microservice/auth"
)

// The replay preview evaluates geofence containment, so a preview that TESTS CONTAINMENT is a read
// of position and takes location:read — while every other preview keeps taking device:read alone.
//
// 🔴 WHY THIS NEEDED ITS OWN GATE. device:read is in the read-only viewer baseline every enabled
// tenant member receives; location:read is deliberately absent from it, so that knowing WHERE a
// vehicle or a person is can be granted separately from knowing how warm it is. Before this gate a
// member refused a single coordinate by latestLocation could preview `geo.inFence("yard")` over a
// day and read back, per device token, the instant it entered and the instant it left — the same
// question, answered from the side.
//
// The gate is on the DRAFT (compiled.RequiresPosition), not on the caller's role, and the pair of
// tests below is what holds that: refusing a containment preview is only correct while an ordinary
// preview still passes. A gate that refused everything would satisfy the first test alone.

// A viewer-baseline caller — device:read and the rest of the read-only baseline, but NOT
// location:read — is refused a preview whose leaf calls inFence.
func TestAFencePreviewIsRefusedWithoutThePositionAuthority(t *testing.T) {
	r := &SchemaResolver{Profiles: &model.ProfileActiveStore{}, FenceSets: nilFenceSource{}}

	// The full read-only viewer baseline, spelled out: this is exactly what an enabled member
	// holds before anyone grants them a role, and it is the population the gate must exclude.
	viewer := previewCtx(auth.DeviceRead, auth.EventRead, auth.StateRead, auth.CommandRead, auth.AlarmRead)

	res, err := r.PreviewRule(viewer, previewInput(fenceDraft,
		"2026-08-01T00:00:00Z", "2026-08-02T00:00:00Z"))
	if err == nil {
		t.Fatalf("the viewer baseline previewed a containment rule and was not refused; "+
			"result=%+v — this is the fence-granular position history location:read exists to gate", res)
	}
	if !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("refusal must be ErrForbidden — an authorization answer the caller can act on, "+
			"not an incidental failure that happens to stop the replay; got %T: %v", err, err)
	}
	if res != nil {
		t.Errorf("a refused preview must return no result at all, got %+v", res)
	}
}

// 🔴 THE COUNTERWEIGHT, AND IT IS NOT OPTIONAL. Every assertion above is also satisfied by a gate
// that refuses EVERY preview — which would break rule authoring platform-wide while looking exactly
// like a working gate. A draft that never asks a fence anything must still preview on device:read.
func TestAThresholdPreviewStillWorksOnDeviceReadAlone(t *testing.T) {
	r := &SchemaResolver{Profiles: &model.ProfileActiveStore{}, FenceSets: nilFenceSource{}}

	// An inverted window, so execution stops at the window check — reaching it at all proves the
	// authority gate was passed rather than that the preview merely failed somewhere.
	res, err := r.PreviewRule(previewCtx(auth.DeviceRead), previewInput(plainDraft,
		"2026-08-02T00:00:00Z", "2026-08-01T00:00:00Z"))
	if err != nil {
		t.Fatalf("a non-containment preview must not require the position authority, got: %v", err)
	}
	diags := res.Diagnostics()
	if len(diags) != 1 || !strings.Contains(diags[0].Message(), "end of the window") {
		t.Fatalf("expected the window diagnostic, proving the authority gate was passed; got %v", diags)
	}
}

// Granting location:read lets the same caller through — so the refusal above is about the missing
// authority and nothing else. Without this, a draft rejected for an unrelated reason (a compile
// failure, a missing profile) would produce the same error and the first test would pass anyway.
func TestAFencePreviewIsAllowedWithThePositionAuthority(t *testing.T) {
	r := &SchemaResolver{Profiles: &model.ProfileActiveStore{}, FenceSets: nilFenceSource{}}

	res, err := r.PreviewRule(previewCtx(auth.DeviceRead, auth.LocationRead),
		previewInput(fenceDraft, "2026-08-02T00:00:00Z", "2026-08-01T00:00:00Z"))
	if err != nil {
		t.Fatalf("location:read must admit a containment preview, got: %v", err)
	}
	diags := res.Diagnostics()
	if len(diags) != 1 || !strings.Contains(diags[0].Message(), "end of the window") {
		t.Fatalf("expected the window diagnostic, proving the gate was passed; got %v", diags)
	}
}

// The gate follows the QUESTION, not the caller: location:read alone is not a way in either, since
// previewing any rule still needs device:read. Pins that the new check was ADDED to the existing
// one rather than replacing it — a substitution would leave every preview open to a caller holding
// only the position authority.
func TestThePositionAuthorityDoesNotReplaceDeviceRead(t *testing.T) {
	r := &SchemaResolver{Profiles: &model.ProfileActiveStore{}, FenceSets: nilFenceSource{}}

	for _, c := range []struct {
		name  string
		draft string
	}{
		{"containment draft", fenceDraft},
		{"threshold draft", plainDraft},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, err := r.PreviewRule(previewCtx(auth.LocationRead), previewInput(c.draft,
				"2026-08-01T00:00:00Z", "2026-08-02T00:00:00Z"))
			if err == nil {
				t.Fatal("previewing without device:read must be refused whatever the draft tests")
			}
		})
	}
}
