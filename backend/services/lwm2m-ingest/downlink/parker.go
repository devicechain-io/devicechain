// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package downlink

import (
	"context"
	"fmt"
)

// parkMutation hands a dispatched command back to command-delivery because this adapter
// found the device unreachable.
//
// Field names are pinned to command-delivery's schema. The graphql-go fork rejects an
// unknown field sent through a variable (CLAUDE.md, the forked-dependency note), so a typo
// here fails the call loudly rather than silently parking nothing.
const parkMutation = `mutation($token: String!, $dispatchNonce: String!) {
  parkCommand(token: $token, dispatchNonce: $dispatchNonce)
}`

// parkResponse decodes the mutation's single boolean field.
type parkResponse struct {
	Parked bool `json:"parkCommand"`
}

// CommandParker returns a command to command-delivery when the device it was published to
// turns out to have no live connection (ADR-075 L4b).
//
// 🔴 IT EXISTS BECAUSE PRESENCE IS NOT REACHABILITY. command-delivery decides whether to
// publish from presence — is the device registered — and a queue-mode device is registered
// and asleep. So the command is published, arrives here, and finds nothing to deliver over.
// Before this, the row simply stayed SENT, a state that means "the device has it": the
// platform then blamed the device at TTL with TIMEOUT, told an operator cancelling a fleet
// write that the command was beyond recall when it was not, and left the row to be
// re-dispatched by the wake drain without a claim.
//
// It shares the fetcher's commandQuerier seam, like the claimer, so downlink stays testable
// without a live command-delivery or a minted service token.
type CommandParker struct {
	client  commandQuerier
	baseURL string
}

// NewCommandParker builds a parker over the command-delivery GraphQL client + URL — the same
// pair the fetcher and claimer are built from.
func NewCommandParker(client commandQuerier, baseURL string) *CommandParker {
	return &CommandParker{client: client, baseURL: baseURL}
}

// Park hands one command back, naming the dispatch it arrived on, and reports whether this
// call moved the row.
//
// 🔴 THE NONCE IS WHAT MAKES THIS SAFE TO RETRY, and a park without one would be a
// re-actuation lever. The request can be a redelivery of a publish whose command has since
// been claimed by a wake drain and RUN; a park matching on status alone would drag that row
// back into the dispatchable set to be actuated a second time. Quoting the nonce means a
// stale request names a dispatch that no longer exists and moves nothing.
//
// Three outcomes, and the caller must treat them differently:
//   - (true, nil)  — parked. The command is back in the platform's hands.
//   - (false, nil) — SETTLED, not failed. The row moved on under this request: answered,
//     cancelled, expired, or re-claimed by a drain. Retrying would burn redeliveries on a
//     row that is already where it should be.
//   - (_, err)     — the park could not be established (command-delivery unreachable,
//     forbidden, a GraphQL error). The caller must leave the message UNACKED so it
//     redelivers; the row stays SENT until it does.
func (p *CommandParker) Park(ctx context.Context, tenant, commandToken, dispatchNonce string) (bool, error) {
	var resp parkResponse
	if err := p.client.Query(ctx, p.baseURL, tenant, parkMutation,
		map[string]any{"token": commandToken, "dispatchNonce": dispatchNonce}, &resp); err != nil {
		return false, fmt.Errorf("downlink: park command %q: %w", commandToken, err)
	}
	return resp.Parked, nil
}

// compile-time assertion that the concrete parker satisfies the dispatcher's seam.
var _ commandParker = (*CommandParker)(nil)
