// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"fmt"

	"github.com/devicechain-io/dc-device-management/model"
	util "github.com/devicechain-io/dc-microservice/graphql"
	gql "github.com/graph-gophers/graphql-go"
)

// ---------------------------
// Device replacement resolver
// ---------------------------

type DeviceReplacementResolver struct {
	M model.DeviceReplacement
	S *SchemaResolver
	C context.Context
}

func (r *DeviceReplacementResolver) Id() gql.ID {
	return gql.ID(fmt.Sprint(r.M.ID))
}

func (r *DeviceReplacementResolver) CreatedAt() *string {
	return util.FormatTime(r.M.CreatedAt)
}

// Device is the identity that survived the swap. Both current readers supply it —
// the query Preloads it and the mutation attaches it after commit — so the nil case
// is a record that arrived by some other route; it lazy-loads by DeviceId the way
// DeviceResolver.DeviceType does, under the SAME context this resolver was built
// with, which is what carries the tenant and the caller's authorities into the read.
//
// One consequence to know before adding a caller: DevicesById requires device:read,
// and authority matching is exact, so device:write does NOT imply it. A resolver
// built on the replaceDevice path — gated on device:write — that reached this load
// would fail for a write-only caller. That is narrower than the caller's authority
// rather than wider, so it is a trap rather than a hole, but it is the reason
// ReplaceDevice attaches the device instead of leaving it to be loaded here.
//
// 🔴 WHEN THE LOAD FINDS NOTHING IT RETURNS nil, AND THAT IS THE POINT. The
// alternative — a zero-valued Device — makes `device { token }` come back as `""`,
// which at a call site is indistinguishable from a device whose token happens to be
// blank: neither an error nor a null, just a plausible wrong answer.
//
// Be clear about what the nil costs, because it is not one field. `device: Device!`
// sits inside `results: [DeviceReplacement!]!` inside `DeviceReplacementSearchResults!`
// — non-null the whole way up — so the error propagates through every ancestor and
// nulls the entire response. ONE unresolvable row fails the WHOLE PAGE of history.
//
// That is the right trade here, and the reason is what makes it different from
// RetiredCredentialTokenList, which argues the opposite a few files away
// (api_device_replacement.go) and returns an empty slice rather than failing a page.
// The distinction is who enforces the invariant. A malformed token annotation is
// unreadable DATA in a column nothing constrains, so one bad row says nothing about
// the rest and must not take them down. A missing device is a broken RELATIONSHIP
// that the database itself forbids: device_id is NOT NULL under the foreign key
// fk_device-management_device_replacements_device, and DeleteDevice hard-deletes a
// device's replacement rows with it (api_delete.go), so no legitimate row can outlive
// its device. Reaching this nil means an invariant the storage layer enforces has
// been violated, and a page that answers confidently over that is worse than no page.
func (r *DeviceReplacementResolver) Device() *DeviceResolver {
	if r.M.Device != nil {
		return &DeviceResolver{M: *r.M.Device, S: r.S, C: r.C}
	}
	ids := []string{fmt.Sprintf("%d", r.M.DeviceId)}
	rez, err := r.S.DevicesById(r.C, struct{ Ids []string }{Ids: ids})
	if err != nil || len(rez) == 0 {
		return nil
	}
	return rez[0]
}

// OccurredTime is always present — it is stamped by ReplaceDevice, never parsed
// from a request — so the SDL field is non-null and the formatter's absent case
// cannot arise.
func (r *DeviceReplacementResolver) OccurredTime() string {
	if s := util.FormatTime(r.M.OccurredTime); s != nil {
		return *s
	}
	return ""
}

func (r *DeviceReplacementResolver) Actor() string {
	return r.M.Actor
}

func (r *DeviceReplacementResolver) Reason() *string {
	return util.NullStr(r.M.Reason)
}

func (r *DeviceReplacementResolver) UnitIdentifier() *string {
	return util.NullStr(r.M.UnitIdentifier)
}

// RetiredCredentialTokens returns the entity tokens of the credentials the
// replacement disabled — never their credential ids, which for an ACCESS_TOKEN are
// the device's bearer. That is what keeps this journal readable under device:read
// without widening the credential gate.
func (r *DeviceReplacementResolver) RetiredCredentialTokens() []string {
	return r.M.RetiredCredentialTokenList()
}

func (r *DeviceReplacementResolver) NewCredentialToken() string {
	return r.M.NewCredentialToken
}

func (r *DeviceReplacementResolver) NewCredentialType() string {
	return r.M.NewCredentialType
}

type DeviceReplacementSearchResultsResolver struct {
	M model.DeviceReplacementSearchResults
	S *SchemaResolver
	C context.Context
}

func (r *DeviceReplacementSearchResultsResolver) Results() []*DeviceReplacementResolver {
	resolvers := make([]*DeviceReplacementResolver, 0)
	for _, current := range r.M.Results {
		resolvers = append(resolvers, &DeviceReplacementResolver{M: current, S: r.S, C: r.C})
	}
	return resolvers
}

func (r *DeviceReplacementSearchResultsResolver) Pagination() *SearchResultsPaginationResolver {
	return &SearchResultsPaginationResolver{M: r.M.Pagination, S: r.S, C: r.C}
}

// ------------------------------
// Device replacement result
// ------------------------------

type DeviceReplaceResultResolver struct {
	M model.DeviceReplaceResult
	S *SchemaResolver
	C context.Context
}

// 🔴 THE THREE FIELDS BELOW ANSWER nil ON A NIL COMPONENT RATHER THAN A ZERO VALUE,
// for the same reason DeviceReplacementResolver.Device does — but they cannot lazy-load
// their way out of it, and that difference is the whole design of this type.
//
// DeviceReplaceResult is not a row. It is the value ReplaceDevice assembles in memory
// from a single transaction, and it carries no id for any of its three components —
// there is nothing to load BY. So the only two answers available are the component
// itself and nil, and every SDL field here is non-null (`device: Device!`,
// `replacement: DeviceReplacement!`, `newCredential: DeviceCredential!`).
//
// api.ReplaceDevice sets all three or returns an error, so none of these branches has
// a caller today. They are written this way because a second construction path is
// exactly what recreated the empty-token answer once already: a zero value renders as
// a successful response full of blanks — an empty device token, a replacement with no
// actor and no occurred time, a credential with an empty token — which reads as a swap
// that happened and did nothing. A mutation that half-succeeded must fail loudly, and
// nil is what makes the non-null field say so.
func (r *DeviceReplaceResultResolver) Device() *DeviceResolver {
	if r.M.Device == nil {
		return nil
	}
	return &DeviceResolver{M: *r.M.Device, S: r.S, C: r.C}
}

func (r *DeviceReplaceResultResolver) Replacement() *DeviceReplacementResolver {
	if r.M.Replacement == nil {
		return nil
	}
	return &DeviceReplacementResolver{M: *r.M.Replacement, S: r.S, C: r.C}
}

// NewCredential exposes the credential minted for the incoming unit — including its
// credentialId, which for an ACCESS_TOKEN is the bearer the new unit will present.
//
// 🔴 THIS IS THE ONE FIELD OUTSIDE THE GATED CREDENTIAL QUERIES THAT RETURNS
// CREDENTIAL MATERIAL, and it is only correct because replaceDevice is gated on
// device:write — the same authority those queries and createDeviceCredential
// require, so it confers nothing a caller could not already obtain. It carries an
// entry in the allowlist of TestDeviceCredentialIsReachableOnlyThroughTheGatedQueries;
// removing the gate without removing that entry is exactly the drift that test
// exists to catch.
func (r *DeviceReplaceResultResolver) NewCredential() *DeviceCredentialResolver {
	if r.M.NewCredential == nil {
		return nil
	}
	return &DeviceCredentialResolver{M: *r.M.NewCredential, S: r.S, C: r.C}
}

// RetiredCredentialTokens names the disabled credentials by their entity tokens
// rather than returning the rows. Deliberately narrower than the model result,
// which does carry the full DeviceCredential values: a caller that wants those can
// ask the gated credential queries for them, and every extra field returning
// credential material is another door the gate has to be re-argued for.
func (r *DeviceReplaceResultResolver) RetiredCredentialTokens() []string {
	tokens := make([]string, 0, len(r.M.RetiredCredentials))
	for _, cred := range r.M.RetiredCredentials {
		tokens = append(tokens, cred.Token)
	}
	return tokens
}
