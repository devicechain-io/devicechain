// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"context"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// A sim for an instance whose broker is reachable in-cluster only is refused with the way out, and
// never pointed at the cluster's first MQTT port, which is another instance's.
func TestSimCreateRefusesAnInstanceWithNoMQTTNodePort(t *testing.T) {
	orig := resolveInstanceMQTTNodePort
	t.Cleanup(func() { resolveInstanceMQTTNodePort = orig })
	resolveInstanceMQTTNodePort = func(context.Context, string, string) int { return 0 }

	cmd := &cobra.Command{}
	cmd.Flags().String("kube-context", "kind-dev", "")
	cmd.SetContext(context.Background())
	broker, err := clusterMqttBroker(cmd, "localhost", "beta")
	if err == nil {
		t.Fatalf("an instance with no MQTT node port was addressed at %q", broker)
	}
	if !strings.Contains(err.Error(), "--"+flagMqttBroker) || !strings.Contains(err.Error(), "in-cluster only") {
		t.Errorf("the refusal does not say why or what to do: %v", err)
	}

	// A non-loopback server is not this function's business, port or no port.
	if broker, err := clusterMqttBroker(cmd, "devicechain.example.com", "beta"); err != nil || broker != "" {
		t.Errorf("a remote server got %q, %v", broker, err)
	}
}
