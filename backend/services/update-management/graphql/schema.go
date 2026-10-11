// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package graphql is the update-management data plane: the per-tenant artifact
// catalogue. Content is not uploaded through it (a GraphQL request cannot stream a
// firmware image); see the upload package for the endpoint the bytes go to.
package graphql

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"

	"github.com/devicechain-io/dc-microservice/auth"
	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-update-management/model"
	gql "github.com/graph-gophers/graphql-go"
)

//go:embed schema.graphql
var SchemaContent string

// SchemaResolver is the root resolver of the update-management data plane.
type SchemaResolver struct {
	// Area is the deployed functional area, reported by updateManagementInfo.
	Area string
	// AllowUnverifiedArtifacts is the instance's fixed updates.allowUnverifiedArtifacts, reported
	// by updateManagementInfo so an instance running with it can be seen to be.
	AllowUnverifiedArtifacts bool
}

// GetApi returns the catalogue API injected into the request context. It fails loudly
// rather than dereferencing nil: a plane served without its API is a wiring error, and
// answering from it would look like an empty catalogue.
func (r *SchemaResolver) GetApi(ctx context.Context) (*model.Api, error) {
	api, ok := ctx.Value(gqlcore.ContextApiKey).(*model.Api)
	if !ok || api == nil {
		return nil, errors.New("update-management: the artifact API is not wired into this request")
	}
	return api, nil
}

type serviceInfoResolver struct {
	area            string
	allowUnverified bool
}

func (r *serviceInfoResolver) Area() string                   { return r.area }
func (r *serviceInfoResolver) AllowUnverifiedArtifacts() bool { return r.allowUnverified }

// UpdateManagementInfo is the non-sensitive service identity.
func (r *SchemaResolver) UpdateManagementInfo() *serviceInfoResolver {
	return &serviceInfoResolver{area: r.Area, allowUnverified: r.AllowUnverifiedArtifacts}
}

// Artifact looks an artifact up by token.
func (r *SchemaResolver) Artifact(ctx context.Context, args struct{ Token string }) (*ArtifactResolver, error) {
	if err := auth.Authorize(ctx, auth.ArtifactRead); err != nil {
		return nil, err
	}
	api, err := r.GetApi(ctx)
	if err != nil {
		return nil, err
	}
	a, err := api.ArtifactByToken(ctx, args.Token)
	if err != nil || a == nil {
		return nil, err
	}
	return &ArtifactResolver{M: *a}, nil
}

// Artifacts searches artifacts.
func (r *SchemaResolver) Artifacts(ctx context.Context, args struct {
	Criteria struct {
		PageNumber int32
		PageSize   int32
		Kind       *string
		State      *string
		Component  *string
	}
}) (*ArtifactSearchResultsResolver, error) {
	if err := auth.Authorize(ctx, auth.ArtifactRead); err != nil {
		return nil, err
	}
	api, err := r.GetApi(ctx)
	if err != nil {
		return nil, err
	}
	found, err := api.Artifacts(ctx, model.ArtifactSearchCriteria{
		Pagination: rdb.Pagination{PageNumber: args.Criteria.PageNumber, PageSize: args.Criteria.PageSize},
		Kind:       args.Criteria.Kind,
		State:      args.Criteria.State,
		Component:  args.Criteria.Component,
	})
	if err != nil {
		return nil, err
	}
	return &ArtifactSearchResultsResolver{M: *found}, nil
}

type artifactCreateRequest struct {
	Token              string
	Kind               string
	Name               string
	Version            string
	Component          string
	SizeBytes          string
	Digest             string
	MediaType          *string
	CompatibleProfiles []string
	Source             *string
	Notes              *string
}

// CreateArtifact declares an artifact.
func (r *SchemaResolver) CreateArtifact(ctx context.Context, args struct {
	Request artifactCreateRequest
}) (*ArtifactResolver, error) {
	if err := auth.Authorize(ctx, auth.ArtifactWrite); err != nil {
		return nil, err
	}
	api, err := r.GetApi(ctx)
	if err != nil {
		return nil, err
	}
	req := args.Request
	size, err := strconv.ParseInt(req.SizeBytes, 10, 64)
	if err != nil {
		return nil, &model.RefusalError{Code: model.CodeInvalidArtifact,
			Msg: fmt.Sprintf("sizeBytes must be a decimal integer, got %q", req.SizeBytes)}
	}
	created, err := api.CreateArtifact(ctx, &model.ArtifactCreateRequest{
		Token: req.Token, Kind: req.Kind, Name: req.Name, Version: req.Version, Component: req.Component,
		SizeBytes: size, Digest: req.Digest, MediaType: req.MediaType,
		CompatibleProfiles: req.CompatibleProfiles, Source: req.Source, Notes: req.Notes,
	})
	if err != nil {
		return nil, err
	}
	return &ArtifactResolver{M: *created}, nil
}

// RetireArtifact withdraws an artifact from new use.
func (r *SchemaResolver) RetireArtifact(ctx context.Context, args struct{ Token string }) (*ArtifactResolver, error) {
	if err := auth.Authorize(ctx, auth.ArtifactWrite); err != nil {
		return nil, err
	}
	api, err := r.GetApi(ctx)
	if err != nil {
		return nil, err
	}
	retired, err := api.RetireArtifact(ctx, args.Token)
	if err != nil {
		return nil, err
	}
	return &ArtifactResolver{M: *retired}, nil
}

// ArtifactResolver resolves the fields of one artifact.
type ArtifactResolver struct{ M model.Artifact }

func (r *ArtifactResolver) Id() gql.ID          { return gql.ID(fmt.Sprint(r.M.ID)) }
func (r *ArtifactResolver) CreatedAt() *string  { return gqlcore.FormatTime(r.M.CreatedAt) }
func (r *ArtifactResolver) UpdatedAt() *string  { return gqlcore.FormatTime(r.M.UpdatedAt) }
func (r *ArtifactResolver) Token() string       { return r.M.Token }
func (r *ArtifactResolver) Kind() string        { return r.M.Kind }
func (r *ArtifactResolver) Name() string        { return r.M.Name }
func (r *ArtifactResolver) Version() string     { return r.M.Version }
func (r *ArtifactResolver) Component() string   { return r.M.Component }
func (r *ArtifactResolver) SizeBytes() string   { return strconv.FormatInt(r.M.SizeBytes, 10) }
func (r *ArtifactResolver) Digest() string      { return r.M.Digest }
func (r *ArtifactResolver) MediaType() string   { return r.M.MediaType }
func (r *ArtifactResolver) TrustStatus() string { return r.M.TrustStatus }
func (r *ArtifactResolver) State() string       { return r.M.State }

// CompatibleProfiles decodes the stored JSON array. A row whose document does not decode
// is corrupt, and is an error rather than an empty list: an empty list would read as
// "compatible with nothing".
func (r *ArtifactResolver) CompatibleProfiles() ([]string, error) {
	var out []string
	if err := json.Unmarshal(r.M.CompatibleProfiles, &out); err != nil {
		return nil, fmt.Errorf("artifact %q has an unreadable compatibleProfiles document: %w", r.M.Token, err)
	}
	return out, nil
}

// Provenance is the stored provenance document, or null when none was recorded.
func (r *ArtifactResolver) Provenance() *string {
	if len(r.M.Provenance) == 0 {
		return nil
	}
	s := string(r.M.Provenance)
	return &s
}

// ArtifactSearchResultsResolver resolves a page of artifacts.
type ArtifactSearchResultsResolver struct{ M model.ArtifactSearchResults }

func (r *ArtifactSearchResultsResolver) Results() []*ArtifactResolver {
	out := make([]*ArtifactResolver, 0, len(r.M.Results))
	for _, a := range r.M.Results {
		out = append(out, &ArtifactResolver{M: a})
	}
	return out
}

func (r *ArtifactSearchResultsResolver) Pagination() *searchResultsPaginationResolver {
	return &searchResultsPaginationResolver{M: r.M.Pagination}
}

type searchResultsPaginationResolver struct{ M rdb.SearchResultsPagination }

func (r *searchResultsPaginationResolver) PageStart() *int32    { return &r.M.PageStart }
func (r *searchResultsPaginationResolver) PageEnd() *int32      { return &r.M.PageEnd }
func (r *searchResultsPaginationResolver) TotalRecords() *int32 { return &r.M.TotalRecords }
