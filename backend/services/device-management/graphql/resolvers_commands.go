// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	_ "embed"
	"fmt"

	"github.com/devicechain-io/dc-device-management/model"
	util "github.com/devicechain-io/dc-microservice/graphql"
	gql "github.com/graph-gophers/graphql-go"
)

// ---------------------------
// Command definition resolver
// ---------------------------

type CommandDefinitionResolver struct {
	M model.CommandDefinition
	S *SchemaResolver
	C context.Context
}

func (r *CommandDefinitionResolver) Id() gql.ID {
	return gql.ID(fmt.Sprint(r.M.ID))
}

func (r *CommandDefinitionResolver) CreatedAt() *string {
	return util.FormatTime(r.M.CreatedAt)
}

func (r *CommandDefinitionResolver) UpdatedAt() *string {
	return util.FormatTime(r.M.UpdatedAt)
}

func (r *CommandDefinitionResolver) DeletedAt() *string {
	return util.FormatTime(r.M.DeletedAt.Time)
}

func (r *CommandDefinitionResolver) Token() string {
	return r.M.Token
}

func (r *CommandDefinitionResolver) Name() *string {
	return util.NullStr(r.M.Name)
}

func (r *CommandDefinitionResolver) Description() *string {
	return util.NullStr(r.M.Description)
}

func (r *CommandDefinitionResolver) Metadata() *string {
	return util.MetadataStr(r.M.Metadata)
}

func (r *CommandDefinitionResolver) CommandKey() string {
	return r.M.CommandKey
}

// ParameterSchema is the ordered []CommandParameter contract as a JSON string; the
// console parses it to render the command form. Nil when the command declares no
// structured arguments.
func (r *CommandDefinitionResolver) ParameterSchema() *string {
	return util.MetadataStr(r.M.ParameterSchema)
}

func (r *CommandDefinitionResolver) DeviceProfile() *DeviceProfileResolver {
	if r.M.DeviceProfile != nil {
		return &DeviceProfileResolver{
			M: *r.M.DeviceProfile,
			S: r.S,
			C: r.C,
		}
	} else {
		ids := []string{fmt.Sprintf("%d", r.M.DeviceProfileId)}
		rez, err := r.S.DeviceProfilesById(r.C, struct{ Ids []string }{Ids: ids})
		if err != nil || len(rez) == 0 {
			return nil
		}
		return rez[0]
	}
}

// ------------------------------------------
// Command definition search results resolver
// ------------------------------------------

type CommandDefinitionSearchResultsResolver struct {
	M model.CommandDefinitionSearchResults
	S *SchemaResolver
	C context.Context
}

func (r *CommandDefinitionSearchResultsResolver) Results() []*CommandDefinitionResolver {
	resolvers := make([]*CommandDefinitionResolver, 0)
	for _, current := range r.M.Results {
		resolvers = append(resolvers,
			&CommandDefinitionResolver{
				M: current,
				S: r.S,
				C: r.C,
			})
	}
	return resolvers
}

func (r *CommandDefinitionSearchResultsResolver) Pagination() *SearchResultsPaginationResolver {
	return &SearchResultsPaginationResolver{
		M: r.M.Pagination,
		S: r.S,
		C: r.C,
	}
}
