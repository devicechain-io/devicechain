// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

// The source address a resolver sees is the nearest hop's: the peer when nothing
// proxied the request, the LAST X-Forwarded-For entry when something did (the earlier
// entries are whatever the client sent).
func TestSourceAddrIsTheNearestHop(t *testing.T) {
	r := httptest.NewRequest("POST", "/graphql", nil)
	r.RemoteAddr = "192.0.2.10:5555"
	require.Equal(t, "192.0.2.10", requestSourceAddr(r))

	r.Header.Set("X-Forwarded-For", "6.6.6.6, 198.51.100.7")
	require.Equal(t, "198.51.100.7", requestSourceAddr(r), "a client-supplied leading entry must not win")

	r.Header.Set("X-Forwarded-For", " ")
	require.Equal(t, "192.0.2.10", requestSourceAddr(r))
}
