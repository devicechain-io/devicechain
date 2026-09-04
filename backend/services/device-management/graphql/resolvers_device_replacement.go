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

// Device is the identity that survived the swap. It is loaded by the query's
// Preload; a replacement row cannot exist without one, so a nil here would be a
// broken read rather than an absent relationship — the empty resolver keeps the
// non-null SDL field honest instead of panicking a whole page of history.
func (r *DeviceReplacementResolver) Device() *DeviceResolver {
	if r.M.Device == nil {
		return &DeviceResolver{M: model.Device{}, S: r.S, C: r.C}
	}
	return &DeviceResolver{M: *r.M.Device, S: r.S, C: r.C}
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

func (r *DeviceReplaceResultResolver) Device() *DeviceResolver {
	if r.M.Device == nil {
		return &DeviceResolver{M: model.Device{}, S: r.S, C: r.C}
	}
	return &DeviceResolver{M: *r.M.Device, S: r.S, C: r.C}
}

func (r *DeviceReplaceResultResolver) Replacement() *DeviceReplacementResolver {
	if r.M.Replacement == nil {
		return &DeviceReplacementResolver{M: model.DeviceReplacement{}, S: r.S, C: r.C}
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
		return &DeviceCredentialResolver{M: model.DeviceCredential{}, S: r.S, C: r.C}
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
