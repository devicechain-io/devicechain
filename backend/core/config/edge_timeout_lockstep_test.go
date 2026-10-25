// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"os"
	"regexp"
	"strconv"
	"testing"
	"time"
)

// RequestEdgeTimeout is the one edge every synchronous browser request lives inside, and
// the edge controller is configured in a different module and a different language. Under
// ingress-nginx it was the controller's default proxy-read-timeout, which is why nothing
// ever had to state it. Traefik has no such default (its response-header timeout is 0, i.e.
// none) and HTTPRoute timeouts are not implemented by it, so the controller's module sets it
// explicitly, and this holds that number to the constant the rest of the platform budgets
// against: AiDraftBudget and the GraphQL execution default are both derived from it.
//
// 🔴 THE FILE READ LIVES OUTSIDE THIS MODULE and Go's test cache does not track it, so a
// plain `go test` serves a stale PASS over an edit there. CI runs every module with -count=1.
const traefikModuleMain = "../../../deploy/opentofu/modules/traefik/main.tf"

func TestTraefikResponseHeaderTimeoutIsTheRequestEdge(t *testing.T) {
	b, err := os.ReadFile(traefikModuleMain)
	if err != nil {
		t.Fatalf("read %s: %v", traefikModuleMain, err)
	}
	src := string(b)

	m := regexp.MustCompile(`(?m)^\s*response_header_timeout_seconds\s*=\s*(\d+)\s*$`).FindStringSubmatch(src)
	if m == nil {
		t.Fatal("response_header_timeout_seconds not found in the Traefik module: the parse is broken, or the local was renamed")
	}
	secs, _ := strconv.Atoi(m[1])
	if got := time.Duration(secs) * time.Second; got != RequestEdgeTimeout {
		t.Errorf("the Traefik module's response-header timeout is %v, but config.RequestEdgeTimeout is %v: "+
			"a synchronous request is cut at one while the platform budgets against the other", got, RequestEdgeTimeout)
	}

	// And the local actually reaches the controller as the argument that carries it.
	if !regexp.MustCompile(`responseHeaderTimeout=\$\{local\.response_header_timeout_seconds\}s`).MatchString(src) {
		t.Error("the Traefik module no longer passes --serversTransport.forwardingTimeouts.responseHeaderTimeout " +
			"from response_header_timeout_seconds, so the edge timeout is not in force")
	}
}
