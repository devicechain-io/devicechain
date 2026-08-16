// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package downlink

import (
	"context"
	"slices"

	"github.com/rs/zerolog/log"
)

// commandQuerier is the narrow slice of svcclient.Client the drain fetcher needs: one
// GraphQL query against command-delivery. downlink depends on the interface so the
// fetcher is unit-testable without a live command-delivery or a minted service token.
type commandQuerier interface {
	Query(ctx context.Context, baseURL, tenant, query string, variables map[string]any, out any) error
}

// statusHeld and statusParked are command-delivery lifecycle states, duplicated here as a
// wire contract (like deliveryEnvelope's JSON tags) rather than imported — downlink does
// not depend on the command-delivery module. BOTH MUST stay equal to their
// command-delivery/model counterparts (CommandHeld, CommandParked); drainStatuses below is
// the single definition everything in this package derives from.
//
// 🔴 NOTHING ENFORCES THAT "MUST", AND NOTHING IN THIS MODULE CAN. The two modules do not
// import each other, so there is no compiler check and no test comparing these literals to
// CommandHeld/CommandParked — the "MUST" is a request to a future editor, and it used to be
// the whole of the mechanism. What stands behind it now is DETECTION, not prevention, and the
// difference is worth being exact about: Pending counts the rows its defensive drainable()
// re-check discards and warns once per wake, naming the offending statuses. A rename on the
// other side still ships, and every affected device still loses its entire backlog on the
// wake it happens. What changed is that the loss is AUDIBLE. Without the warn the symptom is
// an empty slice, which is indistinguishable from a device with nothing queued — no error, no
// metric, nothing to notice.
//
// There is deliberately no statusSent. One was declared here and, once SENT left the drain
// set, went unreferenced — a dead constant, which Go does not complain about. It was not
// neutral clutter: an identifier for the one state this package must NEVER dispatch out of
// is exactly what a later edit reaches for when putting it back into drainStatuses. Why
// SENT is excluded is argued at length below, and that argument needs no constant.
const (
	statusHeld   = "HELD"
	statusParked = "PARKED"
)

// drainStatuses is the SET of states a waking device's backlog lives in. The server-side
// predicate now lives in command-delivery's drainableCommands resolver, so this list is this
// package's INDEPENDENT copy of the same set — kept because the defensive re-check below is
// what stands between a contract drift and a terminal command re-actuating a device, and
// asserted against literal wire values in the tests rather than against itself.
//
// Both states are here because they are the same backlog reached by two different routes,
// and both are states in which the PLATFORM STILL HOLDS THE COMMAND:
//
//   - HELD is what the device's ABSENCE produced: command-delivery knew the device was not
//     reachable and deliberately withheld dispatch rather than publishing into the void.
//     This is where a sleeping device's queue accumulates.
//   - PARKED is what THIS dispatcher produced: the device read as present (it is
//     registered), so command-delivery published, and we found no live conn and handed the
//     command back. It is the queue-mode case — registered and asleep.
//
// 🔴 SENT USED TO BE IN THIS LIST, AND REMOVING IT IS THE POINT OF THE CHANGE THAT ADDED
// PARKED. A SENT row was ambiguous: either a hold awaiting this drain, or a command
// dispatched moments ago and still unanswered — and the query could not tell them apart, so
// this package dispatched out of SENT WITHOUT CLAIMING, which was the platform's only
// unclaimed dispatch. Nothing but a 60-second per-pod dedup cache stood between that and
// actuating a device twice. Now the ambiguity is resolved before the query runs: a command
// that went nowhere is PARKED, and every row this drain sees is claimed before it actuates.
//
// ⚠️ ON CUTOVER, ROWS ALREADY SITTING IN SENT ARE NOT DRAINED. They are the ack-dropped
// backlog of the previous build, and they now ride their TTL to TIMEOUT instead of being
// delivered on the next wake. That is acceptable only because an instance is RECREATED
// rather than migrated pre-GA; it is stated here rather than discovered as "the drain
// broke" on an upgraded dev cluster.
//
// QUEUED stays excluded, and the original reason still holds: a QUEUED row is ≤ one sweep
// old and command-delivery's own redelivery will publish it to the LIVE path, so draining it
// here too would double-dispatch. A terminal row is done.
var drainStatuses = []string{statusHeld, statusParked}

// drainable reports whether a fetched row's status is one this package may dispatch. It reads
// drainStatuses so every place in this package that decides what "drainable" means reads one
// list — a status renamed here and not there cannot leave a row that the query returns but the
// loop discards (a device whose whole backlog vanishes).
//
// That is the WITHIN-package half. The cross-module half — this list drifting from
// command-delivery's — cannot be prevented from here, so Pending counts what this function
// rejects and says so; see the note on statusHeld.
func drainable(status string) bool {
	return slices.Contains(drainStatuses, status)
}

// maxDrainPerWake bounds the per-wake DISPATCH: a waking device drains at most this many
// of its OLDEST backlogged commands (drainStatuses), the remainder on its next
// Register/Update. It is the
// device-edge flood governor (ADR-075 L4b) — a REACT send-command storm (the programmatic
// flood origin, not operators) cannot slam a constrained radio with an unbounded burst the
// instant it wakes.
//
// It is ALSO the page size now: drainableCommands orders oldest-first in the database, so the
// N rows it returns ARE the oldest N. There is deliberately no over-fetch, and the two ways a
// row can be skipped inside the drain loop do NOT create one:
//
//   - the per-pod dedupe cache (dispatcher.go) suppresses a row the live path just dispatched
//     — but such a row is SENT, so it is not in drainStatuses and was never on this page;
//   - a LOST claim means another actor already moved the row out of the dispatchable set, so
//     it is likewise no longer drainable.
//
// In both cases the skipped row is one that has left the set, not a slot stolen from a row
// that still needs delivering. What is genuinely left over — a device with a deeper backlog
// than this cap — drains on its next Register/Update, which was always true.
const maxDrainPerWake = 32

// drainQuery pulls a device's drainable backlog: the commands in drainStatuses (HELD or
// PARKED) that are not past their expiry horizon, OLDEST FIRST, bounded by `limit`. All three
// — the status set, the horizon and the ordering — are the SERVER's, evaluated in the
// database against the server's clock; this package no longer sorts, filters or re-clocks
// anything it gets back.
//
// Field names are pinned to command-delivery's schema; the graphql-go fork rejects an unknown
// field sent through a variable, so a typo here fails the call loudly rather than silently
// returning a half-populated row (CLAUDE.md, the forked-dependency note). Note the bare list:
// drainableCommands is not a paged query, so there is no results/pagination envelope.
const drainQuery = `query($deviceToken: String!, $limit: Int) {
  drainableCommands(deviceToken: $deviceToken, limit: $limit) {
    token name payload status
  }
}`

// DrainCommand is one backlogged command fetched for a waking device, mapped to exactly
// what the dispatcher's executor consumes: the command token (to correlate the response
// back to the persisted command) plus the command name and the raw JSON payload bytes
// the CoAP op mapping reads. It is byte-for-byte the same (name, payload) the live
// deliveryEnvelope yields, so the drain reuses the identical dispatch path.
//
// Status is carried — rather than reduced to a Held bool here — because it is the honest
// datum: the row's own lifecycle state, not this package's interpretation of it.
//
// 🔴 It no longer decides whether to CLAIM: every drained row is claimed, with no exception
// (see claim() in dispatcher.go). It used to, and the exemption it granted — a SENT row has
// already left the dispatchable set, so it needs no claim — was the platform's only unclaimed
// dispatch. The premise failed because SENT also meant "published and nobody was home". Those
// rows are PARKED now, and PARKED is claimable, so both drainable states take the same path.
// The field stays because a status is still what a reader wants to see, and because a boolean
// would have to be recomputed (and could be recomputed differently) the day a third
// non-terminal state appears.
type DrainCommand struct {
	Token   string
	Name    string
	Payload []byte
	Status  string
}

// drainRow decodes one row of the drainableCommands query. Payload is nullable (GraphQL
// String). There is no id and no expiresAt: nothing here sorts or expires any more, and a
// field selected but unused is a field a reader has to disprove.
type drainRow struct {
	Token   string  `json:"token"`
	Name    string  `json:"name"`
	Payload *string `json:"payload"`
	Status  string  `json:"status"`
}

// drainResponse decodes the query's BARE list — drainableCommands is not paged, so there is
// no results/pagination envelope to unwrap.
type drainResponse struct {
	DrainableCommands []drainRow `json:"drainableCommands"`
}

// CommandFetcher reads a waking device's backlogged commands — HELD or PARKED, see
// drainStatuses — from command-delivery so the leader can drain them to the now-live CoAP
// device (ADR-075 L4b, Architecture D). The durable hold is command-delivery's Postgres row:
// a command withheld for a device already known absent sits in HELD, and one that was
// published and handed back because this dispatcher found no live session sits in PARKED,
// until it is delivered (this) or reaches its TTL horizon. This is the read side of that
// hold; it builds no second source of truth.
type CommandFetcher struct {
	client  commandQuerier
	baseURL string
	max     int // per-wake dispatch cap; sent as the query's `limit` (the server returns the oldest N)
}

// NewCommandFetcher builds a fetcher over the command-delivery GraphQL client + URL.
func NewCommandFetcher(client commandQuerier, baseURL string) *CommandFetcher {
	return &CommandFetcher{client: client, baseURL: baseURL, max: maxDrainPerWake}
}

// Pending returns a waking device's backlogged commands — those in drainStatuses (HELD or
// PARKED), not past their expiry horizon, OLDEST FIRST, at most maxDrainPerWake of them. Every
// one of those four properties is the SERVER's: drainableCommands applies the status set, the
// horizon, the ORDER BY and the limit in the database, and this function hands the rows on in
// the order it received them.
//
// 🔴 In particular there is no `now` argument and no client-side expiry compare any more. The
// horizon is evaluated against the DATABASE's clock, not this pod's: a skewed pod clock used to
// be able to drop a still-live command (never delivered, no error anywhere), and the client
// predicate (`exp <= now`) disagreed with command-delivery's own sweep (`expires_at < now`) at
// the exact instant of expiry, so the two halves of the platform could classify the same row
// differently. One clock, one predicate, one answer.
//
// A device with more commands than the cap drains the rest on subsequent wakes, as each drained
// command leaves the drainable set. A device with none pending is the overwhelmingly common case
// (one mostly-empty query per Register/Update) and returns an empty slice, not an error.
func (f *CommandFetcher) Pending(ctx context.Context, tenant, deviceToken string) ([]DrainCommand, error) {
	// 🔴 This and command-delivery's schema MUST land together: drainableCommands is a distinct
	// query, so against an older command-delivery this call fails loudly (a counted drain error,
	// retried on the next wake) rather than silently degrading.
	vars := map[string]any{"deviceToken": deviceToken, "limit": f.max}
	var resp drainResponse
	if err := f.client.Query(ctx, f.baseURL, tenant, drainQuery, vars, &resp); err != nil {
		return nil, err
	}

	rows := resp.DrainableCommands
	out := make([]DrainCommand, 0, min(len(rows), f.max))
	var dropped int
	var droppedStatuses []string // distinct, in the order first seen
	for _, r := range rows {
		if len(out) >= f.max {
			break // defensive: the server clamps to `limit`, but never dispatch more than the cap
		}
		// Defensive: the server already filtered on the drainable states, but never hand a row
		// outside that set to dispatch even if the contract ever drifts (a terminal command must
		// not re-fire). A status SET is static, so re-checking it costs nothing and cannot go
		// stale the way a clock comparison can — which is why this guard survives and the expiry
		// one did not.
		//
		// 🔴 A ROW ARRIVING HERE IS EVIDENCE, NOT NOISE, AND THIS GUARD USED TO THROW IT AWAY.
		// drainableCommands applies the SAME predicate server-side before returning anything, so
		// a row this rejects means the two copies of that predicate disagree — there is no benign
		// cause. The consequence is the quietest failure this package has: every row rejected,
		// Pending returns an empty slice, and a device's whole backlog stops being delivered
		// while looking exactly like a device with nothing queued. So the rejections are counted
		// and reported below rather than silently skipped.
		if !drainable(r.Status) {
			dropped++
			if !slices.Contains(droppedStatuses, r.Status) {
				droppedStatuses = append(droppedStatuses, r.Status)
			}
			continue
		}
		var payload []byte
		if r.Payload != nil {
			payload = []byte(*r.Payload)
		}
		// Status rides along as the row's OWN lifecycle state, not as an instruction to anyone.
		// Nothing downstream branches on it — every drained row is claimed, with no exception
		// (claim() in dispatcher.go) — and a SENT row cannot reach this line at all, because
		// drainable() rejected it above. What it buys is DIAGNOSIS: it is the field the
		// no-claimer refusal logs, so an operator can see which half of a backlog went
		// undispatched. Dropping it would cost that field, which is the shape a fake omitting a
		// field produces — the caller's omission hides.
		out = append(out, DrainCommand{Token: r.Token, Name: r.Name, Payload: payload, Status: r.Status})
	}
	// ONE line per wake, not one per row, and that is the point rather than tidiness: this fires
	// only on contract drift, and drift is not per-device — it hits EVERY waking device on the
	// instance at once. A warn per row would emit a backlog's worth of identical lines per
	// device per Register/Update and bury the signal in itself, which is the same reason the
	// claim-failure path logs at debug. The distinct STATUSES are the whole diagnostic: the
	// offending name is what says which side renamed what.
	//
	// It stays FAIL-CLOSED and non-fatal. The unrecognised rows are skipped (a terminal command
	// must not re-fire) and the understood ones in the same response are still returned, because
	// turning drift into an error return would stop delivering the rows this build does
	// understand — trading a partial outage for a total one.
	if dropped > 0 {
		log.Warn().
			Str("tenant", tenant).
			Str("device", deviceToken).
			Int("dropped", dropped).
			Strs("statuses", droppedStatuses).
			Msg("Dropped LwM2M backlog rows whose status this build does not recognise as drainable; those commands were NOT delivered to the device. command-delivery applies the same predicate before returning a row, so this can only mean the two disagree about the drainable status set — a version/contract mismatch between this adapter and command-delivery, not a device fault.")
	}
	return out, nil
}
