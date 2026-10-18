// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-event-processing/internal/preview"
	"github.com/devicechain-io/dc-microservice/calcfield"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type stubConstants struct {
	byDevice  map[string][]calcfield.Constant
	truncated bool
	err       error
}

func (s stubConstants) DeviceConstants(context.Context, string, string, []string, int) (map[string][]calcfield.Constant, bool, error) {
	return s.byDevice, s.truncated, s.err
}

// A preview whose draft reads device constants says what it could not read; it never answers as
// if every device had a default.
func TestAPreviewSaysWhenItCannotReadDeviceConstants(t *testing.T) {
	keys := []string{"calibration"}
	ctx := context.Background()

	_, note := (&SchemaResolver{}).draftConstants(ctx, "acme", "meter", keys)
	assert.Contains(t, note, "cannot be read from this service")
	assert.Contains(t, note, "calibration")
	assert.Contains(t, note, "missing_input")

	_, note = (&SchemaResolver{Constants: stubConstants{err: errors.New("boom")}}).draftConstants(ctx, "acme", "meter", keys)
	assert.Contains(t, note, "could not be read")
	assert.Contains(t, note, "missing_input")

	got, note := (&SchemaResolver{Constants: stubConstants{truncated: true,
		byDevice: map[string][]calcfield.Constant{"d1": {calcfield.NewConstant("calibration", 1)}}}}).draftConstants(ctx, "acme", "meter", keys)
	require.Len(t, got, 1, "what was read is still used")
	assert.True(t, strings.Contains(note, "first 10000 devices"), note)
	assert.Equal(t, 10_000, preview.MaxConstantDevices)

	got, note = (&SchemaResolver{Constants: stubConstants{
		byDevice: map[string][]calcfield.Constant{"d1": {calcfield.NewConstant("calibration", 1)}}}}).draftConstants(ctx, "acme", "meter", keys)
	assert.Len(t, got, 1)
	assert.Empty(t, note, "a complete read has nothing to say")
}
