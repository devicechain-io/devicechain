// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"errors"
	"strings"
	"testing"
)

const sampleDockerPort = "80/tcp -> 0.0.0.0:8080\n80/tcp -> [::]:8080\n443/tcp -> 0.0.0.0:8443\n31883/tcp -> 0.0.0.0:11883\n6443/tcp -> 127.0.0.1:41999\n"

func TestParseDockerPort(t *testing.T) {
	if p, ok := parseDockerPort(sampleDockerPort, 31883); !ok || p != 11883 {
		t.Fatalf("got %d %v", p, ok)
	}
	if _, ok := parseDockerPort(sampleDockerPort, 9999); ok {
		t.Fatal("unpublished port must not resolve")
	}
	if _, ok := parseDockerPort("garbage", 80); ok {
		t.Fatal("garbage must not resolve")
	}
}

func TestConsoleURL(t *testing.T) {
	old := dockerPortOutput
	defer func() { dockerPortOutput = old }()
	dockerPortOutput = func(string) (string, error) { return sampleDockerPort, nil }
	if got := ConsoleURL("kind-x", "http", "localhost"); got != "http://localhost:8080/" {
		t.Errorf("got %s", got)
	}
	if got := ConsoleURL("kind-x", "https", "localhost"); got != "https://localhost:8443/" {
		t.Errorf("got %s", got)
	}
	if got := ConsoleURL("gke-x", "http", "localhost"); got != "http://localhost/" {
		t.Errorf("non-kind context must fall back, got %s", got)
	}
	dockerPortOutput = func(string) (string, error) { return "", errors.New("no docker") }
	if got := ConsoleURL("kind-x", "http", "localhost"); got != "http://localhost/" {
		t.Errorf("got %s", got)
	}
	dockerPortOutput = func(string) (string, error) { return "80/tcp -> 0.0.0.0:80\n", nil }
	if got := ConsoleURL("kind-x", "http", "localhost"); got != "http://localhost/" {
		t.Errorf("got %s", got)
	}
}

// The summary's GraphQL line must carry the same host:port as the console line.
func TestReportGraphQLAddressMatchesConsolePort(t *testing.T) {
	old := dockerPortOutput
	defer func() { dockerPortOutput = old }()
	dockerPortOutput = func(string) (string, error) { return sampleDockerPort, nil }
	st := aWritableState()
	st.KubeContext = "kind-x"
	st.Values = map[string]string{"ingressHost": "localhost", "scheme": "http"}
	out := captureStdout(t, func() {
		if err := stepReport(t.Context(), st); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(out, "http://localhost:8080/") {
		t.Fatalf("console line lost its port:\n%s", out)
	}
	if !strings.Contains(out, "http://localhost:8080/api/<area>/graphql") {
		t.Errorf("GraphQL line disagrees with the console port:\n%s", out)
	}
}
