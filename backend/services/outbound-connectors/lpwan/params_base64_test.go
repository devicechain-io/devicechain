// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package lpwan

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// The base64 text bound admits up to 243 decoded bytes (324 characters), so the decoded length has its
// own check: 242 passes, 243 does not.
func TestBase64PayloadBoundaryIsOnDecodedBytes(t *testing.T) {
	ok := base64.StdEncoding.EncodeToString([]byte(strings.Repeat("a", 242)))
	got, err := ParseParams([]byte(`{"fPort":1,"base64":"` + ok + `"}`))
	require.NoError(t, err)
	require.Len(t, got.Data, 242)

	over := base64.StdEncoding.EncodeToString([]byte(strings.Repeat("a", 243)))
	_, err = ParseParams([]byte(`{"fPort":1,"base64":"` + over + `"}`))
	require.Error(t, err)
	var pe *ParamError
	require.ErrorAs(t, err, &pe)
}
