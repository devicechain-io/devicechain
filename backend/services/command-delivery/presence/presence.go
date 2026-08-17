// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package presence answers one question for the delivery sweep: for each of these
// devices, is dispatching a command to it worth doing right now?
//
// It exists because a command is a PHYSICAL ACTUATION and, over MQTT, a publish reaches
// only a device that is connected and subscribed at that instant — the broker does not
// hold it. Dispatching to an absent device therefore does not merely fail; it fails
// SILENTLY, and command-delivery has already recorded the command as sent. The device is
// then blamed by a TIMEOUT for a command it was never given.
package presence

import (
	"context"
	"fmt"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/transport"
)

// Verdict is what the gate does with one command.
type Verdict int

const (
	// Dispatch means send it now. This is the DEFAULT and the fail-open direction:
	// the gate may only withhold on positive evidence of absence.
	Dispatch Verdict = iota
	// Hold means the device is authoritatively absent and can be expected back, so the
	// command waits for its return rather than being thrown at a broker that will drop it.
	Hold
	// Undeliverable means this device's transport cannot carry a command AT ALL, so
	// waiting would be a lie of a different kind — the device returns and delivery still
	// cannot happen.
	Undeliverable
)

// State is the projection's answer for one device, reduced to what the gate needs.
type State struct {
	Active   bool
	Asserted bool
	Source   string
	Known    bool // false when the projection has no row for this device
}

// Reader reads the live presence projection for a set of devices in one tenant.
//
// It returns what it could read TOGETHER WITH an error when any part of the read failed:
// both values are meaningful, and neither is a consolation prize. A chunked read that
// loses one chunk still gates every device the other chunks covered.
type Reader interface {
	StatesFor(ctx context.Context, deviceTokens []string) (map[string]State, error)
}

// Resolved reports whether a read actually learned anything about a device — the question
// a caller must ask before acting on silence.
//
// 🔴🔴 THE SAME ABSENCE MEANS TWO INCOMPATIBLE THINGS EITHER SIDE OF AN ERROR, AND THE
// CALLERS OWE THEM OPPOSITE ACTIONS:
//
//   - on a CLEAN read, a device absent from states is a device the projection holds no row
//     for. That is a real answer, and the hold reconciler RELEASES on it — deliberately,
//     so the sweep re-decides in one place.
//   - on a FAILED read the identical absence is not an answer at all, and releasing on it
//     is how a transient device-state blip returns a whole page of withheld commands to
//     the dispatch path, to be published at brokers where those devices are absent. Every
//     one lands SENT and then TIMEOUT: the record that blames the device for the
//     platform's outage.
//
// 🔑 THE ERROR IS THE ONLY CHANNEL THAT CAN CARRY THIS, which is worth stating because the
// first attempt used a second one. That version had the reader also NAME the devices its
// failed chunks covered, and a review established the field could never change an answer:
// every path that named devices also returned an error, so the error alone already decided
// every case the field was consulted for. Worse, Reader is an interface — an implementation
// that returned an error while naming nothing would have slipped straight past the field
// and released the batch. One rule that cannot be forgotten beats two where the weaker one
// looks like the protection.
//
// The cost is small and one-directional: on a partially failed read, a device that a
// SUCCESSFUL chunk found no row for stays held one extra pass instead of being released
// immediately. Held slightly too long self-corrects on the next clean read; released
// wrongly does not.
func Resolved(states map[string]State, token string, err error) bool {
	if _, known := states[token]; known {
		return true
	}
	return err == nil
}

// nonDeliveringTransports names the sources positively known to have NO command path at
// all. It is a DENY list, not the complement of an allow list, and that is deliberate: a
// source nobody has classified must fall through to Dispatch rather than be condemned by
// silence. The gate withholds only where the absence of a delivery path is established.
//
// 🔴🔴 THE ENTRY THAT MATTERS IS SPARKPLUG, AND IT IS NOT A BUG BEING WORKED AROUND.
// Sparkplug has no command egress by construction: nothing bridges the device-commands
// stream to a Sparkplug host, the host publishes only the Node Control/Rebirth control
// toward nodes, and its devices sit on the CUSTOMER's MQTT infrastructure rather than
// ours — so a publish onto our own subject reaches nothing.
//
// 🔑 A SPARKPLUG COMMAND IS THEREFORE NOT "HELD", IT IS UNDELIVERABLE, and that
// distinction is why this list exists at all. HELD means "waiting for the device to come
// back". For Sparkplug that is FALSE: the device does come back, on its next birth, and
// delivery still cannot happen. Holding would swap one lie for another — and it would
// occupy the tenant's undelivered-command ceiling for a full TTL, letting a Sparkplug
// fleet crowd out commands to devices that CAN receive them.
//
// 🔴🔴 KEYED ON THE TRANSPORT, MATCHED THROUGH transport.Of — NEVER ON THE WHOLE `source`.
// A projected source is `transport` or `transport:qualifier`, and the qualifier is chosen at
// runtime, so the whole value is not a closed vocabulary and cannot be enumerated here. This
// list held the bare word "sparkplug" and was looked up with the whole value, which no
// producer ever writes: sparkplug-ingest stamps "sparkplug:"+hostId. The lookup therefore
// missed for every real device and the Undeliverable verdict — with failUndeliverable,
// MarkUndeliverable and command_delivery_undeliverable_total behind it — could not fire at
// all, while every test passed on a hand-written bare "sparkplug" that nothing emits.
//
// 🔴 BOTH HALVES NOW COME FROM core/transport — the name from the constant, the reduction
// from transport.Of. They are minted in sparkplug-ingest and compared here, in modules that
// do not import each other, and the last time each side wrote its own the two disagreed in
// exactly the way described above. What one definition buys is that THE TWO SIDES CAN NO
// LONGER DISAGREE: change it and both move together, so the verdict stays connected.
//
// ⚠️ THAT IS NOT THE SAME AS THE COMPILER GUARDING THE VALUE, and the difference matters.
// Renaming the Go identifier breaks the build; changing the STRING breaks nothing anywhere,
// because it propagates to both sides at once — and the string is a wire contract already
// written into device_states rows that no longer match. The only tripwire for that is a test
// in core, which `go test ./...` here will never run.
//
// The one collision the reduction cannot prevent — an operator naming a source so that it
// reduces to a minted transport name — is rejected at config load by event-sources' Validate
// rather than merely described here. That was worth closing because the cost INVERTS with
// this list's polarity: against a DENY list it is self-inflicted (the operator's own commands
// stop), while against an ALLOW list the collision makes a foreign transport's device look
// like a member and the caller ACTS on a device the list was written to exclude. The
// stranded-SENT reconciler is that first allow list, and this is the groundwork it asked for.
var nonDeliveringTransports = map[transport.Name]bool{
	transport.Sparkplug: true,
}

// Decide reduces one device's projected state to a verdict.
//
// 🔴 THE `Asserted` CONJUNCT IS WHAT STOPS THIS BEING A DISASTER, AND IT IS NOT OBVIOUS.
// An INFERRED row's Active=false means only "no events for a while" — the data-silence
// sweep wrote it. That is not evidence the device cannot receive: a device reporting
// hourly is inactive for 59 minutes of every 60 and reachable throughout. Gating on
// !Active alone would hold every command to every quiet device on every transport that
// does not assert presence, which is most of them. Only an AUTHORITATIVE absence — a
// broker saying there is no connection — makes a dispatch provably futile.
//
// 🔴 AND THE EVIDENCE CAN BE STALE, which is stated here rather than discovered later.
// Two known windows leave a reachable device reading asserted-absent: a device whose plain
// broker session died while a discriminated side-connection stays subscribed, and a device
// that reconnected onto a broker node with a trailing clock (its lower session id is
// refused until the presence reconciler re-files it). In both, commands wait until that
// reconciler catches up. The gate withholds on positive evidence; the evidence is not
// infallible.
func Decide(s State) Verdict {
	if !s.Known {
		// No row at all: the platform has never been told anything about this device.
		// Dispatch, exactly as before the gate existed.
		return Dispatch
	}
	if nonDeliveringTransports[transport.Of(s.Source)] {
		return Undeliverable
	}
	if s.Asserted && !s.Active {
		return Hold
	}
	return Dispatch
}

// graphqlReader reads the projection over device-state's GraphQL API.
type graphqlReader struct {
	client GraphQLClient
	url    string
}

// GraphQLClient is the svcclient seam, kept narrow so tests need no HTTP.
type GraphQLClient interface {
	Query(ctx context.Context, url, tenant, query string, vars map[string]any, out any) error
}

// NewGraphQLReader binds a reader to device-state's GraphQL endpoint.
func NewGraphQLReader(client GraphQLClient, url string) Reader {
	return &graphqlReader{client: client, url: url}
}

const statesQuery = `query($deviceTokens: [String!]!) {
  deviceStatesByDeviceToken(deviceTokens: $deviceTokens) {
    deviceToken
    active
    presenceSource
    source
  }
}`

// readChunkSize bounds how many device tokens one presence query asks about.
//
// 🔴 IT IS A RESPONSE BOUND, NOT A REQUEST ONE. The cross-service client reads at most
// 1 MiB of any response; past that the body is cut off and the read fails. A presence
// row costs roughly 90 bytes on the wire with an ordinary device token and over 200 with
// a long one, so an unchunked read stops working somewhere between five and twelve
// thousand devices — INSIDE the fleet size a tenant is allowed to accumulate commands
// for. The failure is not a slow query; it is the delivery gate switching itself off for
// that tenant at exactly the moment a large fleet is reconnecting.
//
// 500 matches reconcilePageSize, the bound the hold reconciler already reads its side
// with. The two page the same set from opposite ends, and two numbers for one set is a
// pair to keep in step for no benefit.
const readChunkSize = 500

// readBudget bounds the WHOLE of one tenant's presence read, however many chunks it takes.
//
// 🔴 CHUNKING MULTIPLIES A HUNG PEER'S COST, AND WITHOUT THIS THE FIX WOULD HAVE TRADED
// ONE FAILURE FOR ANOTHER. The cross-service client bounds each call at ten seconds, which
// was the whole cost of the single read this replaced. Twenty chunks against a device-state
// that accepts connections and never answers is two hundred seconds — inside a sweep that
// ticks every thirty, with every other tenant queued behind it. The chunks share one
// budget so the worst case stays a property of the tenant rather than of its size.
//
// Chunks not reached inside the budget are reported unread, exactly like chunks that were
// attempted and failed: in both cases nothing was learned about those devices.
const readBudget = 15 * time.Second

// StatesFor reads the projection for a batch of this tenant's devices, in chunks.
//
// 🔑 BATCHED ON PURPOSE. The sweep holds a whole tick's worth of queued commands, and one
// round trip per command would put a network call between the platform and every dispatch
// — turning a delivery sweep into a fan-out of reads. deviceStatesByDeviceToken is the
// only multi-token presence query available, so the batch is grouped per tenant.
//
// 🔴 A DEVICE MISSING FROM THE RESPONSE IS "UNKNOWN", NOT "ABSENT". The query returns rows
// that exist, so a device the projection has never seen simply does not come back. Reading
// that as absence would hold commands for every device that has not spoken yet.
//
// 🔑 A FAILED CHUNK COSTS ONLY ITS OWN DEVICES. Every chunk is attempted even after one
// fails, and the states already read are kept rather than discarded: a device-state blip
// mid-way through a large tenant used to un-gate every command that tenant had queued, and
// now un-gates one page of them. The error is still returned — it is what tells a caller
// that the devices missing from the map may be missing because nobody could ask (see
// Resolved), and it is what the meter and the log count.
func (r *graphqlReader) StatesFor(ctx context.Context, deviceTokens []string) (map[string]State, error) {
	states := make(map[string]State, len(deviceTokens))
	if len(deviceTokens) == 0 {
		return states, nil
	}
	tenant, ok := core.TenantFromContext(ctx)
	if !ok {
		return states, fmt.Errorf("presence read requires a tenant in context")
	}
	ctx, cancel := context.WithTimeout(ctx, readBudget)
	defer cancel()

	var failed error
	for start := 0; start < len(deviceTokens); start += readChunkSize {
		end := min(start+readChunkSize, len(deviceTokens))
		chunk := deviceTokens[start:end]
		if ctx.Err() != nil {
			// The budget is spent. Attempting the rest would add ten seconds each to a
			// sweep tick that has already overrun, and would learn nothing new about a
			// peer that has already failed to answer. The devices in the chunks not
			// reached are simply absent from states, which — with the error — is exactly
			// what Resolved reads as "nothing is known about this device".
			if failed == nil {
				failed = ctx.Err()
			}
			break
		}
		if err := r.readChunk(ctx, tenant, chunk, states); err != nil {
			// Recorded, not returned: the chunks after this one are independent reads
			// and their devices are equally entitled to an answer. Only the FIRST error
			// is carried, because a tenant whose device-state is down produces one
			// error per chunk and a joined list of twenty identical failures tells an
			// operator nothing the first one did not.
			if failed == nil {
				failed = err
			}
		}
	}
	return states, failed
}

// readChunk performs one query and folds its rows into states.
func (r *graphqlReader) readChunk(ctx context.Context, tenant string, tokens []string, states map[string]State) error {
	var resp struct {
		DeviceStatesByDeviceToken []struct {
			DeviceToken    string `json:"deviceToken"`
			Active         bool   `json:"active"`
			PresenceSource string `json:"presenceSource"`
			Source         string `json:"source"`
		} `json:"deviceStatesByDeviceToken"`
	}
	if err := r.client.Query(ctx, r.url, tenant, statesQuery,
		map[string]any{"deviceTokens": tokens}, &resp); err != nil {
		return err
	}
	for _, row := range resp.DeviceStatesByDeviceToken {
		states[row.DeviceToken] = State{
			Active:   row.Active,
			Asserted: row.PresenceSource == presenceSourceAsserted,
			Source:   row.Source,
			Known:    true,
		}
	}
	return nil
}

// presenceSourceAsserted mirrors device-state's constant. It is duplicated rather than
// imported because command-delivery does not otherwise depend on that module, and a
// one-word string is a cheaper coupling than a module edge — the value is part of the
// GraphQL contract, which is the thing actually being relied on.
const presenceSourceAsserted = "ASSERTED"
