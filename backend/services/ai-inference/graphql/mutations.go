// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"

	"github.com/devicechain-io/dc-ai-inference/model"
	"github.com/devicechain-io/dc-microservice/auth"
)

// CreateAiProvider creates a new provider.
func (r *SchemaResolver) CreateAiProvider(ctx context.Context, args struct {
	Request model.AIProviderCreateRequest
}) (*AIProviderResolver, error) {
	if err := auth.Authorize(ctx, auth.AIAdmin); err != nil {
		return nil, err
	}
	created, err := r.GetApi(ctx).CreateAIProvider(ctx, &args.Request)
	if err != nil {
		return nil, err
	}
	return &AIProviderResolver{M: *created, S: r, C: ctx}, nil
}

// UpdateAiProvider updates the provider with the given (current) token.
// expectedUpdatedAt, when supplied, is an optimistic-concurrency precondition.
func (r *SchemaResolver) UpdateAiProvider(ctx context.Context, args struct {
	Token             string
	Request           model.AIProviderCreateRequest
	ExpectedUpdatedAt *string
}) (*AIProviderResolver, error) {
	if err := auth.Authorize(ctx, auth.AIAdmin); err != nil {
		return nil, err
	}
	updated, err := r.GetApi(ctx).UpdateAIProvider(ctx, args.Token, &args.Request, args.ExpectedUpdatedAt)
	if err != nil {
		return nil, err
	}
	return &AIProviderResolver{M: *updated, S: r, C: ctx}, nil
}

// DeleteAiProvider deletes the provider with the given token.
func (r *SchemaResolver) DeleteAiProvider(ctx context.Context, args struct {
	Token string
}) (bool, error) {
	if err := auth.Authorize(ctx, auth.AIAdmin); err != nil {
		return false, err
	}
	return r.GetApi(ctx).DeleteAIProvider(ctx, args.Token)
}

// SetActiveAiProvider promotes the provider with the given token to THE active one.
func (r *SchemaResolver) SetActiveAiProvider(ctx context.Context, args struct {
	Token string
}) (*AIProviderResolver, error) {
	if err := auth.Authorize(ctx, auth.AIAdmin); err != nil {
		return nil, err
	}
	active, err := r.GetApi(ctx).SetActiveProvider(ctx, args.Token)
	if err != nil {
		return nil, err
	}
	return &AIProviderResolver{M: *active, S: r, C: ctx}, nil
}

// ClearActiveAiProvider clears the active provider (return to "default of none").
func (r *SchemaResolver) ClearActiveAiProvider(ctx context.Context) (bool, error) {
	if err := auth.Authorize(ctx, auth.AIAdmin); err != nil {
		return false, err
	}
	if err := r.GetApi(ctx).ClearActiveProvider(ctx); err != nil {
		return false, err
	}
	return true, nil
}
