// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"testing"

	"github.com/devicechain-io/dc-microservice/resolvedevent"
	"github.com/stretchr/testify/require"
)

// A payload decoder's refusal is served as DECODE_FAILED, filterable by that name; an older
// event-management maps the number to UNKNOWN (reasonCode carries it), which is safe mid-rollout.
func TestADecodeFailureIsServedAsDecodeFailed(t *testing.T) {
	require.Equal(t, "DECODE_FAILED", reasonEnum(int32(resolvedevent.FailureReason_DecodeFailed)))
	code, ok := reasonCodeOf("DECODE_FAILED")
	require.True(t, ok)
	require.EqualValues(t, resolvedevent.FailureReason_DecodeFailed, code)
}
