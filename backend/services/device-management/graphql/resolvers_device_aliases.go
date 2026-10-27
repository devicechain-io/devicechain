// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"fmt"
	"math"

	"github.com/devicechain-io/dc-device-management/model"
	util "github.com/devicechain-io/dc-microservice/graphql"
	gql "github.com/graph-gophers/graphql-go"
)

// ---------------------------------------------------
// The answer to resolveIntegrationDevice
// ---------------------------------------------------

// IntegrationDeviceResolutionResolver serves the outcome of resolving a sender's identifier.
type IntegrationDeviceResolutionResolver struct {
	M *model.IntegrationDeviceResolution
}

func (r *IntegrationDeviceResolutionResolver) Outcome() string { return string(r.M.Outcome) }

func (r *IntegrationDeviceResolutionResolver) DeviceToken() *string {
	if r.M.DeviceToken == "" {
		return nil
	}
	return &r.M.DeviceToken
}

func (r *IntegrationDeviceResolutionResolver) DeviceTypeToken() *string {
	if r.M.DeviceTypeToken == "" {
		return nil
	}
	return &r.M.DeviceTypeToken
}

func (r *IntegrationDeviceResolutionResolver) Overflow() bool { return r.M.Overflow }

// ---------------------------------------------------
// Unregistered integration devices
// ---------------------------------------------------

// UnregisteredIntegrationDeviceResolver serves one sender-named device an integration saw.
type UnregisteredIntegrationDeviceResolver struct {
	M model.IntegrationUnregisteredDevice
}

func (r *UnregisteredIntegrationDeviceResolver) Id() gql.ID {
	return gql.ID(fmt.Sprint(r.M.ID))
}

func (r *UnregisteredIntegrationDeviceResolver) Integration() string { return r.M.IntegrationToken }
func (r *UnregisteredIntegrationDeviceResolver) Scheme() string      { return r.M.Scheme }
func (r *UnregisteredIntegrationDeviceResolver) Value() string       { return r.M.Value }

func (r *UnregisteredIntegrationDeviceResolver) FirstSeen() string {
	return *util.FormatTime(r.M.FirstSeen)
}

func (r *UnregisteredIntegrationDeviceResolver) LastSeen() string {
	return *util.FormatTime(r.M.LastSeen)
}

// Count saturates at the largest GraphQL Int rather than wrapping.
func (r *UnregisteredIntegrationDeviceResolver) Count() int32 {
	if r.M.Count > math.MaxInt32 {
		return math.MaxInt32
	}
	if r.M.Count < 0 {
		return 0
	}
	return int32(r.M.Count)
}

// UnregisteredIntegrationDeviceSearchResultsResolver serves a page of them.
type UnregisteredIntegrationDeviceSearchResultsResolver struct {
	M model.UnregisteredDeviceSearchResults
	S *SchemaResolver
	C context.Context
}

func (r *UnregisteredIntegrationDeviceSearchResultsResolver) Results() []*UnregisteredIntegrationDeviceResolver {
	out := make([]*UnregisteredIntegrationDeviceResolver, 0, len(r.M.Results))
	for _, current := range r.M.Results {
		out = append(out, &UnregisteredIntegrationDeviceResolver{M: current})
	}
	return out
}

func (r *UnregisteredIntegrationDeviceSearchResultsResolver) Pagination() *SearchResultsPaginationResolver {
	return &SearchResultsPaginationResolver{M: r.M.Pagination, S: r.S, C: r.C}
}
