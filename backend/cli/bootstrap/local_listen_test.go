// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	assets "github.com/devicechain-io/dc-deploy"
	"sigs.k8s.io/yaml"
)

type kindPortMappings struct {
	Nodes []struct {
		ExtraPortMappings []struct {
			ContainerPort int32  `json:"containerPort"`
			HostPort      int32  `json:"hostPort"`
			ListenAddress string `json:"listenAddress"`
		} `json:"extraPortMappings"`
	} `json:"nodes"`
}

// listenAddresses is every host port the config publishes, mapped to the address it binds.
func listenAddresses(t *testing.T, cfg []byte) map[int32]string {
	t.Helper()
	var parsed kindPortMappings
	if err := yaml.Unmarshal(cfg, &parsed); err != nil {
		t.Fatal(err)
	}
	got := map[int32]string{}
	for _, n := range parsed.Nodes {
		for _, m := range n.ExtraPortMappings {
			got[m.HostPort] = m.ListenAddress
		}
	}
	if len(got) == 0 {
		t.Fatal("the config publishes no host port at all")
	}
	return got
}

// 🔴 kind BINDS AN extraPortMapping WITH NO listenAddress ON EVERY INTERFACE. The local
// cluster serves the console (plain HTTP under --no-tls, superuser sign-in included) and the
// MQTT broker on those ports, so a default local install was reachable from the whole LAN.
// Loopback is the default, the checked-in file says so on its own (it is what a person
// running `kind create cluster --config` gets), and only an explicit --expose-lan widens it.
func TestTheLocalClusterPublishesOnLoopbackUnlessAskedNotTo(t *testing.T) {
	for port, addr := range listenAddresses(t, assets.KindClusterConfig()) {
		if addr != "127.0.0.1" {
			t.Errorf("the checked-in kind config publishes host port %d on %q, not loopback", port, addr)
		}
	}
	for _, c := range []struct {
		exposeLAN bool
		want      string
	}{{false, "127.0.0.1"}, {true, "0.0.0.0"}} {
		cfg, err := kindClusterConfig(c.exposeLAN)
		if err != nil {
			t.Fatal(err)
		}
		for port, addr := range listenAddresses(t, cfg) {
			if addr != c.want {
				t.Errorf("exposeLAN=%v: host port %d is published on %q, want %q", c.exposeLAN, port, addr, c.want)
			}
		}
	}
}

// The choice reaches the cluster kind actually creates: the config file handed to it, read
// while kind still could, through the same EnsureCluster an install calls.
func TestEnsureClusterCreatesTheClusterWithTheChosenListenAddress(t *testing.T) {
	dir := t.TempDir()
	name := "kind"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	if err := os.WriteFile(filepath.Join(dir, name), nil, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	kubeconfig := filepath.Join(dir, "kubeconfig")
	if err := os.WriteFile(kubeconfig, []byte("apiVersion: v1\nkind: Config\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KUBECONFIG", kubeconfig)
	original := runKindCommand
	t.Cleanup(func() { runKindCommand = original })

	for _, c := range []struct {
		exposeLAN bool
		want      string
	}{{false, "127.0.0.1"}, {true, "0.0.0.0"}} {
		var seen []byte
		runKindCommand = func(cmd *exec.Cmd) error {
			for i, a := range cmd.Args {
				if a == "--config" && i+1 < len(cmd.Args) {
					b, err := os.ReadFile(cmd.Args[i+1])
					if err != nil {
						return err
					}
					seen = b
				}
			}
			return nil
		}
		if _, err := (localProvider{}).EnsureCluster(t.Context(), Options{
			Cluster: "listen", CreateCluster: true, AssumeYes: true, ExposeLAN: c.exposeLAN,
		}); err != nil {
			t.Fatal(err)
		}
		if seen == nil {
			t.Fatal("kind was not given a config")
		}
		for port, addr := range listenAddresses(t, seen) {
			if addr != c.want {
				t.Errorf("exposeLAN=%v: kind was told to publish host port %d on %q, want %q", c.exposeLAN, port, addr, c.want)
			}
		}
	}
}
