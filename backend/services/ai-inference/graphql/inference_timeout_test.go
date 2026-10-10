// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/devicechain-io/dc-ai-inference/inference"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// slowProvider answers only when its context ends, the way an HTTP provider call
// surfaces an expired deadline: wrapped, as the real providers wrap client.Do errors.
type slowProvider struct{}

func (slowProvider) Infer(ctx context.Context, _ inference.Input) (inference.Output, error) {
	<-ctx.Done()
	return inference.Output{}, fmt.Errorf("call inference provider https://provider.example: %w", ctx.Err())
}

func resolvedSlow() *inference.Resolved {
	return &inference.Resolved{Provider: slowProvider{}, Token: "slow", Kind: "claude"}
}

// A provider that overruns the configured inference timeout is reported as TIMED OUT —
// to the tenant as the bare sentinel, not collapsed into "unavailable", which would
// send the author to an operator over a model that was merely slow.
func TestInferenceTimeoutIsReportedAsTimedOut(t *testing.T) {
	res := inference.NewResolver(nil, nil, nil, inference.Bounds{Timeout: 20 * time.Millisecond}, nil)
	_, err := runInference(context.Background(), res, nil, resolvedSlow(), InferenceRequestInput{Prompt: "p"})
	require.Error(t, err)
	assert.ErrorIs(t, err, inference.ErrTimedOut)
	assert.Equal(t, outcomeTimedOut, outcomeFor(err))

	safe := tenantSafeError(err)
	assert.Same(t, inference.ErrTimedOut, safe, "the tenant sees the bare sentinel, no provider detail")
	assert.NotContains(t, safe.Error(), "provider.example")
}

// The counterweight: a CALLER that went away is not a slow model. Only the inference
// deadline firing with the caller still live classifies as timed out.
func TestCallerCancellationIsNotATimeout(t *testing.T) {
	res := inference.NewResolver(nil, nil, nil, inference.Bounds{Timeout: time.Minute}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := runInference(ctx, res, nil, resolvedSlow(), InferenceRequestInput{Prompt: "p"})
	require.Error(t, err)
	assert.False(t, errors.Is(err, inference.ErrTimedOut), "caller deadline misread as a provider timeout: %v", err)
}
