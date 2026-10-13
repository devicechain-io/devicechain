// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package adapter

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

func deviceEnabledAnswer(token string, enabled any) any {
	return map[string]any{"devicesByToken": []map[string]any{{"token": token, "enabled": enabled}}}
}

// 🔴 Disabled ASKS EVERY TIME. The token cache answers "which device is this" for an hour, so a
// device disabled after it resolved would still read as live; a registration is rare enough to pay
// one round trip for the truth. It also fails closed: an answer that cannot say is an error, never
// "enabled".
func TestDisabledReadsTheDevicesStateFreshEveryCall(t *testing.T) {
	enabled := true
	gql := &fakeGraphQL{responder: func(query string, vars map[string]any) (any, error) {
		return deviceEnabledAnswer("lw-1", enabled), nil
	}}
	r := NewRegistrar(gql, "url", "lw-", nil)

	got, err := r.Disabled(context.Background(), "acme", "lw-1")
	require.NoError(t, err)
	require.False(t, got)

	enabled = false
	got, err = r.Disabled(context.Background(), "acme", "lw-1")
	require.NoError(t, err)
	require.True(t, got, "a device disabled after the first answer read as enabled (answer cached)")
}

func TestDisabledFailsClosedOnAnAnswerItCannotRead(t *testing.T) {
	for name, resp := range map[string]func() (any, error){
		"transport error":  func() (any, error) { return nil, errors.New("device-management unreachable") },
		"no enabled field": func() (any, error) { return deviceEnabledAnswer("lw-1", nil), nil },
	} {
		gql := &fakeGraphQL{responder: func(string, map[string]any) (any, error) { return resp() }}
		_, err := NewRegistrar(gql, "url", "lw-", nil).Disabled(context.Background(), "acme", "lw-1")
		require.Error(t, err, name)
	}
	// A device the tenant no longer has is not disabled: there is nothing to quarantine.
	gql := &fakeGraphQL{responder: func(string, map[string]any) (any, error) {
		return map[string]any{"devicesByToken": []map[string]any{}}, nil
	}}
	got, err := NewRegistrar(gql, "url", "lw-", nil).Disabled(context.Background(), "acme", "lw-1")
	require.NoError(t, err)
	require.False(t, got)
}
