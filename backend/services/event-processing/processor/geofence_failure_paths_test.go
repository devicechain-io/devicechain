// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/devicechain-io/dc-event-processing/internal/geofence"
	"github.com/devicechain-io/dc-event-processing/internal/runtime"
	"github.com/devicechain-io/dc-microservice/svcclient"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

// testDetectMetrics builds a metrics recorder on a PRIVATE counter rather than through
// newDetectMetrics, which registers on the global promauto registry and would panic on the
// second test to build one.
//
// It fills only the field these tests read. Every recorder on detectMetrics is nil-safe against
// a nil RECEIVER, not against a nil field, so a future change that made the fence-set fact path
// touch another metric would panic here — loudly, which is the right failure. A partially-filled
// struct that silently absorbed the call would be the wrong one.
func testDetectMetrics(t *testing.T) *detectMetrics {
	t.Helper()
	return &detectMetrics{
		fenceSetPointerUnresolved: prometheus.NewCounter(
			prometheus.CounterOpts{Name: "test_detect_fence_set_pointer_unresolved_total"}),
	}
}

// The happy path of the pointer fact is covered next door. This file is the other half: every
// way it can FAIL, and the decision taken at each one. Those decisions are not obvious —
// acking a fact the archive would not answer, erroring rather than returning a short set,
// refusing a page that reports no total — and an untested decision is a decision nobody can
// change safely.

// ── a transport that fails on demand ─────────────────────────────────────────────────────────

// scriptedExec is a fenceSetExec whose every response is dictated by the test: the JSON "data"
// object to decode, or an error to return. It stands in for device-management at the ONE seam
// the walk actually depends on, which is what lets a test produce wire conditions a real server
// would not produce on request — a missing totalRecords, a page that goes empty early, a
// response the cap refuses.
type scriptedExec struct {
	t     *testing.T
	steps []scriptedStep
	calls int
	// vars records the pagination each call asked for, so a test can assert on the page SIZES
	// the walk chose rather than only on what it returned.
	vars []map[string]any
}

type scriptedStep struct {
	data string
	err  error
}

func (x *scriptedExec) run(_ context.Context, _ string, _ string, vars map[string]any, out any) error {
	x.t.Helper()
	if p, ok := vars["pagination"].(map[string]any); ok {
		x.vars = append(x.vars, p)
	}
	if x.calls >= len(x.steps) {
		x.t.Fatalf("the walk asked for response %d but the script has %d", x.calls+1, len(x.steps))
	}
	step := x.steps[x.calls]
	x.calls++
	if step.err != nil {
		return step.err
	}
	return decodeInto(x.t, step.data, out)
}

func decodeInto(t *testing.T, data string, out any) error {
	t.Helper()
	return json.Unmarshal([]byte(data), out)
}

// pageSizesAsked reports the page size of each request, in order.
func (x *scriptedExec) pageSizesAsked() []int32 {
	sizes := make([]int32, 0, len(x.vars))
	for _, v := range x.vars {
		if n, ok := v["pageSize"].(int32); ok {
			sizes = append(sizes, n)
		}
	}
	return sizes
}

// snapshotJSON builds a geoFenceSetSnapshot "data" object with the given fences and total.
// total is a STRING so a test can write "null" — the wire condition a Go int cannot express and
// the one the reader is required to refuse.
func snapshotJSON(version int, total string, tokens ...string) string {
	results := ""
	for i, tok := range tokens {
		if i > 0 {
			results += ","
		}
		results += fmt.Sprintf(`{"token":%q,"geometry":%q}`, tok,
			`{"kind":"POLYGON_2D","geometry":{"type":"Polygon","coordinates":[[[0,0],[1,0],[1,1],[0,0]]]}}`)
	}
	return fmt.Sprintf(`{"geoFenceSetSnapshot":{"version":%d,"fences":{"results":[%s],`+
		`"pagination":{"pageStart":1,"pageEnd":%d,"totalRecords":%s}}}}`,
		version, results, len(tokens), total)
}

// ── 1. the wire conditions the reader must refuse ────────────────────────────────────────────

// A page that reports no totalRecords is refused, not read as zero.
//
// 🔴 THE PR BODY CALLS THIS OUT BY NAME AND NOTHING TESTED IT. SearchResultsPagination's fields
// are nullable Ints; decoded into a plain int32 a missing total is zero, the walk concludes it
// has the whole set after one page, and a hundred fences resolve to twenty-five. Downstream
// nothing can tell a truncated fence set from a small one, so it would evaluate quietly and
// wrongly forever. The refusal is the only thing standing between those two readings.
func TestFenceSetPageWithNoTotalIsRefused(t *testing.T) {
	x := &scriptedExec{t: t, steps: []scriptedStep{
		{data: snapshotJSON(7, "null", "a", "b")},
	}}
	set, err := fetchSnapshotAt(context.Background(), x.run, "acme", 7)
	if err == nil {
		t.Fatalf("a page with no totalRecords was accepted, yielding %d fences", set.Len())
	}
	if set != nil {
		t.Errorf("a refused read returned a non-nil set (%d fences); the caller turns nil into "+
			"ErrNoFenceSet and a set into truth", set.Len())
	}
}

// A page that goes empty while the total says there is more is refused, not returned short.
//
// The direction is the whole point: returning the accumulator here would hand back a fence set
// that is real, well-formed, and missing fences — which reads downstream as a tenant with fewer
// fences than it has, i.e. a rule that never fires for the missing ones.
func TestFenceSetShortPageIsRefusedRatherThanTruncated(t *testing.T) {
	x := &scriptedExec{t: t, steps: []scriptedStep{
		{data: snapshotJSON(7, "5", "a", "b")}, // claims 5, gives 2
		{data: snapshotJSON(7, "5")},           // then nothing
	}}
	set, err := fetchSnapshotAt(context.Background(), x.run, "acme", 7)
	if err == nil {
		t.Fatalf("a set that stopped answering was returned as complete with %d fences", set.Len())
	}
	if set != nil {
		t.Errorf("a refused read returned a non-nil set of %d fences", set.Len())
	}
	if x.calls != 2 {
		t.Errorf("the walk spent %d responses, want 2 (the short page must stop it, not retry)", x.calls)
	}
}

// A peer that never completes the set is stopped by the response budget rather than looping
// forever. The script answers the same non-empty page every time, so the accumulator grows but
// never reaches a total that recedes ahead of it.
func TestFenceSetReadStopsAtTheResponseBudget(t *testing.T) {
	steps := make([]scriptedStep, maxFenceSetResponses+8)
	for i := range steps {
		steps[i] = scriptedStep{data: snapshotJSON(7, "1000000", "a")}
	}
	x := &scriptedExec{t: t, steps: steps}
	if _, err := fetchSnapshotAt(context.Background(), x.run, "acme", 7); err == nil {
		t.Fatal("a peer that never completes the set was read to completion")
	}
	if x.calls != maxFenceSetResponses {
		t.Errorf("the walk spent %d responses, want exactly the %d-response budget",
			x.calls, maxFenceSetResponses)
	}
}

// ── 2. the halving retry ─────────────────────────────────────────────────────────────────────

// A response the peer refuses for being too large makes the walk HALVE its page size and try
// again, all the way down to one fence.
//
// 🔴 THIS IS WHAT MAKES THE READ TOTAL, AND A FENCE-COUNTED PAGE SIZE CANNOT DO IT. Bytes per
// fence are not bounded by the vertex count — a coordinate is a JSON number of any length and a
// position may carry extra ordinates — so any fixed page size is a guess that some stored fence
// set defeats. The script refuses everything above a page size of one, which is the worst case
// a reader has to survive; below it there is nothing left to halve, which is precisely why
// device-management now bounds a single fence's bytes.
func TestFenceSetReadHalvesItsPageSizeUntilTheResponseFits(t *testing.T) {
	tooBig := fmt.Errorf("%w: synthetic", svcclient.ErrResponseTooLarge)
	steps := []scriptedStep{
		{err: tooBig},                     // 25
		{err: tooBig},                     // 12
		{err: tooBig},                     // 6
		{err: tooBig},                     // 3
		{data: snapshotJSON(7, "2", "a")}, // 1: page 1
		{data: snapshotJSON(7, "2", "b")}, // 1: page 2
	}
	x := &scriptedExec{t: t, steps: steps}
	set, err := fetchSnapshotAt(context.Background(), x.run, "acme", 7)
	if err != nil {
		t.Fatalf("the walk gave up instead of halving: %v", err)
	}
	if set.Len() != 2 {
		t.Fatalf("the halved walk produced %d fences, want 2", set.Len())
	}
	for _, tok := range []string{"a", "b"} {
		if set.Fence(tok) == nil {
			t.Errorf("fence %q is missing after the halving retry", tok)
		}
	}
	want := []int32{25, 12, 6, 3, 1, 1}
	got := x.pageSizesAsked()
	if len(got) != len(want) {
		t.Fatalf("the walk asked for page sizes %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("the walk asked for page sizes %v, want %v", got, want)
		}
	}
}

// When even a page of ONE is refused, the walk stops and reports it. There is nothing left to
// halve, so a reader cannot recover — which is the argument for bounding a single fence's bytes
// at authoring, and this test is what would notice if that argument were ever quietly dropped.
func TestFenceSetReadGivesUpWhenEvenOneFenceIsTooLarge(t *testing.T) {
	tooBig := fmt.Errorf("%w: synthetic", svcclient.ErrResponseTooLarge)
	steps := make([]scriptedStep, 6)
	for i := range steps {
		steps[i] = scriptedStep{err: tooBig}
	}
	x := &scriptedExec{t: t, steps: steps}
	_, err := fetchSnapshotAt(context.Background(), x.run, "acme", 7)
	if !errors.Is(err, svcclient.ErrResponseTooLarge) {
		t.Fatalf("a set no page size can carry reported %v, want the cap error unchanged so the "+
			"operator sees the real cause", err)
	}
	if got := x.pageSizesAsked(); len(got) != 5 || got[len(got)-1] != 1 {
		t.Errorf("the walk asked for page sizes %v; it must try all the way down to 1 before "+
			"giving up", got)
	}
}

// An error that is NOT the cap is terminal: the walk does not halve its way through a transport
// outage or a refused authority, which would turn one failure into five.
func TestFenceSetReadDoesNotHalveOnAnUnrelatedError(t *testing.T) {
	boom := errors.New("device-management is down")
	x := &scriptedExec{t: t, steps: []scriptedStep{{err: boom}}}
	_, err := fetchSnapshotAt(context.Background(), x.run, "acme", 7)
	if !errors.Is(err, boom) {
		t.Fatalf("got %v, want the transport error unchanged", err)
	}
	if x.calls != 1 {
		t.Errorf("the walk spent %d responses on a non-cap error, want 1", x.calls)
	}
}

// ── 3. the consumer's decision when the archive will not answer ───────────────────────────────

// failingFenceSource answers every archive read with the same error.
type failingFenceSource struct {
	err   error
	calls int
}

func (s *failingFenceSource) FenceSetAt(context.Context, string, int32) (*geofence.FenceSet, error) {
	s.calls++
	return nil, s.err
}

// nilFenceSource returns neither a set nor an error — a broken implementation of the contract,
// and the one shape that would install nil as a fence set if it were not caught.
type nilFenceSource struct{ calls int }

func (s *nilFenceSource) FenceSetAt(context.Context, string, int32) (*geofence.FenceSet, error) {
	s.calls++
	return nil, nil
}

var (
	_ runtime.FenceSetSource = (*failingFenceSource)(nil)
	_ runtime.FenceSetSource = (*nilFenceSource)(nil)
)

// pointerFactFor mints an oversized set and returns the pointer fact device-management published
// for it, so the consumer tests below run on a real fact rather than a hand-written one.
func pointerFactFor(t *testing.T) ([]byte, int32) {
	t.Helper()
	_, facts := ceilingFenceSet(t, nil)
	raw, ev := lastFact(t, facts)
	if !ev.FencesOmitted {
		t.Fatalf("fixture: the last fact is not a pointer fact")
	}
	return raw, ev.Version
}

// A pointer fact whose archive read FAILS is acked, counted, and installs nothing.
//
// Each of those three is a decision. Acking rather than leaving it to redeliver, because
// redelivery re-runs the same read against the same unavailable peer on the broker's schedule
// while the reconcile sweep already exists to repair exactly this, and because a permanently
// unreadable version would otherwise park the stream behind it. Counting, because containment
// eval errors are the symptom every geofence fault shares and cannot say which one this is.
// Installing nothing, because the alternative — an empty set — reads as "this tenant has no
// fences" and never fires again.
func TestPointerFactWithAFailingArchiveIsAckedAndCounted(t *testing.T) {
	raw, version := pointerFactFor(t)
	src := &failingFenceSource{err: errors.New("device-management is down")}

	w := &captureWriter{}
	rp := newFenceProcessor(t, fenceRuleReg(t, "acme", "p@1", "yard"), w, nil)
	rp.fenceView = runtime.NewFenceSetView()
	rp.VersionedFenceSets = src
	rp.metrics = testDetectMetrics(t)

	ack := &fakeAck{}
	if !rp.handleFenceSetFact(fenceFactMsg("acme", raw, ack)) {
		t.Fatal("handleFenceSetFact reported shutdown")
	}
	if src.calls != 1 {
		t.Errorf("the archive was consulted %d times, want 1", src.calls)
	}
	if ack.acks != 1 {
		t.Errorf("the fact was acked %d times, want 1 — an unreadable version left unacked "+
			"redelivers forever and parks the stream behind it", ack.acks)
	}
	if n := drainFenceUpdates(rp); n != 0 {
		t.Errorf("%d fence sets were marshalled onto the loop after a failed archive read; "+
			"installing anything here would present a read failure as the tenant's fences", n)
	}
	if got := testutil.ToFloat64(rp.metrics.fenceSetPointerUnresolved); got != 1 {
		t.Errorf("the unresolvable pointer was counted %v times, want 1 — without it the only "+
			"signal is a containment eval error, which cannot say why", got)
	}
	if held := rp.fenceView.RetainedVersions("acme"); len(held) != 0 {
		t.Errorf("the projection holds %v after a failed read, want nothing (version %d must "+
			"stay unresolvable, not become empty)", held, version)
	}
}

// A source that returns neither a set nor an error is treated as a failure, not installed.
func TestPointerFactWithAnEmptyArchiveAnswerIsCounted(t *testing.T) {
	raw, _ := pointerFactFor(t)
	src := &nilFenceSource{}

	rp := newFenceProcessor(t, fenceRuleReg(t, "acme", "p@1", "yard"), &captureWriter{}, nil)
	rp.fenceView = runtime.NewFenceSetView()
	rp.VersionedFenceSets = src
	rp.metrics = testDetectMetrics(t)

	ack := &fakeAck{}
	if !rp.handleFenceSetFact(fenceFactMsg("acme", raw, ack)) {
		t.Fatal("handleFenceSetFact reported shutdown")
	}
	if n := drainFenceUpdates(rp); n != 0 {
		t.Errorf("%d fence sets were installed from a nil archive answer", n)
	}
	if got := testutil.ToFloat64(rp.metrics.fenceSetPointerUnresolved); got != 1 {
		t.Errorf("a nil archive answer was counted %v times, want 1", got)
	}
	if ack.acks != 1 {
		t.Errorf("the fact was acked %d times, want 1", ack.acks)
	}
}

// With NO archive seam wired at all, a pointer fact is reported through the same counted path
// rather than through a branch of its own.
//
// The branch it used to have was unreachable in production — fenceView is built only when the
// seam exists, and buildFenceSetSeam returns both halves or neither — so its error log and its
// metric increment read as coverage of a state nothing could reach. Folded into the ordinary
// failure path it is reachable, reported once, and tested here.
func TestPointerFactWithNoArchiveSeamIsCounted(t *testing.T) {
	raw, _ := pointerFactFor(t)

	rp := newFenceProcessor(t, fenceRuleReg(t, "acme", "p@1", "yard"), &captureWriter{}, nil)
	rp.fenceView = runtime.NewFenceSetView()
	rp.VersionedFenceSets = nil
	rp.metrics = testDetectMetrics(t)

	ack := &fakeAck{}
	if !rp.handleFenceSetFact(fenceFactMsg("acme", raw, ack)) {
		t.Fatal("handleFenceSetFact reported shutdown")
	}
	if got := testutil.ToFloat64(rp.metrics.fenceSetPointerUnresolved); got != 1 {
		t.Errorf("an unwired archive seam was counted %v times, want 1", got)
	}
	if ack.acks != 1 {
		t.Errorf("the fact was acked %d times, want 1", ack.acks)
	}
	if n := drainFenceUpdates(rp); n != 0 {
		t.Errorf("%d fence sets were installed with no archive to read them from", n)
	}
}

// With the projection DISABLED entirely (no fenceView), a pointer fact is acked and NOT counted.
// That is the counterweight to the three tests above: this deployment does no containment at
// all, so there is nothing degraded to report, and counting it would make an unconfigured
// instance look like a failing one.
func TestPointerFactWithNoProjectionIsAckedWithoutCounting(t *testing.T) {
	raw, _ := pointerFactFor(t)

	rp := newFenceProcessor(t, fenceRuleReg(t, "acme", "p@1", "yard"), &captureWriter{}, nil)
	rp.fenceView = nil
	rp.metrics = testDetectMetrics(t)

	ack := &fakeAck{}
	if !rp.handleFenceSetFact(fenceFactMsg("acme", raw, ack)) {
		t.Fatal("handleFenceSetFact reported shutdown")
	}
	if got := testutil.ToFloat64(rp.metrics.fenceSetPointerUnresolved); got != 0 {
		t.Errorf("a deployment with containment switched off counted %v unresolvable pointers; "+
			"an unconfigured instance must not look like a failing one", got)
	}
	if ack.acks != 1 {
		t.Errorf("the fact was acked %d times, want 1", ack.acks)
	}
}
