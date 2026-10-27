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
	// MaxArtifactBytes is the effective per-artifact ceiling, reported by updateManagementInfo.
	MaxArtifactBytes int64
	// DownloadsConfigured is whether a download base is set (not that devices can reach it), reported by
	// updateManagementInfo. Unset, no device can be given a download capability, so an
	// assignment is published and never fetched; the console says so rather than let that
	// look like a slow fleet.
	DownloadsConfigured bool
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
	area                  string
	allowUnverified       bool
	maxArtifactBytes      int64
	downloadsConfigured   bool
	objectStoreConfigured bool
}

func (r *serviceInfoResolver) MaxArtifactBytes() string {
	return strconv.FormatInt(r.maxArtifactBytes, 10)
}

func (r *serviceInfoResolver) Area() string                   { return r.area }
func (r *serviceInfoResolver) AllowUnverifiedArtifacts() bool { return r.allowUnverified }
func (r *serviceInfoResolver) DownloadsConfigured() bool      { return r.downloadsConfigured }
func (r *serviceInfoResolver) ObjectStoreConfigured() bool    { return r.objectStoreConfigured }

// UpdateManagementInfo is the non-sensitive service identity. Whether a store is configured is
// read from the catalogue API the request carries (its blob store), not assumed: a plane served
// without its API is a wiring error and fails here rather than reporting a store that is not there.
func (r *SchemaResolver) UpdateManagementInfo(ctx context.Context) (*serviceInfoResolver, error) {
	api, err := r.GetApi(ctx)
	if err != nil {
		return nil, err
	}
	return &serviceInfoResolver{objectStoreConfigured: api.Blobs != nil, area: r.Area, allowUnverified: r.AllowUnverifiedArtifacts, maxArtifactBytes: r.MaxArtifactBytes,
		downloadsConfigured: r.DownloadsConfigured}, nil
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
	return newArtifactResolver(api, *a), nil
}

// Artifacts searches artifacts.
func (r *SchemaResolver) Artifacts(ctx context.Context, args struct {
	Criteria struct {
		PageNumber  int32
		PageSize    int32
		Kind        *string
		State       *string
		Component   *string
		TrustStatus *string
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
		Pagination:  rdb.Pagination{PageNumber: args.Criteria.PageNumber, PageSize: args.Criteria.PageSize},
		Kind:        args.Criteria.Kind,
		State:       args.Criteria.State,
		Component:   args.Criteria.Component,
		TrustStatus: args.Criteria.TrustStatus,
	})
	if err != nil {
		return nil, err
	}
	return newArtifactSearchResultsResolver(api, *found), nil
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
	return newArtifactResolver(api, *created), nil
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
	return newArtifactResolver(api, *retired), nil
}

// ArtifactResolver resolves the fields of one artifact.
type ArtifactResolver struct {
	M model.Artifact
	// sigs loads the artifact's signatures the first time the field is selected. Artifacts of one
	// search page share a loader, so a page costs two queries however many artifacts it holds.
	sigs *signatureLoader
}

func newArtifactResolver(api *model.Api, a model.Artifact) *ArtifactResolver {
	return &ArtifactResolver{M: a, sigs: newSignatureLoader(api, a.ID)}
}

func (r *ArtifactResolver) Id() gql.ID         { return gql.ID(fmt.Sprint(r.M.ID)) }
func (r *ArtifactResolver) CreatedAt() *string { return gqlcore.FormatTime(r.M.CreatedAt) }
func (r *ArtifactResolver) UpdatedAt() *string { return gqlcore.FormatTime(r.M.UpdatedAt) }
func (r *ArtifactResolver) Token() string      { return r.M.Token }
func (r *ArtifactResolver) Kind() string       { return r.M.Kind }
func (r *ArtifactResolver) Name() string       { return r.M.Name }
func (r *ArtifactResolver) Version() string    { return r.M.Version }
func (r *ArtifactResolver) Component() string  { return r.M.Component }
func (r *ArtifactResolver) SizeBytes() string  { return strconv.FormatInt(r.M.SizeBytes, 10) }
func (r *ArtifactResolver) Digest() string     { return r.M.Digest }
func (r *ArtifactResolver) MediaType() string  { return r.M.MediaType }
func (r *ArtifactResolver) State() string      { return r.M.State }

// TrustStatus is the DERIVED trust. An artifact whose trust was never derived is an error,
// not UNVERIFIED: a read path that forgot to derive it would otherwise report a plausible value.
func (r *ArtifactResolver) TrustStatus() (string, error) {
	if r.M.Trust == "" {
		return "", fmt.Errorf("artifact %q was read without deriving its trust", r.M.Token)
	}
	return r.M.Trust, nil
}

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
// Provenance records who uploaded the artifact (a username, which for a person is their
// email) and the uploader's own source and notes. A member limited to part of the tenant
// (ADR-081) reaches an artifact only as the one its device's assignment names, and the
// tenant's staff and their notes are not its business: the field refuses it.
func (r *ArtifactResolver) Provenance(ctx context.Context) (*string, error) {
	if _, scoped := auth.ScopeFromContext(ctx); scoped {
		return nil, auth.ErrScopeUnsupported
	}
	return r.provenance(), nil
}

func (r *ArtifactResolver) provenance() *string {
	if len(r.M.Provenance) == 0 {
		return nil
	}
	s := string(r.M.Provenance)
	return &s
}

// ArtifactSearchResultsResolver resolves a page of artifacts.
type ArtifactSearchResultsResolver struct {
	M    model.ArtifactSearchResults
	sigs *signatureLoader
}

func newArtifactSearchResultsResolver(api *model.Api, m model.ArtifactSearchResults) *ArtifactSearchResultsResolver {
	ids := make([]uint, 0, len(m.Results))
	for _, a := range m.Results {
		ids = append(ids, a.ID)
	}
	return &ArtifactSearchResultsResolver{M: m, sigs: newSignatureLoader(api, ids...)}
}

func (r *ArtifactSearchResultsResolver) Results() []*ArtifactResolver {
	out := make([]*ArtifactResolver, 0, len(r.M.Results))
	for _, a := range r.M.Results {
		out = append(out, &ArtifactResolver{M: a, sigs: r.sigs})
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
