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
	for _, tc := range []struct {
		name   string
		ledger map[string]peerWiringCount
	}{
		{"absent", nil},
		{"too low", map[string]peerWiringCount{file: {0, 0}}},
		{"too high", map[string]peerWiringCount{file: {2, 2}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			problems := strings.Join(checkPeerWiring(found, tc.ledger), "\n")
			for _, kind := range []string{"urls", "mints"} {
				if !strings.Contains(problems, file+": "+kind) {
					t.Fatalf("planted %s mismatch escaped: %s", kind, problems)
				}
			}
		})
	}
	if problems := checkPeerWiring(found, map[string]peerWiringCount{file: {1, 1}}); len(problems) != 0 {
		t.Fatal(problems)
	}
	writeMintFixture(t, root, file, `package fixture
// fmt.Sprintf("http://%s:%d/graphql", "comment", 80)
var text = "http://%s:%d/graphql"`)
	found, err = peerWiringUnder(root)
	if err != nil || len(found) != 0 {
		t.Fatalf("comment-only source counted: %v, %v", found, err)
	}
	if len(checkPeerWiring(found, map[string]peerWiringCount{file: {1, 1}})) != 2 {
		t.Fatal("removed calls did not fail ledger")
	}
}
