// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package presence

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
)

// chunkingClient answers each call with a row for every token it was asked about, and
// records the sizes it saw. failOn names the (zero-based) calls that must fail instead,
// which is how a test stages a device-state blip part-way through a large tenant.
type chunkingClient struct {
	failOn map[int]bool
	calls  int
	sizes  []int
	asked  [][]string
}

func (c *chunkingClient) Query(_ context.Context, _, _, _ string, vars map[string]any, out any) error {
	call := c.calls
	c.calls++
	tokens, _ := vars["deviceTokens"].([]string)
	c.sizes = append(c.sizes, len(tokens))
	c.asked = append(c.asked, tokens)
	if c.failOn[call] {
		return fmt.Errorf("device-state is unavailable")
	}
	resp, ok := out.(*struct {
		DeviceStatesByDeviceToken []struct {
			DeviceToken    string `json:"deviceToken"`
			Active         bool   `json:"active"`
			PresenceSource string `json:"presenceSource"`
			Source         string `json:"source"`
		} `json:"deviceStatesByDeviceToken"`
	})
	if !ok {
		return errors.New("unexpected out type")
	}
	for _, token := range tokens {
		resp.DeviceStatesByDeviceToken = append(resp.DeviceStatesByDeviceToken, struct {
			DeviceToken    string `json:"deviceToken"`
			Active         bool   `json:"active"`
			PresenceSource string `json:"presenceSource"`
			Source         string `json:"source"`
		}{DeviceToken: token, Active: false, PresenceSource: presenceSourceAsserted, Source: "mqtt"})
	}
	return nil
}

// tokens builds n distinct device tokens.
func tokens(n int) []string {
	out := make([]string, 0, n)
	for i := range n {
		out = append(out, fmt.Sprintf("device-%04d", i))
	}
	return out
}

// TestALargeTenantIsReadInChunks is the size fix itself.
//
// 🔴 THE BOUND BEING DEFENDED IS THE RESPONSE, NOT THE QUERY. The cross-service client
// reads at most 1 MiB and its answer past that is a FAILED read, not a shorter one — so
// an unchunked read of a large fleet does not return less presence, it returns none, and
// the delivery gate switches itself off for that tenant. This asserts the read is split
// before it can get there.
func TestALargeTenantIsReadInChunks(t *testing.T) {
	client := &chunkingClient{}
	reader := NewGraphQLReader(client, "http://device-state/graphql")
	ctx := core.WithTenant(context.Background(), "acme")

	all := tokens(readChunkSize*2 + 200)
	states, err := reader.StatesFor(ctx, all)
	if err != nil {
		t.Fatalf("StatesFor failed: %v", err)
	}
	if client.calls != 3 {
		t.Fatalf("expected 3 chunked queries for %d devices at a chunk size of %d, got %d (sizes %v)",
			len(all), readChunkSize, client.calls, client.sizes)
	}
	for i, size := range client.sizes {
		if size > readChunkSize {
			t.Fatalf("chunk %d asked about %d devices, past the %d bound", i, size, readChunkSize)
		}
	}
	// Every device must still be answered for: chunking is a transport detail, not a
	// reduction in what the caller learns.
	if len(states) != len(all) {
		t.Fatalf("chunking lost devices: asked about %d, got %d states", len(all), len(states))
	}
	for _, token := range all {
		if !Resolved(states, token, err) {
			t.Fatalf("%s was answered for and must read as resolved", token)
		}
	}
}

// TestAFailedChunkCostsOnlyItsOwnDevices is the blast-radius fix.
//
// Before chunking, one failed read meant the whole tenant's commands dispatched ungated.
// The failure is the same; what changes is that the chunks that succeeded still gate their
// own devices, while the ones that did not are absent from the map — which, together with
// the returned error, is exactly what Resolved reads as "nothing is known about this".
func TestAFailedChunkCostsOnlyItsOwnDevices(t *testing.T) {
	client := &chunkingClient{failOn: map[int]bool{1: true}}
	reader := NewGraphQLReader(client, "http://device-state/graphql")
	ctx := core.WithTenant(context.Background(), "acme")

	all := tokens(readChunkSize * 3)
	states, err := reader.StatesFor(ctx, all)
	if err == nil {
		t.Fatal("a failed chunk must still be reported as an error; the meter and the log depend on it")
	}

	// 🔑 EVERY CHUNK IS ATTEMPTED. Stopping at the first failure would throw away the
	// third chunk's devices for a problem that had nothing to do with them.
	if client.calls != 3 {
		t.Fatalf("every chunk must be attempted even after one fails, got %d calls", client.calls)
	}
	if len(states) != readChunkSize*2 {
		t.Fatalf("the surviving chunks must still be gated: expected %d states, got %d",
			readChunkSize*2, len(states))
	}
	// And it must be the RIGHT devices — a count alone would pass with the wrong page
	// dropped.
	for _, token := range client.asked[1] {
		if _, present := states[token]; present {
			t.Fatalf("device %s was in the failed chunk but has a state", token)
		}
		if Resolved(states, token, err) {
			t.Fatalf("device %s was in the failed chunk and must not read as resolved", token)
		}
	}
	for _, token := range client.asked[0] {
		if !Resolved(states, token, err) {
			t.Fatalf("device %s was in a chunk that SUCCEEDED and must read as resolved", token)
		}
	}
}

// TestAnUnreachedDeviceIsNotTheSameAsAnUnknownOne pins the distinction Resolved exists to
// draw.
//
// 🔴🔴 BOTH ARE ABSENT FROM THE MAP, AND THE TWO CALLERS OWE THEM OPPOSITE ANSWERS. The
// delivery sweep dispatches for either. The hold reconciler must RELEASE for a device the
// projection has never seen and KEEP HOLDING for one whose chunk failed. The read's ERROR
// is what separates them: with it, every absence is unknown; without it, every absence is
// a real answer.
func TestAnUnreachedDeviceIsNotTheSameAsAnUnknownOne(t *testing.T) {
	// A first chunk that succeeds, and a second — holding a single device — that fails
	// outright. Both of those devices end up absent from States; only one of them is a
	// real answer.
	client := &chunkingClient{failOn: map[int]bool{1: true}}
	reader := NewGraphQLReader(client, "http://device-state/graphql")
	ctx := core.WithTenant(context.Background(), "acme")

	all := tokens(readChunkSize + 1)
	states, err := reader.StatesFor(ctx, all)

	seen := all[0]
	neverAsked := all[readChunkSize]
	if !states[seen].Known {
		t.Fatalf("%s was answered for and must be Known", seen)
	}
	if !Resolved(states, seen, err) {
		t.Fatalf("%s was answered for and must read as resolved even though another chunk failed", seen)
	}
	// The device in the failed chunk: absent from the map, and NOT resolved — which is
	// what the hold reconciler must not act on.
	if _, present := states[neverAsked]; present {
		t.Fatalf("%s was in the failed chunk and must have no state", neverAsked)
	}
	if Resolved(states, neverAsked, err) {
		t.Fatalf("%s was in the failed chunk and must not read as resolved; otherwise the hold "+
			"reconciler cannot tell it apart from a device the projection has never seen", neverAsked)
	}
	// A device nobody asked about is in neither map, and Decide still answers Dispatch —
	// the fail-open the gate has always taken.
	if v := Decide(states["a-device-not-in-this-batch"]); v != Dispatch {
		t.Fatalf("an unknown device decided %v, want Dispatch", v)
	}
}

// TestAReadWithNoTenantAsksNothing keeps the tenant check ahead of the chunk loop: a
// chunked read that lost the check would issue one cross-tenant query per chunk instead
// of none.
//
// 🔴 AND NOTHING IT RETURNS MAY READ AS RESOLVED. This is a precondition failure, so the
// map is empty — and an empty map WITHOUT the error would say the projection has no row
// for any of these devices, which for the hold reconciler means release them all. The
// error is what makes that silence mean "nobody asked".
func TestAReadWithNoTenantAsksNothing(t *testing.T) {
	client := &chunkingClient{}
	reader := NewGraphQLReader(client, "http://device-state/graphql")
	asked := tokens(readChunkSize + 1)

	states, err := reader.StatesFor(context.Background(), asked)
	if err == nil {
		t.Fatal("a presence read with no tenant in context must fail rather than read across tenants")
	}
	if client.calls != 0 {
		t.Fatalf("a tenantless read must not reach the network at all, got %d calls", client.calls)
	}
	for _, token := range asked {
		if _, present := states[token]; present {
			t.Fatalf("%s has a state from a read that never happened", token)
		}
		if Resolved(states, token, err) {
			t.Fatalf("%s must not read as resolved after a read that never happened", token)
		}
	}
}

// budgetSpy records the deadline each chunk's context carried.
type budgetSpy struct {
	deadlines []time.Time
	hadNone   int
	sleep     time.Duration
}

func (b *budgetSpy) Query(ctx context.Context, _, _, _ string, vars map[string]any, out any) error {
	if deadline, ok := ctx.Deadline(); ok {
		b.deadlines = append(b.deadlines, deadline)
	} else {
		b.hadNone++
	}
	if b.sleep > 0 {
		select {
		case <-time.After(b.sleep):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	// 🔑 IT ANSWERS FOR ITS TOKENS, and that is not decoration. A spy that recorded the
	// deadline and returned an empty response would leave the states map empty however
	// many chunks succeeded — so an assertion about "the chunks that DID complete are
	// kept" would be measuring the fixture rather than the code. (It did, on the first
	// attempt, and failed honestly.)
	tokens, _ := vars["deviceTokens"].([]string)
	resp, ok := out.(*struct {
		DeviceStatesByDeviceToken []struct {
			DeviceToken    string `json:"deviceToken"`
			Active         bool   `json:"active"`
			PresenceSource string `json:"presenceSource"`
			Source         string `json:"source"`
		} `json:"deviceStatesByDeviceToken"`
	})
	if !ok {
		return errors.New("unexpected out type")
	}
	for _, token := range tokens {
		resp.DeviceStatesByDeviceToken = append(resp.DeviceStatesByDeviceToken, struct {
			DeviceToken    string `json:"deviceToken"`
			Active         bool   `json:"active"`
			PresenceSource string `json:"presenceSource"`
			Source         string `json:"source"`
		}{DeviceToken: token, Active: true, PresenceSource: presenceSourceAsserted, Source: "mqtt"})
	}
	return nil
}

// TestTheChunksOfOneReadShareONEBudget is what keeps chunking from multiplying a hung
// peer's cost.
//
// Chunking multiplies a hung peer's cost — see readBudget for the arithmetic. One budget
// for the whole read is what keeps the worst case a property of the tenant rather than of
// its size.
//
// 🔑 THE ASSERTION IS THAT THE DEADLINES ARE PRESENT AND IDENTICAL. Identical is what
// distinguishes one shared budget from a per-chunk timeout that would multiply exactly as
// before; present is what fails if the budget is dropped altogether. The parent context
// deliberately carries no deadline, so nothing but the code under test can supply one.
func TestTheChunksOfOneReadShareONEBudget(t *testing.T) {
	spy := &budgetSpy{}
	reader := NewGraphQLReader(spy, "http://device-state/graphql")
	ctx := core.WithTenant(context.Background(), "acme")

	if _, err := reader.StatesFor(ctx, tokens(readChunkSize*3)); err != nil {
		t.Fatalf("StatesFor failed: %v", err)
	}
	if spy.hadNone > 0 {
		t.Fatalf("%d chunk(s) ran with no deadline at all; a hung peer would cost ten seconds each", spy.hadNone)
	}
	if len(spy.deadlines) != 3 {
		t.Fatalf("expected 3 chunks, got %d", len(spy.deadlines))
	}
	for i, deadline := range spy.deadlines {
		if !deadline.Equal(spy.deadlines[0]) {
			t.Fatalf("chunk %d carries its own deadline rather than the read's shared one; "+
				"the worst case scales with the number of chunks again", i)
		}
		if left := time.Until(deadline); left > readBudget {
			t.Fatalf("chunk %d had %s left, past the %s budget for the whole read", i, left, readBudget)
		}
	}
}

// TestChunksNotReachedInTimeDoNotReadAsAnswers covers what the budget does when it runs
// out: the devices it never got to must not be mistaken for devices with no row.
//
// 🔑 UNREACHED AND FAILED ARE THE SAME FACT. In both cases nothing was learned about those
// devices, so both must reach the hold reconciler as "do not act on this" rather than as
// "the projection has no row" — which is the release direction.
func TestChunksNotReachedInTimeDoNotReadAsAnswers(t *testing.T) {
	spy := &budgetSpy{sleep: 40 * time.Millisecond}
	reader := NewGraphQLReader(spy, "http://device-state/graphql")
	// A parent budget far shorter than readBudget, so the read runs out part-way through
	// without the test waiting the real budget out.
	ctx, cancel := context.WithTimeout(core.WithTenant(context.Background(), "acme"), 60*time.Millisecond)
	defer cancel()

	asked := tokens(readChunkSize * 5)
	states, err := reader.StatesFor(ctx, asked)
	if err == nil {
		t.Fatal("a read that ran out of budget must report it")
	}
	if len(states) == 0 {
		t.Fatal("the chunks that DID complete before the budget ran out must still be kept")
	}
	if len(states) == len(asked) {
		t.Fatal("the fixture did not actually run out of budget; this test measured nothing")
	}
	// The last chunk is certainly unreached, and must not read as "the projection has no
	// row for this device".
	last := asked[len(asked)-1]
	if Resolved(states, last, err) {
		t.Fatalf("%s was never asked about and must not read as resolved", last)
	}
}

// TestResolvedSeparatesNeverSeenFromNeverAsked pins the predicate both callers lean on.
//
// 🔑 THE SAME ABSENCE, TWO ANSWERS, DECIDED BY THE ERROR. On a clean read an absent device
// is a real answer — the projection holds no row — and the reconciler releases it. On a
// failed read the identical absence is not an answer at all, and releasing on it is how a
// transient blip turns into a bulk release of withheld commands.
func TestResolvedSeparatesNeverSeenFromNeverAsked(t *testing.T) {
	states := map[string]State{"seen": {Known: true}}

	if !Resolved(states, "seen", nil) {
		t.Fatal("a device the read answered for is resolved")
	}
	if !Resolved(states, "never-seen", nil) {
		t.Fatal("on a CLEAN read, an absent device is a real answer — the projection has no row for it")
	}

	failed := errors.New("device-state is unavailable")
	if !Resolved(states, "seen", failed) {
		t.Fatal("a device a SUCCESSFUL chunk answered for stays resolved even though another chunk failed")
	}
	if Resolved(states, "never-seen", failed) {
		t.Fatal("on a FAILED read, silence is not an answer — it is the absence of one")
	}
	// The rule holds for a reader that failed while answering for nothing at all, which is
	// the shape an implementation that has not thought about the contract produces.
	if Resolved(map[string]State{}, "anything", failed) {
		t.Fatal("a read that failed and answered for nothing must resolve nothing")
	}
}
