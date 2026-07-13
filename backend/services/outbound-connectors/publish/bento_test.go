// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package publish

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSendDeliversToDropOutput verifies a bounded send resolves successfully when the
// output accepts the message. `drop` (a pure output) acks every message, standing in for
// a real connector without needing a broker.
func TestSendDeliversToDropOutput(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err := Send(ctx, "drop: {}", []byte(`{"hello":"world"}`), map[string]string{"idempotency_key": "k1"})
	require.NoError(t, err)
}

// TestSendReturnsRejection verifies a bounded send surfaces a delivery failure: `reject`
// (a pure output) nacks every message with the interpolated error, so the producer
// handler returns non-nil rather than falsely reporting success.
func TestSendReturnsRejection(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err := Send(ctx, `reject: "nope"`, []byte(`{}`), nil)
	require.Error(t, err)
}

// TestSendRejectsEmptyOutput fails closed on an empty output config (a generator bug),
// never silently "succeeding" a message with nowhere to go. It is a TERMINAL config error.
func TestSendRejectsEmptyOutput(t *testing.T) {
	err := Send(context.Background(), "", []byte(`{}`), nil)
	assert.ErrorIs(t, err, ErrPublishConfig)
}

// TestSendRejectsBadConfigTerminal surfaces an unknown/unbuildable output config as a
// TERMINAL error (ErrPublishConfig), so the executor dead-letters rather than retrying a
// deterministic config failure.
func TestSendRejectsBadConfigTerminal(t *testing.T) {
	err := Send(context.Background(), "this_output_does_not_exist: {}", []byte(`{}`), nil)
	assert.ErrorIs(t, err, ErrPublishConfig)
}

// TestSendDoesNotInterpolateHostEnv is the regression guard for the ${VAR} host-secret
// exfiltration vector: Bento would otherwise substitute a config-level ${NAME} from the
// process environment before parsing. `reject` uses its config string as the rejection
// error, so if the host env were consulted the canary value would surface in the returned
// error. With env interpolation disabled in Send it must not.
func TestSendDoesNotInterpolateHostEnv(t *testing.T) {
	t.Setenv("DC_LEAK_CANARY", "leaked-secret-value")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err := Send(ctx, `reject: "${DC_LEAK_CANARY}"`, []byte(`{}`), nil)
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "leaked-secret-value",
		"a config-level ${VAR} must not be interpolated from the host environment")
}
