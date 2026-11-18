// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"
)

// Keep the published Production hardening page tied to the listener contract: the
// ports it names, the probe paths it says live on the operations port, the outside
// checks it tells an operator to run, and its explorer deny rule. Run uncached: Go's
// test cache does not track the documentation outside this module.
func TestProductionHardeningDocumentsTheListenerContract(t *testing.T) {
	data, err := os.ReadFile("../../../docs/docs/deployment/production-hardening.md")
	if err != nil {
		t.Fatal(err)
	}
	doc := string(data)

	for _, port := range []int{HttpPort, OpsPort} {
		if !strings.Contains(doc, fmt.Sprintf("**%d**", port)) {
			t.Errorf("port table omits port %d", port)
		}
	}
	opsRow := regexp.MustCompile(fmt.Sprintf(`(?m)^\| \*\*%d\*\* \|.*$`, OpsPort)).FindString(doc)
	if opsRow == "" {
		t.Fatalf("port table has no row for the operations port %d", OpsPort)
	}
	for _, probe := range probeContract {
		if !strings.Contains(opsRow, "`"+probe.path+"`") {
			t.Errorf("operations-port row omits %s", probe.path)
		}
		check := "curl -i https://INSTANCE_HOST/api/device-management" + probe.path + "\n"
		if !strings.Contains(doc, check) {
			t.Errorf("outside check omits %s", probe.path)
		}
	}

	match := regexp.MustCompile(`location ~\* (\S+) \{\s+return 403;`).FindStringSubmatch(doc)
	if len(match) != 2 {
		t.Fatal("production hardening must provide an explicit NGINX explorer deny rule returning 403")
	}
	deny, err := regexp.Compile("(?i)" + match[1])
	if err != nil {
		t.Fatal(err)
	}
	for _, area := range []string{"device-management", "user-management", "future-area"} {
		for _, suffix := range []string{"", "/", "/child"} {
			if path := "/api/" + area + "/graphiql" + suffix; !deny.MatchString(path) {
				t.Errorf("documented explorer rule allows %s", path)
			}
		}
	}
	if !deny.MatchString("/API/device-management/GRAPHIQL") {
		t.Error("documented explorer rule is case-sensitive")
	}
	for _, path := range []string{"/api/user-management/graphql", "/api/device-management/graphiql-extra", "/.well-known/oauth-authorization-server", "/", "/graphiql"} {
		if deny.MatchString(path) {
			t.Errorf("documented explorer rule blocks API, discovery, console or direct path %s", path)
		}
	}
}
