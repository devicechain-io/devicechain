// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphqlws

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// WithHost presents the routing hostname as the upgrade request's Host while the connection still
// goes to the address in the endpoint.
func TestWithHostPresentsTheHostHeaderOnTheUpgrade(t *testing.T) {
	seen := make(chan string, 2)
	up := websocket.Upgrader{Subprotocols: []string{wsSubprotocol}, CheckOrigin: func(*http.Request) bool { return true }}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen <- r.Host
		conn, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		_, _, _ = conn.ReadMessage()
		_ = conn.WriteJSON(wsMessage{Type: msgConnectionAck})
		_, _, _ = conn.ReadMessage()
	}))
	defer srv.Close()
	endpoint := "ws" + strings.TrimPrefix(srv.URL, "http")

	c, err := Dial(dialCtx(t), endpoint, nil, WithHost("iot.example.com"))
	require.NoError(t, err)
	_ = c.Close()
	assert.Equal(t, "iot.example.com", <-seen)

	// Control: without the option the request carries the address it was dialed at.
	c, err = Dial(dialCtx(t), endpoint, nil)
	require.NoError(t, err)
	_ = c.Close()
	assert.Equal(t, strings.TrimPrefix(srv.URL, "http://"), <-seen)
}

func TestHostNameStripsThePort(t *testing.T) {
	assert.Equal(t, "iot.example.com", hostName("iot.example.com:8443"))
	assert.Equal(t, "iot.example.com", hostName("iot.example.com"))
}
