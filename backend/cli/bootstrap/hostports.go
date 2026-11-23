// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"os/exec"
	"strconv"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Container ports a kind cluster built from the embedded topology publishes on the
// host, keyed by what they carry. The HOST side is whatever the cluster was created
// with, which is why the address dcctl prints is read back rather than assumed.
const (
	KindIngressHTTPPort  = 80
	KindIngressHTTPSPort = 443
	KindMQTTNodePort     = localMQTTNodePort
)

// MQTTNodePortNone is what an instance record holds for a local instance that was given NO MQTT
// node port (every port the cluster publishes was another instance's). Recorded explicitly, so it
// is told apart from a record that predates the field.
const MQTTNodePortNone = -1

// ResolveInstanceMQTTNodePort is the node port a local instance's MQTT gateway is exposed on, or 0
// when it has none (its broker is reachable in-cluster only).
//
//   - a recorded port of the set is the answer, and a recorded "none" (MQTTNodePortNone) is 0;
//   - a record that predates the field (or none at all) is settled by the instance's own Services:
//     the one of localMQTTNodePorts it holds, and 0 when it holds none, which is how an instance
//     built before there were more ports, second on its cluster, is told apart from the first;
//   - only when the cluster cannot be read does it fall back to KindMQTTNodePort, the port every
//     local instance that had one had before.
func ResolveInstanceMQTTNodePort(ctx context.Context, kubeContext, instance string) int {
	if rec, err := ReadInstanceRecord(instance); err == nil {
		if rec.MQTTNodePort == MQTTNodePortNone {
			return 0
		}
		for _, p := range localMQTTNodePorts {
			if int(p) == rec.MQTTNodePort {
				return int(p)
			}
		}
	}
	own, err := instanceOwnMQTTNodePort(ctx, kubeContext, instance)
	if err != nil {
		return KindMQTTNodePort
	}
	return int(own)
}

// instanceOwnMQTTNodePort is the one of localMQTTNodePorts a Service in the instance's own namespace
// holds, or 0. A variable so a test can answer without a cluster.
var instanceOwnMQTTNodePort = func(ctx context.Context, kubeContext, instance string) (int32, error) {
	_, _, typed, err := kubeClients(kubeContext)
	if err != nil {
		return 0, err
	}
	svcs, err := typed.CoreV1().Services(InstanceNamespace(instance)).List(ctx, metav1.ListOptions{})
	if err != nil {
		return 0, err
	}
	for _, s := range svcs.Items {
		for _, p := range s.Spec.Ports {
			if isLocalMQTTNodePort(p.NodePort) {
				return p.NodePort, nil
			}
		}
	}
	return 0, nil
}

// dockerPortOutput is the one window onto `docker port`, a variable so a test can
// present any published-port table without a Docker daemon.
var dockerPortOutput = func(container string) (string, error) {
	out, err := exec.Command("docker", "port", container).Output()
	return string(out), err
}

// KindPublishedHostPort reports the host port the kind cluster behind kubeContext
// publishes for a control-plane container port. ok is false whenever it cannot be
// determined (not a kind context, no Docker, port not published), and callers fall
// back to the conventional port rather than guessing.
func KindPublishedHostPort(kubeContext string, containerPort int) (int, bool) {
	cluster := clusterNameFromKindContext(kubeContext)
	if cluster == "" {
		return 0, false
	}
	out, err := dockerPortOutput(cluster + "-control-plane")
	if err != nil {
		return 0, false
	}
	return parseDockerPort(out, containerPort)
}

// KindPublishedHostPorts reads every container-port to host-port mapping the kind cluster behind
// kubeContext publishes, from ONE `docker port` call. ok is false when it cannot be read.
func KindPublishedHostPorts(kubeContext string) (map[int]int, bool) {
	cluster := clusterNameFromKindContext(kubeContext)
	if cluster == "" {
		return nil, false
	}
	out, err := dockerPortOutput(cluster + "-control-plane")
	if err != nil {
		return nil, false
	}
	m := map[int]int{}
	for _, line := range strings.Split(out, "\n") {
		left, _, found := strings.Cut(line, "->")
		if !found {
			continue
		}
		cp, err := strconv.Atoi(strings.TrimSuffix(strings.TrimSpace(left), "/tcp"))
		if err != nil {
			continue
		}
		if hp, ok := parseDockerPort(line, cp); ok {
			if _, seen := m[cp]; !seen {
				m[cp] = hp
			}
		}
	}
	return m, true
}

// parseDockerPort reads `docker port` lines ("80/tcp -> 0.0.0.0:8080", possibly
// repeated for an IPv6 binding) and returns the host port for containerPort/tcp.
func parseDockerPort(out string, containerPort int) (int, bool) {
	want := strconv.Itoa(containerPort) + "/tcp"
	for _, line := range strings.Split(out, "\n") {
		left, right, found := strings.Cut(line, "->")
		if !found || strings.TrimSpace(left) != want {
			continue
		}
		right = strings.TrimSpace(right)
		i := strings.LastIndex(right, ":")
		if i < 0 {
			continue
		}
		if p, err := strconv.Atoi(right[i+1:]); err == nil && p > 0 && p < 65536 {
			return p, true
		}
	}
	return 0, false
}

// ConsoleURL formats the console address for a scheme and host, adding the host
// port the kind cluster really publishes for ingress when it is not the scheme's
// default. Falls back to scheme://host/ when it cannot be determined.
func ConsoleURL(kubeContext, scheme, host string) string {
	return IngressBase(kubeContext, scheme, host) + "/"
}

// IngressBase is scheme://host[:port] with no trailing slash: the origin every
// ingress-served address (console, /api/<area>/graphql) hangs off. The port is
// the one the kind cluster really publishes, so no printed address can disagree
// with another about where the instance listens.
func IngressBase(kubeContext, scheme, host string) string {
	cp, def := KindIngressHTTPPort, 80
	if scheme == "https" {
		cp, def = KindIngressHTTPSPort, 443
	}
	if p, ok := KindPublishedHostPort(kubeContext, cp); ok && p != def && !strings.Contains(host, ":") {
		return scheme + "://" + host + ":" + strconv.Itoa(p)
	}
	return scheme + "://" + host
}
