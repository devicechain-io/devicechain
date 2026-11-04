// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// The ceiling-shrink override is off unless the Job is given it by name.
func TestTheShrinkOverrideIsOffUnlessAsked(t *testing.T) {
	e := env(map[string]string{"DC_INSTANCE_ID": "acme"})
	r, err := parseReconcile([]string{"--areas=device-management"}, e)
	require.NoError(t, err)
	require.False(t, r.allowShrink)
	r, err = parseReconcile([]string{"--areas=device-management", "--allow-ceiling-shrink"}, e)
	require.NoError(t, err)
	require.True(t, r.allowShrink)
}
