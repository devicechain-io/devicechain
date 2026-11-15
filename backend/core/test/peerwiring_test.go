// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package test

import (
	"strings"
	"testing"
)

func TestPeerWiringNegativeControls(t *testing.T) {
	root := t.TempDir()
	const file = "backend/services/fixture/main.go"
	writeMintFixture(t, root, file, `package fixture
import "fmt"
import svcclient "github.com/devicechain-io/dc-microservice/svcclient"
func wire() { fmt.Sprintf("http://%s:%d/graphql", "host", 80); svcclient.New(nil) }
// fmt.Sprintf("http://%s:%d/graphql", "comment", 80)
var comment = "http://%s:%d/graphql"
`)
	found, err := peerWiringUnder(root)
	if err != nil {
		t.Fatal(err)
	}
	if found[file] != (peerWiringCount{1, 1}) {
		t.Fatalf("AST scan counted comment/string or missed calls: %v", found)
	}
	// Neither a missing entry nor URL permission may excuse a direct mint.
	for _, allowed := range []map[string]bool{nil, {file: true}} {
		problems := strings.Join(checkPeerWiring(found, allowed), "\n")
		if !strings.Contains(problems, file+": mints") {
			t.Fatalf("planted mint escaped: %s", problems)
		}
		if allowed == nil && !strings.Contains(problems, file+": urls") {
			t.Fatalf("planted URL escaped: %s", problems)
		}
	}
	// URL permission is per file, with no count ledger.
	if problems := checkPeerWiring(map[string]peerWiringCount{file: {2, 0}}, map[string]bool{file: true}); len(problems) != 0 {
		t.Fatal(problems)
	}
	writeMintFixture(t, root, file, `package fixture
// fmt.Sprintf("http://%s:%d/graphql", "comment", 80)
var text = "http://%s:%d/graphql"`)
	found, err = peerWiringUnder(root)
	if err != nil || len(found) != 0 {
		t.Fatalf("comment-only source counted: %v, %v", found, err)
	}
	if len(checkPeerWiring(found, map[string]bool{file: true})) != 1 {
		t.Fatal("removed URL did not fail allowlist")
	}
}
