// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package otafleet

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/devicechain-io/dc-simulator/internal/fakemqtt"
	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/stretchr/testify/require"
)

// A device publishes the moment its session is up, which is when paho's session resume walks the
// client store and flags every stored publish DUP while the outgoing goroutine reads the same
// packet. Run under -race: with a plain memory store that is a data race inside paho (seen in CI
// as a failure of whichever fleet test attached next); with the fleet's store there is nothing
// for resume to walk. Many short sessions, because the window is narrow.
func TestPublishOnConnectDoesNotRaceSessionResume(t *testing.T) {
	b := fakemqtt.New(t)
	var wg sync.WaitGroup
	for i := range 150 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			opts := mqtt.NewClientOptions().AddBroker(b.URL()).SetClientID(fmt.Sprintf("c-%d", i))
			opts.SetCleanSession(false).SetStore(newClientStore())
			published := make(chan mqtt.Token, 1)
			opts.OnConnect = func(c mqtt.Client) { published <- c.Publish("t/reports", 1, false, []byte("x")) }
			c := mqtt.NewClient(opts)
			require.True(t, c.Connect().WaitTimeout(10*time.Second))
			require.True(t, (<-published).WaitTimeout(10*time.Second))
			c.Disconnect(0)
		}()
	}
	wg.Wait()
}
