// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"

	"github.com/devicechain-io/dc-ai-inference/model"
	"github.com/devicechain-io/dc-microservice/auth"
)

// AiProvider looks up a single provider by its token. Returns nil when not found.
func (r *SchemaResolver) AiProvider(ctx context.Context, args struct {
	Token string
}) (*AIProviderResolver, error) {
	if err := auth.Authorize(ctx, auth.AIAdmin); err != nil {
		return nil, err
	}
	matches, err := r.GetApi(ctx).AIProvidersByToken(ctx, []string{args.Token})
	if err != nil {
		return nil, err
	}
	if len(matches) == 0 {
		return nil, nil
	}
	return &AIProviderResolver{M: *matches[0], S: r, C: ctx}, nil
}

// AiProviders searches providers by criteria.
func (r *SchemaResolver) AiProviders(ctx context.Context, args struct {
	Criteria model.AIProviderSearchCriteria
}) (*AIProviderSearchResultsResolver, error) {
	if err := auth.Authorize(ctx, auth.AIAdmin); err != nil {
		return nil, err
	}
	found, err := r.GetApi(ctx).AIProviders(ctx, args.Criteria)
	if err != nil {
		return nil, err
	}
	return &AIProviderSearchResultsResolver{M: *found, S: r, C: ctx}, nil
}

// AiProviderKinds returns the registered provider-kind vocabulary (for a console
// picker). Read-gated like the rest of the surface.
func (r *SchemaResolver) AiProviderKinds(ctx context.Context) ([]string, error) {
	if err := auth.Authorize(ctx, auth.AIAdmin); err != nil {
		return nil, err
	}
	return model.ProviderKinds(), nil
}

// ActiveAiProvider returns THE active provider, or nil when none is active.
func (r *SchemaResolver) ActiveAiProvider(ctx context.Context) (*AIProviderResolver, error) {
	if err := auth.Authorize(ctx, auth.AIAdmin); err != nil {
		return nil, err
	}
	active, err := r.GetApi(ctx).ActiveProvider(ctx)
	if err != nil {
		return nil, err
	}
	if active == nil {
		return nil, nil
	}
	return &AIProviderResolver{M: *active, S: r, C: ctx}, nil
}
