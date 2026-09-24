// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package publish

import (
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/devicechain-io/dc-outbound-connectors/connectorspec"
)

// A proxy named in the pod's environment is never used. If it were, the proxy's address
// would be the only one the egress guard saw — it is allowed, or public — and the proxy
// would then fetch whatever the connector named, with nothing in any log to say so.
//
// The body runs in a child process, because the standard library reads the proxy
// environment once per process. The proxy is a listener in the PARENT, so its accept
// count survives the child. The destinations are unresolvable names: without a proxy each
// send fails on DNS, and with one the proxy would be dialed instead.
func TestProxyEnvironmentIsIgnored(t *testing.T) {
	const name = "TestProxyEnvironmentIsIgnored"
	if os.Getenv(childEnv) != name {
		proxy := listen(t, "tcp", "127.0.0.1:0", nil)
		addr := "127.0.0.1:" + proxy.port()
		runInChild(t, name,
			"ALL_PROXY=socks5://"+addr, "all_proxy=socks5://"+addr,
			"HTTPS_PROXY=http://"+addr, "https_proxy=http://"+addr,
			"HTTP_PROXY=http://"+addr, "http_proxy=http://"+addr,
			"NO_PROXY=", "no_proxy=")
		time.Sleep(50 * time.Millisecond)
		assert.Equal(t, int32(0), proxy.accepts.Load(), "a connector send reached the environment's proxy")
		return
	}

	// Anchor: the environment IS in effect in this process, so a client that consulted it
	// would have gone to the proxy. Without this, zero accepts could mean the variables
	// never arrived.
	req, err := http.NewRequest(http.MethodPost, "http://sns.invalid/", nil)
	require.NoError(t, err)
	p, err := http.ProxyFromEnvironment(req)
	require.NoError(t, err)
	require.NotNil(t, p, "the proxy environment is not in effect in the child")

	s := NewSender(guardAllowing("127.0.0.0/8", "::1/128"))
	targets := []connectorspec.Target{
		mqttTarget(t, "tcp://broker.invalid:1883"),
		mqttTarget(t, "ssl://broker.invalid:8883"),
		mqttTarget(t, "ws://broker.invalid:80/mqtt"),
		mqttTarget(t, "wss://broker.invalid:443/mqtt"),
		snsTarget(t, "http://sns.invalid"),
		snsTarget(t, "https://sns.invalid"),
		sqsTarget(t, "http://sqs.invalid"),
	}
	for _, target := range targets {
		err := s.Send(sendCtx(t, 3*time.Second), target, []byte("x"), "k")
		assert.Error(t, err, "%#v: an unresolvable destination cannot succeed", target)
	}
}
