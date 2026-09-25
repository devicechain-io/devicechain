// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package downlink

import (
	"context"
	"fmt"
)

// claimMutation moves a still-dispatchable command (QUEUED, HELD or PARKED) to SENT and answers
// with the DISPATCH NONCE the transition stamped, or null if this call did not perform it. It is
// command-delivery's markCommandSent.
//
// It returns the nonce rather than the command on purpose, and the drain depends on both halves
// of that. A non-null answer means THIS call's conditional UPDATE matched — the decision to
// actuate a physical device must rest on that and not on a status read back afterwards, which
// cannot distinguish "I claimed it" from "another replica claimed it a millisecond ago", since
// both look like SENT. And the value itself is the identity of the dispatch this claim created,
// which the drain has to quote when it publishes the command's outcome: a response that names no
// dispatch is refused, so a claim that did not hand the nonce back would leave the drain able to
// actuate a device and unable to settle the command.
//
// Field names are pinned to command-delivery's schema. The graphql-go fork rejects an
// unknown field sent through a variable (CLAUDE.md, the forked-dependency note), so a typo
// here fails the call loudly — and a failed claim does NOT dispatch, which is the safe
// direction.
const claimMutation = `mutation($token: String!) {
  markCommandSent(token: $token)
}`

// claimResponse decodes the mutation's single nullable-string field: the dispatch nonce when
// this call won the claim, absent when it did not.
type claimResponse struct {
	DispatchNonce *string `json:"markCommandSent"`
}

// CommandClaimer takes ownership of a backlogged (HELD or PARKED) command in command-delivery
// immediately before the wake drain issues its CoAP op (ADR-075 L4b). Both drainable states go
// through it — see claim() in dispatcher.go, where the exemption that used to skip it is
// argued away.
//
// It exists because the delivery sweep publishes anything still dispatchable. A drain that
// dispatched a HELD row without first claiming it would leave that row HELD for the next
// sweep tick to publish down the live path — and a command is a PHYSICAL ACTUATION, so that
// is a valve opened twice, not a duplicate log line. Only a claim on the row can close this:
// anything held in one pod's memory says nothing about another replica, or about this pod
// after a restart, which are exactly the two situations a leadership change produces.
//
// It also claims on the LIVE path (ClaimDispatch): a command that arrived on the delivery
// stream is confirmed on its row immediately before it actuates, so a late or redelivered
// envelope is discarded rather than carried out a second time.
//
// 🔴 THAT ARGUMENT IS HELD's, AND A PARKED ROW DOES NOT INHERIT IT — which is why the sentence
// above names HELD and this one exists rather than the wording being widened. The sweep
// deliberately does NOT look at PARKED (republishing it would re-publish an offline fleet's
// whole backlog every tick), so PARKED's risk is the other one: two replicas, or a drain and a
// cancel, reaching the same row at once. Same conditional UPDATE, same exclusion, different
// thing being excluded. Neither state is exempt.
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

// Claim attempts to move one command to SENT and reports whether this caller won it, along with
// the dispatch nonce it must quote when it publishes the command's outcome.
//
// Three outcomes, and the caller must treat them differently:
//   - (nonce, true, nil)  — claimed; the caller owns this command, may actuate, and must carry
//     the nonce into the response it publishes.
//   - ("", false, nil) — LOST. Another replica, or the sweep, got there first, or the command
//     was answered/cancelled meanwhile. Benign: skip it, do not actuate.
//   - (_, _, err)     — the claim could not be established at all (command-delivery unreachable,
//     forbidden, a GraphQL error). The caller must NOT actuate: dispatch is irreversible and
//     a claim we cannot confirm is a claim we do not hold. The command stays dispatchable and
//     the drain turn is retried from it shortly.
//
// 🔴 A WON CLAIM WITH AN EMPTY NONCE IS TREATED AS AN ERROR, NOT AS A WIN. command-delivery
// answers null for a lost claim and a value for a won one, so an empty string is neither — a
// truncated response, a schema that has moved. Actuating on it would produce an outcome nothing
// can settle, which is the one failure worse than declining to actuate.
func (c *CommandClaimer) Claim(ctx context.Context, tenant, commandToken string) (string, bool, error) {
	var resp claimResponse
	if err := c.client.Query(ctx, c.baseURL, tenant, claimMutation,
		map[string]any{"token": commandToken}, &resp); err != nil {
		return "", false, fmt.Errorf("downlink: claim command %q: %w", commandToken, err)
	}
	if resp.DispatchNonce == nil {
		return "", false, nil
	}
	if *resp.DispatchNonce == "" {
		return "", false, fmt.Errorf("downlink: claim command %q: the claim was granted with no dispatch nonce", commandToken)
	}
	return *resp.DispatchNonce, true, nil
}

// confirmDispatchMutation confirms a command received on the live delivery stream immediately
// before actuating it, quoting the envelope's dispatch nonce. It is command-delivery's
// confirmCommandDispatch, and it answers with the NEW nonce the confirmation rotated the row
// to, or null when the envelope names a dispatch the row is no longer on.
//
// Field names are pinned to command-delivery's schema, for the reason claimMutation's are.
const confirmDispatchMutation = `mutation($token: String!, $dispatchNonce: String!) {
  confirmCommandDispatch(token: $token, dispatchNonce: $dispatchNonce)
}`

// confirmDispatchResponse decodes the mutation's single nullable-string field.
type confirmDispatchResponse struct {
	DispatchNonce *string `json:"confirmCommandDispatch"`
}

// ClaimDispatch is the LIVE-path claim: it confirms that the dispatch a delivery envelope names
// is still the command's current one, and reports whether this caller may actuate it, along
// with the NEW dispatch nonce it must quote when it publishes the outcome.
//
// 🔴 WHY THE LIVE PATH CLAIMS. An envelope can arrive late — redelivered after it waited out
// its ack deadline behind a slow device, redelivered to a new leader, or pulled for the first
// time long after it was published because no replica was reading. By then the platform may
// have re-armed the command and a wake drain may have carried it out. The confirmation is
// predicated on the envelope's nonce and rotates it, so a late copy names a dispatch that no
// longer exists and loses; so does every redelivered copy after the first confirmation.
//
// 🔴 THE RESPONSE QUOTES THE RETURNED NONCE, NOT THE ENVELOPE'S. The confirmation moves the
// row onto a new dispatch, and command-delivery matches an answer against the row's CURRENT
// nonce, so an answer quoting the envelope's value would be refused and the command would
// time out.
//
// The three outcomes are Claim's, and are treated the same way: (nonce, true, nil) actuate;
// ("", false, nil) LOST — do not actuate; (_, _, err) unknown — do not actuate. A won
// confirmation with an empty nonce is an error, for Claim's reason.
func (c *CommandClaimer) ClaimDispatch(ctx context.Context, tenant, commandToken, dispatchNonce string) (string, bool, error) {
	var resp confirmDispatchResponse
	if err := c.client.Query(ctx, c.baseURL, tenant, confirmDispatchMutation,
		map[string]any{"token": commandToken, "dispatchNonce": dispatchNonce}, &resp); err != nil {
		return "", false, fmt.Errorf("downlink: confirm dispatch of command %q: %w", commandToken, err)
	}
	if resp.DispatchNonce == nil {
		return "", false, nil
	}
	if *resp.DispatchNonce == "" {
		return "", false, fmt.Errorf("downlink: confirm dispatch of command %q: the confirmation was granted with no dispatch nonce", commandToken)
	}
	return *resp.DispatchNonce, true, nil
}

// compile-time assertion that the concrete claimer satisfies the dispatcher's seam.
var _ commandClaimer = (*CommandClaimer)(nil)
