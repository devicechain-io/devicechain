// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// Keep the published edge rule tied to the probe contract. Run uncached: Go's
// test cache does not track the documentation outside this module.
func TestProductionHardeningDocumentsAndBlocksProbeContract(t *testing.T) {
	data, err := os.ReadFile("../../../docs/docs/deployment/production-hardening.md")
	if err != nil {
		t.Fatal(err)
	}
	doc := string(data)
	match := regexp.MustCompile(`location ~\* (\S+) \{\s+return 403;`).FindStringSubmatch(doc)
	if len(match) != 2 {
		t.Fatal("production hardening must provide an explicit NGINX deny rule returning 403")
	}
	deny, err := regexp.Compile("(?i)" + match[1])
	if err != nil {
		t.Fatal(err)
	}
	for _, probe := range probeContract {
		if !strings.Contains(doc, "`"+probe.path+"`") {
			t.Errorf("public-port inventory omits %s", probe.path)
		}
		for _, area := range []string{"device-management", "user-management", "future-area"} {
			for _, suffix := range []string{"", "/", "/child"} {
				path := "/api/" + area + probe.path + suffix
				if !deny.MatchString(path) {
					t.Errorf("documented edge rule allows %s", path)
				}
			}
		}
	}
	for _, path := range []string{"/api/device-management/graphiql", "/API/device-management/METRICS"} {
		if !deny.MatchString(path) {
			t.Errorf("documented edge rule allows %s", path)
		}
	}
	for _, path := range []string{"/api/user-management/graphql", "/api/device-management/metrics-extra", "/.well-known/oauth-authorization-server", "/", "/healthz", "/metrics"} {
		if deny.MatchString(path) {
			t.Errorf("documented edge rule blocks API, discovery, console or direct probe path %s", path)
		}
	}
}
