// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"os/exec"
	"strconv"
	"strings"
)

// Container ports a kind cluster built from the embedded topology publishes on the
// host, keyed by what they carry. The HOST side is whatever the cluster was created
// with, which is why the address dcctl prints is read back rather than assumed.
const (
	KindIngressHTTPPort  = 80
	KindIngressHTTPSPort = 443
	KindMQTTNodePort     = localMQTTNodePort
)

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
	cp, def := KindIngressHTTPPort, 80
	if scheme == "https" {
		cp, def = KindIngressHTTPSPort, 443
	}
	if p, ok := KindPublishedHostPort(kubeContext, cp); ok && p != def && !strings.Contains(host, ":") {
		return scheme + "://" + host + ":" + strconv.Itoa(p) + "/"
	}
	return scheme + "://" + host + "/"
}
