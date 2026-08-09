// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package predicate

import (
	celast "github.com/google/cel-go/common/ast"
)

// ReferencesFences reports whether this leaf calls the containment function at all.
//
// It backs the POSITION-SCOPED FEED, which is the geofence sibling of the metric-scoped feed
// (see ScopableMetrics) and exists for the identical reason. A fence leaf asked about an event
// that reports no position cannot answer, and the two ways of not answering are both wrong:
//
//   - returning FALSE is a positive claim that the device is outside, which for a Duration rule
//     CANCELS an in-flight hold — the exact hazard the metric feed scope was introduced to fix,
//     reproduced by a device sending one battery reading mid-transit;
//   - returning an ERROR is safe for the hold (the runtime skips the sample) but would fire on
//     every non-location event the device sends, burying the eval-error signal that is supposed
//     to mean "a rule is broken" under routine telemetry, and marking every preview degraded.
//
// Scoping the feed removes the question instead of answering it badly. It is SOUND with no
// partial-evaluation proof of the kind ScopableMetrics needs, because the scope is not derived
// from what the leaf computes: a leaf that calls inFence cannot produce a meaningful value for a
// positionless event under ANY assignment of the other variables — the call itself errors. So a
// skipped positionless event could not have raised, and no raise is dropped.
//
// Note what this deliberately does NOT do: it does not require the leaf to be false without a
// position (`!geo.inFence("yard") || m["x"] > 1` is scoped out of positionless events too). That
// is the raw-CEL-author-owns-totality contract the rest of this package keeps — a leaf mixing
// fences with measurements is scoped by BOTH gates, so it feeds only events carrying both.
func (p *Predicate) ReferencesFences() bool { return p.referencesFences }

// referencesFences walks a type-checked AST for a call to the containment function. It matches
// the FUNCTION NAME rather than the receiver, so both the receiver form the type checker admits
// (`geo.inFence("yard")`) and any future overload of the same function are covered by one test.
func referencesFences(a *celast.AST) bool {
	if a == nil {
		return false
	}
	root := celast.NavigateAST(a)
	found := celast.MatchDescendants(root, func(e celast.NavigableExpr) bool {
		return e.Kind() == celast.CallKind && e.AsCall().FunctionName() == FuncInFence
	})
	return len(found) > 0
}
