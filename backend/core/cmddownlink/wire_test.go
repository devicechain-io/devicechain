// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmddownlink

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestWireNamesAreLiterals pins every cross-service spelling against a LITERAL. These strings are
// a contract with command-delivery that no compiler checks, so a test comparing a constant to
// itself would pass whatever either side were renamed to.
func TestWireNamesAreLiterals(t *testing.T) {
	assert.Equal(t, "Dc-Transport", HeaderTransport)
	assert.Equal(t, "Dc-Command-Impact", HeaderCommandImpact)
	assert.Equal(t, "UNANSWERED", ReachUnanswered)
	assert.Equal(t, "UNSENT", ReachUnsent)
}

// TestResponseEnvelopeJSON pins the response shape command-delivery decodes: field names, the
// always-present nonce, and the omission of reach and payload on a bare success.
func TestResponseEnvelopeJSON(t *testing.T) {
	msg := "no"
	b, err := json.Marshal(ResponseEnvelope{CommandToken: "c1", Success: false, Error: &msg,
		Reach: ReachUnsent, DispatchNonce: "n1"})
	require.NoError(t, err)
	assert.JSONEq(t, `{"commandToken":"c1","success":false,"error":"no","reach":"UNSENT","dispatchNonce":"n1"}`, string(b))

	b, err = json.Marshal(ResponseEnvelope{CommandToken: "c1", Success: true})
	require.NoError(t, err)
	assert.JSONEq(t, `{"commandToken":"c1","success":true,"dispatchNonce":""}`, string(b),
		"the nonce stays on the wire even when empty, so a refused answer is diagnosable")
}

func TestDeliveryEnvelopeJSON(t *testing.T) {
	var env DeliveryEnvelope
	require.NoError(t, json.Unmarshal([]byte(
		`{"token":"c1","deviceToken":"d1","name":"sparkplug.write","payload":{"metric":"x"},"dispatchNonce":"n"}`), &env))
	assert.Equal(t, "c1", env.Token)
	assert.Equal(t, "d1", env.DeviceToken)
	assert.Equal(t, "sparkplug.write", env.Name)
	assert.JSONEq(t, `{"metric":"x"}`, string(*env.Payload))
	assert.Equal(t, "n", env.DispatchNonce)
}

type scriptedWriter struct {
	failFirst int
	calls     int
	tenant    string
	device    string
	value     []byte
}

func (w *scriptedWriter) WriteToDevice(ctx context.Context, device string, msgs ...messaging.Message) error {
	w.calls++
	w.tenant, _ = core.TenantFromContext(ctx)
	w.device = device
	if w.calls <= w.failFirst {
		return errors.New("publish refused")
	}
	w.value = msgs[0].Value
	return nil
}

// TestPublishResponse covers the retry bound from both sides: a transient failure is retried to
// success, and a persistent one stops at the bound and reports the error rather than looping or
// swallowing it.
func TestPublishResponse(t *testing.T) {
	env := ResponseEnvelope{CommandToken: "c1", Success: true, DispatchNonce: "n1"}

	t.Run("retries a transient failure", func(t *testing.T) {
		w := &scriptedWriter{failFirst: 2}
		require.NoError(t, PublishResponse(w, "acme", "dev-1", env, 3, time.Millisecond))
		assert.Equal(t, 3, w.calls)
		assert.Equal(t, "acme", w.tenant, "the tenant context is built from the tenant string")
		assert.Equal(t, "dev-1", w.device)
		assert.JSONEq(t, `{"commandToken":"c1","success":true,"dispatchNonce":"n1"}`, string(w.value))
	})
	t.Run("stops at the bound and reports", func(t *testing.T) {
		w := &scriptedWriter{failFirst: 100}
		err := PublishResponse(w, "acme", "dev-1", env, 3, time.Millisecond)
		require.Error(t, err)
		assert.Equal(t, 3, w.calls, "exactly the bound, no more")
		assert.Contains(t, err.Error(), "c1")
	})
	t.Run("refuses a non-positive bound", func(t *testing.T) {
		w := &scriptedWriter{}
		require.Error(t, PublishResponse(w, "acme", "dev-1", env, 0, 0))
		assert.Zero(t, w.calls, "attempts below 1 must not silently publish nothing as success")
	})
}
