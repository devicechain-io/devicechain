// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package downlink

import (
	"context"
	"sort"
	"strconv"
	"time"
)

// commandQuerier is the narrow slice of svcclient.Client the drain fetcher needs: one
// GraphQL query against command-delivery. downlink depends on the interface so the
// fetcher is unit-testable without a live command-delivery or a minted service token.
type commandQuerier interface {
	Query(ctx context.Context, baseURL, tenant, query string, variables map[string]any, out any) error
}

// statusHeld and statusSent are command-delivery lifecycle states, duplicated here as a
// wire contract (like deliveryEnvelope's JSON tags) rather than imported — downlink does
// not depend on the command-delivery module. BOTH MUST stay equal to their
// command-delivery/model counterparts (CommandHeld, CommandSent); drainStatuses below is
// the single definition everything in this package derives from.
const (
	statusHeld   = "HELD"
	statusSent   = "SENT"
	statusParked = "PARKED"
)

// drainStatuses is the SET of states a waking device's backlog lives in. It is BOTH the
// server-side predicate (the criteria this package sends) and the client-side re-check, so
// the two cannot drift into disagreeing about what is drainable.
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

// drainable reports whether a fetched row's status is one this package may dispatch. It
// reads drainStatuses so the client-side defensive re-check and the server-side predicate
// are literally the same list — a status renamed on one side and not the other cannot leave
// a row that the query returns but the loop silently discards (a device whose whole backlog
// vanishes, with no error anywhere).
func drainable(status string) bool {
	for _, s := range drainStatuses {
		if status == s {
			return true
		}
	}
	return false
}

// maxDrainPerWake bounds the per-wake DISPATCH: a waking device drains at most this many
// of its OLDEST backlogged commands (drainStatuses), the remainder on its next
// Register/Update. It is the
// device-edge flood governor (ADR-075 L4b) — a REACT send-command storm (the programmatic
// flood origin, not operators) cannot slam a constrained radio with an unbounded burst the
// instant it wakes.
const maxDrainPerWake = 32

// maxDrainFetch is the FETCH page size — deliberately larger than the dispatch cap. The
// commands query has no server-side ORDER BY, so pageSize=N returns an ARBITRARY N rows;
// sorting a page that is already the wrong N cannot recover the oldest ones (a firmware
// Write /5/0/1 could be left off a 32-row page while its Execute /5/0/2 is on it → dispatched
// out of order across wakes). So we fetch a large page (this equals core rdb.MaxPageSize, the
// server's own clamp — a larger request is clamped to it anyway), sort by id, THEN truncate
// to maxDrainPerWake, making the selection genuinely oldest-first. A device with more than
// this many backlogged commands is pathological (bounded by the 7-day TTL horizon); its
// overflow drains across subsequent wakes.
const maxDrainFetch = 1000

// drainQuery pulls a device's commands in a given SET of lifecycle states. Field names are
// pinned to command-delivery's schema; the graphql-go fork rejects an unknown field
// sent through a variable, so a typo here fails the call loudly rather than silently
// returning a half-populated row (CLAUDE.md, the forked-dependency note). The query
// carries NO ordering — the commands resolver has none (only PendingCommands adds
// Order("id ASC")) — so Pending sorts client-side.
const drainQuery = `query($criteria: CommandSearchCriteria!) {
  commands(criteria: $criteria) {
    results { id token name payload status expiresAt }
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

// drainRow decodes one row of the commands query. Payload/ExpiresAt are nullable
// (GraphQL String); Id is the numeric PK stringified (ID!), the enqueue-order proxy.
type drainRow struct {
	Id        string  `json:"id"`
	Token     string  `json:"token"`
	Name      string  `json:"name"`
	Payload   *string `json:"payload"`
	Status    string  `json:"status"`
	ExpiresAt *string `json:"expiresAt"`
}

type drainResponse struct {
	Commands struct {
		Results []drainRow `json:"results"`
	} `json:"commands"`
}

// CommandFetcher reads a waking device's backlogged commands — HELD or PARKED, see
// drainStatuses — from command-delivery so the leader can drain them to the now-live CoAP
// device (ADR-075 L4b, Architecture D). The durable hold is command-delivery's Postgres row:
// a command withheld for a device already known absent sits in HELD, and one that was
// published and handed back because this dispatcher found no live session sits in PARKED,
// until it is delivered (this) or reaches its TTL horizon. This is the read side of that
// hold; it builds no second source of truth.
type CommandFetcher struct {
	client    commandQuerier
	baseURL   string
	fetchSize int // page size requested from command-delivery (large, so the sort sees the oldest)
	max       int // per-wake dispatch cap (the oldest N after sorting)
}

// NewCommandFetcher builds a fetcher over the command-delivery GraphQL client + URL.
func NewCommandFetcher(client commandQuerier, baseURL string) *CommandFetcher {
	return &CommandFetcher{client: client, baseURL: baseURL, fetchSize: maxDrainFetch, max: maxDrainPerWake}
}

// Pending returns a waking device's backlogged commands — those in drainStatuses (HELD or
// PARKED) — OLDEST FIRST (by numeric id — the commands query has no server-side ordering, and
// a firmware Write /5/0/1 must not dispatch after its Execute /5/0/2). Already-expired rows
// are dropped: a command past its horizon will expire within a sweep and must never actuate
// a device late. The result is capped at the fetch page (maxDrainPerWake); a device with a
// deeper backlog drains the rest on subsequent wakes as each drained command leaves the
// drainable set. `now` is injected for testability.
//
// A device with no pending commands is the overwhelmingly common case (one mostly-empty
// query per Register/Update) and returns an empty slice, not an error.
func (f *CommandFetcher) Pending(ctx context.Context, tenant, deviceToken string, now time.Time) ([]DrainCommand, error) {
	// `statuses`, not `status`: the drain wants a SET. 🔴 This and command-delivery's schema
	// MUST land together — the forked graphql-go rejects an input-object field the schema does
	// not define when it arrives through a VARIABLE (CLAUDE.md, the forked-dependency note),
	// which is exactly how this is sent. Against an older command-delivery this call therefore
	// fails loudly (a counted drain error, retried on the next wake) rather than silently
	// dropping the filter and draining every device's commands.
	criteria := map[string]any{
		"pageNumber":  1,
		"pageSize":    f.fetchSize,
		"deviceToken": deviceToken,
		"statuses":    drainStatuses,
	}
	var resp drainResponse
	if err := f.client.Query(ctx, f.baseURL, tenant, drainQuery,
		map[string]any{"criteria": criteria}, &resp); err != nil {
		return nil, err
	}

	rows := resp.Commands.Results
	// Sort by numeric id ascending — enqueue order. String-compare on queuedTime would
	// be fragile (timezone/precision); the id is a monotonic PK, exactly what
	// command-delivery's own PendingCommands orders by. The sort runs over the WHOLE fetched
	// page (up to maxDrainFetch) so the truncation below selects the genuinely oldest, not an
	// arbitrary page's oldest.
	sort.Slice(rows, func(i, j int) bool { return parseID(rows[i].Id) < parseID(rows[j].Id) })

	out := make([]DrainCommand, 0, min(len(rows), f.max))
	for _, r := range rows {
		if len(out) >= f.max {
			break // per-wake dispatch cap: the oldest f.max; the rest drain on the next wake
		}
		// Defensive: the server already filtered on drainStatuses, but never hand a row
		// outside that set to dispatch even if the contract ever drifts (a terminal command
		// must not re-fire). This re-check reads the SAME list the criteria above sent, so
		// the predicate and the guard cannot disagree about what "drainable" means.
		if !drainable(r.Status) {
			continue
		}
		// Drop an already-expired command: it is about to be TIMEOUT'd by the sweep and
		// must not actuate the device past its horizon.
		if r.ExpiresAt != nil {
			if exp, err := time.Parse(time.RFC3339, *r.ExpiresAt); err == nil && !exp.After(now) {
				continue
			}
		}
		var payload []byte
		if r.Payload != nil {
			payload = []byte(*r.Payload)
		}
		// Status rides along: the dispatcher claims a HELD row before actuating and leaves a
		// SENT one alone. Dropping it here would make every drained command look unclaimable,
		// which is the shape a fake that omits a field produces — the caller's omission hides.
		out = append(out, DrainCommand{Token: r.Token, Name: r.Name, Payload: payload, Status: r.Status})
	}
	return out, nil
}

// parseID parses the stringified numeric PK; an unparseable id sorts first (0) rather
// than aborting the whole drain — a single malformed id must not strand a device's queue.
func parseID(s string) uint64 {
	v, _ := strconv.ParseUint(s, 10, 64)
	return v
}
