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

// statusSent is command-delivery's SENT lifecycle state, duplicated here as a wire
// contract (like deliveryEnvelope's JSON tags) rather than imported — downlink does not
// depend on the command-delivery module. It MUST stay equal to
// command-delivery/model.CommandSent. The drain reads only SENT commands: a QUEUED row
// is ≤ one sweep old and command-delivery's own 30s redelivery will publish it to the
// LIVE path (draining it here too would double-dispatch), and a terminal row is done.
const statusSent = "SENT"

// maxDrainPerWake bounds BOTH the fetch page and the per-wake dispatch: a waking device
// drains at most this many of its oldest still-SENT commands, the remainder on its next
// Register/Update. It is the device-edge flood governor (ADR-075 L4b) — a REACT
// send-command storm (the programmatic flood origin, not operators) cannot slam a
// constrained radio with an unbounded burst the instant it wakes.
const maxDrainPerWake = 32

// drainQuery pulls a device's commands in a given lifecycle state. Field names are
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

// DrainCommand is one still-SENT command fetched for a waking device, mapped to exactly
// what the dispatcher's executor consumes: the command token (to correlate the response
// back to the persisted command) plus the command name and the raw JSON payload bytes
// the CoAP op mapping reads. It is byte-for-byte the same (name, payload) the live
// deliveryEnvelope yields, so the drain reuses the identical dispatch path.
type DrainCommand struct {
	Token   string
	Name    string
	Payload []byte
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

// CommandFetcher reads a waking device's still-SENT commands from command-delivery so
// the leader can drain them to the now-live CoAP device (ADR-075 L4b, Architecture D).
// The durable hold is command-delivery's Postgres row — the command that was
// published-and-ack-dropped while the device was offline sits in SENT until it is
// delivered (this) or reaches its TTL horizon (TIMEOUT). This is the read side of that
// hold; it builds no second source of truth.
type CommandFetcher struct {
	client  commandQuerier
	baseURL string
	max     int
}

// NewCommandFetcher builds a fetcher over the command-delivery GraphQL client + URL.
func NewCommandFetcher(client commandQuerier, baseURL string) *CommandFetcher {
	return &CommandFetcher{client: client, baseURL: baseURL, max: maxDrainPerWake}
}

// Pending returns a waking device's still-SENT commands, OLDEST FIRST (by numeric id —
// the commands query has no server-side ordering, and a firmware Write /5/0/1 must not
// dispatch after its Execute /5/0/2). Already-expired rows are dropped: a command past
// its horizon will TIMEOUT within a sweep and must never actuate a device late. The
// result is capped at the fetch page (maxDrainPerWake); a device with a deeper backlog
// drains the rest on subsequent wakes as each drained command leaves SENT. `now` is
// injected for testability.
//
// A device with no pending commands is the overwhelmingly common case (one mostly-empty
// query per Register/Update) and returns an empty slice, not an error.
func (f *CommandFetcher) Pending(ctx context.Context, tenant, deviceToken string, now time.Time) ([]DrainCommand, error) {
	criteria := map[string]any{
		"pageNumber":  1,
		"pageSize":    f.max,
		"deviceToken": deviceToken,
		"status":      statusSent,
	}
	var resp drainResponse
	if err := f.client.Query(ctx, f.baseURL, tenant, drainQuery,
		map[string]any{"criteria": criteria}, &resp); err != nil {
		return nil, err
	}

	rows := resp.Commands.Results
	// Sort by numeric id ascending — enqueue order. String-compare on queuedTime would
	// be fragile (timezone/precision); the id is a monotonic PK, exactly what
	// command-delivery's own PendingCommands orders by.
	sort.Slice(rows, func(i, j int) bool { return parseID(rows[i].Id) < parseID(rows[j].Id) })

	out := make([]DrainCommand, 0, len(rows))
	for _, r := range rows {
		// Defensive: the server filtered status=SENT, but never dispatch a row that is
		// not SENT even if that contract ever drifts (a terminal command must not re-fire).
		if r.Status != statusSent {
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
		out = append(out, DrainCommand{Token: r.Token, Name: r.Name, Payload: payload})
	}
	return out, nil
}

// parseID parses the stringified numeric PK; an unparseable id sorts first (0) rather
// than aborting the whole drain — a single malformed id must not strand a device's queue.
func parseID(s string) uint64 {
	v, _ := strconv.ParseUint(s, 10, 64)
	return v
}
