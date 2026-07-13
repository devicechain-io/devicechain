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
// Detection rule resolver
// ---------------------------

type DetectionRuleResolver struct {
	M model.DetectionRule
	S *SchemaResolver
	C context.Context
}

func (r *DetectionRuleResolver) Id() gql.ID {
	return gql.ID(fmt.Sprint(r.M.ID))
}

func (r *DetectionRuleResolver) CreatedAt() *string {
	return util.FormatTime(r.M.CreatedAt)
}

func (r *DetectionRuleResolver) UpdatedAt() *string {
	return util.FormatTime(r.M.UpdatedAt)
}

func (r *DetectionRuleResolver) DeletedAt() *string {
	return util.FormatTime(r.M.DeletedAt.Time)
}

func (r *DetectionRuleResolver) Token() string { return r.M.Token }

func (r *DetectionRuleResolver) Name() *string { return util.NullStr(r.M.Name) }

func (r *DetectionRuleResolver) Description() *string { return util.NullStr(r.M.Description) }

func (r *DetectionRuleResolver) Metadata() *string { return util.MetadataStr(r.M.Metadata) }

func (r *DetectionRuleResolver) Enabled() bool { return r.M.Enabled }

// Definition is the opaque rules.Rule JSON document, returned as its raw JSON text.
func (r *DetectionRuleResolver) Definition() string { return string(r.M.Definition) }

// AuthoringGraph is the opaque canvas CanvasDefinition JSON (ADR-053 slice 9b), returned as
// its raw JSON text, or null for a form-authored rule with no sidecar.
func (r *DetectionRuleResolver) AuthoringGraph() *string {
	if len(r.M.AuthoringGraph) == 0 {
		return nil
	}
	s := string(r.M.AuthoringGraph)
	return &s
}

func (r *DetectionRuleResolver) DeviceProfile() *DeviceProfileResolver {
	if r.M.DeviceProfile != nil {
		return &DeviceProfileResolver{M: *r.M.DeviceProfile, S: r.S, C: r.C}
	}
	ids := []string{fmt.Sprintf("%d", r.M.DeviceProfileId)}
	rez, err := r.S.DeviceProfilesById(r.C, struct{ Ids []string }{Ids: ids})
	if err != nil || len(rez) == 0 {
		return nil
	}
	return rez[0]
}

// --------------------------------------
// Detection rule search results resolver
// --------------------------------------

type DetectionRuleSearchResultsResolver struct {
	M model.DetectionRuleSearchResults
	S *SchemaResolver
	C context.Context
}

func (r *DetectionRuleSearchResultsResolver) Results() []*DetectionRuleResolver {
	resolvers := make([]*DetectionRuleResolver, 0)
	for _, current := range r.M.Results {
		resolvers = append(resolvers,
			&DetectionRuleResolver{M: current, S: r.S, C: r.C})
	}
	return resolvers
}

func (r *DetectionRuleSearchResultsResolver) Pagination() *SearchResultsPaginationResolver {
	return &SearchResultsPaginationResolver{M: r.M.Pagination, S: r.S, C: r.C}
}
