// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package downlink

import (
	"context"
	"fmt"
)

// claimMutation moves a still-dispatchable command (QUEUED, HELD or PARKED) to SENT and reports
// whether THIS call performed the transition. It is command-delivery's markCommandSent.
//
// It returns a boolean rather than the command on purpose, and the drain depends on that:
// the decision to actuate a physical device must rest on whether the conditional UPDATE
// matched, not on a status read back afterwards — a read-back cannot distinguish "I claimed
// it" from "another replica claimed it a millisecond ago", and both would look like SENT.
//
// Field names are pinned to command-delivery's schema. The graphql-go fork rejects an
// unknown field sent through a variable (CLAUDE.md, the forked-dependency note), so a typo
// here fails the call loudly — and a failed claim does NOT dispatch, which is the safe
// direction.
const claimMutation = `mutation($token: String!) {
  markCommandSent(token: $token)
}`

// claimResponse decodes the mutation's single boolean field.
type claimResponse struct {
	Claimed bool `json:"markCommandSent"`
}

// CommandClaimer takes ownership of a HELD command in command-delivery immediately before
// the wake drain issues its CoAP op (ADR-075 L4b).
//
// It exists because the delivery sweep publishes anything still dispatchable. A drain that
// dispatched a HELD row without first claiming it would leave that row HELD for the next
// sweep tick to publish down the live path — and a command is a PHYSICAL ACTUATION, so that
// is a valve opened twice, not a duplicate log line. The dispatcher's in-memory dedupe does
// not close this: it is per-pod and TTL-bounded, so it cannot be what stands between a
// leadership change and a duplicate actuation.
//
// It shares the fetcher's commandQuerier seam (svcclient.Client's Query posts a mutation
// document just as it posts a query), so downlink stays testable without a live
// command-delivery or a minted service token.
type CommandClaimer struct {
	client  commandQuerier
	baseURL string
}

// NewCommandClaimer builds a claimer over the command-delivery GraphQL client + URL — the
// same pair the fetcher is built from, because the claim is the write half of the same read.
func NewCommandClaimer(client commandQuerier, baseURL string) *CommandClaimer {
	return &CommandClaimer{client: client, baseURL: baseURL}
}

// Claim attempts to move one command to SENT and reports whether this caller won it.
//
// Three outcomes, and the caller must treat them differently:
//   - (true, nil)  — claimed; the caller owns this command and may actuate.
//   - (false, nil) — LOST. Another replica, or the sweep, got there first, or the command
//     was answered/cancelled meanwhile. Benign: skip it, do not actuate.
//   - (_, err)     — the claim could not be established at all (command-delivery unreachable,
//     forbidden, a GraphQL error). The caller must NOT actuate: dispatch is irreversible and
//     a claim we cannot confirm is a claim we do not hold. The command stays dispatchable and
//     is retried on the device's next wake.
func (c *CommandClaimer) Claim(ctx context.Context, tenant, commandToken string) (bool, error) {
	var resp claimResponse
	if err := c.client.Query(ctx, c.baseURL, tenant, claimMutation,
		map[string]any{"token": commandToken}, &resp); err != nil {
		return false, fmt.Errorf("downlink: claim command %q: %w", commandToken, err)
	}
	return resp.Claimed, nil
}

// compile-time assertion that the concrete claimer satisfies the dispatcher's seam.
var _ commandClaimer = (*CommandClaimer)(nil)
