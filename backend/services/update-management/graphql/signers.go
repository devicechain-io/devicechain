// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"fmt"
	"sync"

	"github.com/devicechain-io/dc-microservice/auth"
	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-update-management/model"
	gql "github.com/graph-gophers/graphql-go"
)

// ArtifactSigner looks a signer up by token.
func (r *SchemaResolver) ArtifactSigner(ctx context.Context, args struct{ Token string }) (*ArtifactSignerResolver, error) {
	if err := auth.Authorize(ctx, auth.ArtifactRead); err != nil {
		return nil, err
	}
	api, err := r.GetApi(ctx)
	if err != nil {
		return nil, err
	}
	s, err := api.SignerByToken(ctx, args.Token)
	if err != nil || s == nil {
		return nil, err
	}
	return &ArtifactSignerResolver{api: api, M: *s}, nil
}

// ArtifactSigners searches signers.
func (r *SchemaResolver) ArtifactSigners(ctx context.Context, args struct {
	Criteria struct {
		PageNumber int32
		PageSize   int32
		State      *string
	}
}) (*ArtifactSignerSearchResultsResolver, error) {
	if err := auth.Authorize(ctx, auth.ArtifactRead); err != nil {
		return nil, err
	}
	api, err := r.GetApi(ctx)
	if err != nil {
		return nil, err
	}
	c := args.Criteria
	found, err := api.Signers(ctx, c.State, rdb.Pagination{PageNumber: c.PageNumber, PageSize: c.PageSize})
	if err != nil {
		return nil, err
	}
	return &ArtifactSignerSearchResultsResolver{api: api, M: *found}, nil
}

// RegisterArtifactSigner registers a trusted public key. It needs artifact:trust, not
// artifact:write: publishing a binary and deciding whose signature makes it trusted are separate
// decisions.
func (r *SchemaResolver) RegisterArtifactSigner(ctx context.Context, args struct {
	Name         string
	PublicKeyPem string
}) (*ArtifactSignerResolver, error) {
	if err := auth.Authorize(ctx, auth.ArtifactTrust); err != nil {
		return nil, err
	}
	api, err := r.GetApi(ctx)
	if err != nil {
		return nil, err
	}
	s, err := api.RegisterSigner(ctx, args.Name, args.PublicKeyPem)
	if err != nil {
		return nil, err
	}
	return &ArtifactSignerResolver{api: api, M: *s}, nil
}

// RevokeArtifactSigner revokes a signer.
func (r *SchemaResolver) RevokeArtifactSigner(ctx context.Context, args struct{ Token string }) (*ArtifactSignerResolver, error) {
	if err := auth.Authorize(ctx, auth.ArtifactTrust); err != nil {
		return nil, err
	}
	api, err := r.GetApi(ctx)
	if err != nil {
		return nil, err
	}
	s, err := api.RevokeSigner(ctx, args.Token)
	if err != nil {
		return nil, err
	}
	return &ArtifactSignerResolver{api: api, M: *s}, nil
}

// AddArtifactSignature attaches a verified signature to an artifact.
func (r *SchemaResolver) AddArtifactSignature(ctx context.Context, args struct {
	ArtifactToken string
	SignerToken   string
	Signature     string
}) (*ArtifactResolver, error) {
	if err := auth.Authorize(ctx, auth.ArtifactWrite); err != nil {
		return nil, err
	}
	api, err := r.GetApi(ctx)
	if err != nil {
		return nil, err
	}
	a, err := api.AddSignature(ctx, args.ArtifactToken, args.SignerToken, args.Signature)
	if err != nil {
		return nil, err
	}
	return newArtifactResolver(api, *a), nil
}

// ArtifactSignerResolver resolves the fields of one signer.
type ArtifactSignerResolver struct {
	api *model.Api
	M   model.ArtifactSigner
}

func (r *ArtifactSignerResolver) Id() gql.ID           { return gql.ID(fmt.Sprint(r.M.ID)) }
func (r *ArtifactSignerResolver) CreatedAt() *string   { return gqlcore.FormatTime(r.M.CreatedAt) }
func (r *ArtifactSignerResolver) Token() string        { return r.M.Token }
func (r *ArtifactSignerResolver) Name() string         { return r.M.Name }
func (r *ArtifactSignerResolver) Algorithm() string    { return r.M.Algorithm }
func (r *ArtifactSignerResolver) PublicKeyPem() string { return r.M.PublicKeyPem }
func (r *ArtifactSignerResolver) KeyId() string        { return r.M.KeyId }
func (r *ArtifactSignerResolver) State() string        { return r.M.State }

func (r *ArtifactSignerResolver) RevokedAt() *string {
	if !r.M.RevokedAt.Valid {
		return nil
	}
	return gqlcore.FormatTime(r.M.RevokedAt.Time)
}

func (r *ArtifactSignerResolver) CreatedBy() *string { return nonEmpty(r.M.CreatedBy) }
func (r *ArtifactSignerResolver) RevokedBy() *string { return nonEmpty(r.M.RevokedBy) }

// SoleSignedArtifactCount previews a revoke: the artifacts that would fall back to UNVERIFIED.
func (r *ArtifactSignerResolver) SoleSignedArtifactCount(ctx context.Context) (int32, error) {
	n, err := r.api.SoleSignedArtifactCount(ctx, &r.M)
	return int32(n), err
}

func nonEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// ArtifactSignerSearchResultsResolver resolves a page of signers.
type ArtifactSignerSearchResultsResolver struct {
	api *model.Api
	M   model.SignerSearchResults
}

func (r *ArtifactSignerSearchResultsResolver) Results() []*ArtifactSignerResolver {
	out := make([]*ArtifactSignerResolver, 0, len(r.M.Results))
	for _, s := range r.M.Results {
		out = append(out, &ArtifactSignerResolver{api: r.api, M: s})
	}
	return out
}

func (r *ArtifactSignerSearchResultsResolver) Pagination() *searchResultsPaginationResolver {
	return &searchResultsPaginationResolver{M: r.M.Pagination}
}

// signatureLoader loads the signatures of a set of artifacts on first use, once.
type signatureLoader struct {
	api  *model.Api
	ids  []uint
	once sync.Once
	got  map[uint][]model.SignatureView
	err  error
}

func newSignatureLoader(api *model.Api, ids ...uint) *signatureLoader {
	return &signatureLoader{api: api, ids: ids}
}

func (l *signatureLoader) of(ctx context.Context, id uint) ([]model.SignatureView, error) {
	l.once.Do(func() { l.got, l.err = l.api.SignaturesOf(ctx, l.ids...) })
	return l.got[id], l.err
}

// Signatures lists the artifact's signatures. It is loaded only when selected.
func (r *ArtifactResolver) Signatures(ctx context.Context) ([]*artifactSignatureResolver, error) {
	if r.sigs == nil {
		return nil, fmt.Errorf("artifact %q was resolved without a signature loader", r.M.Token)
	}
	views, err := r.sigs.of(ctx, r.M.ID)
	if err != nil {
		return nil, err
	}
	out := make([]*artifactSignatureResolver, 0, len(views))
	for _, v := range views {
		out = append(out, &artifactSignatureResolver{api: r.sigs.api, v: v})
	}
	return out, nil
}

// artifactSignatureResolver resolves one signature. The signature bytes are deliberately not a field.
type artifactSignatureResolver struct {
	api *model.Api
	v   model.SignatureView
}

func (r *artifactSignatureResolver) Id() gql.ID { return gql.ID(fmt.Sprint(r.v.Signature.ID)) }
func (r *artifactSignatureResolver) CreatedAt() *string {
	return gqlcore.FormatTime(r.v.Signature.CreatedAt)
}
func (r *artifactSignatureResolver) CreatedBy() *string { return nonEmpty(r.v.Signature.Actor) }
func (r *artifactSignatureResolver) CountsTowardTrust() bool {
	return r.v.CountsTowardTrust()
}
func (r *artifactSignatureResolver) Signer() *ArtifactSignerResolver {
	return &ArtifactSignerResolver{api: r.api, M: r.v.Signer}
}
