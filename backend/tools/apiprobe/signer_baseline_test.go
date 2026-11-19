// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 🔴 THE ANSWER THE RIG PARSES, as printed. hack/upgrade-rig.sh allows unverified artifacts on
// its instance only when this does NOT say "supported". The current tree, which signs the
// artifact it assigns, must answer "supported" (or the rig would go on deviating from the shipped
// default in exactly the area under test), and a tree without the signer registry must not (or
// the baseline seed's assignment would be refused).
func TestSignerBaselinePrintsTheWordTheRigParses(t *testing.T) {
	var out bytes.Buffer
	if err := runSignerBaselineTo([]string{"--baseline-schemas", filepath.Join("..", "..", "services")}, &out); err != nil {
		t.Fatal(err)
	}
	if out.String() != "supported\n" {
		t.Fatalf("the current tree answered %q, want exactly \"supported\\n\"", out.String())
	}

	// A release whose update-management predates the signer registry.
	dir := t.TempDir()
	graphqlDir := filepath.Join(dir, "update-management", "graphql")
	if err := os.MkdirAll(graphqlDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(graphqlDir, "schema.graphql"), []byte("type Query { ping: String }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := runSignerBaselineTo([]string{"--baseline-schemas", dir}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out.String(), "unsupported: ") {
		t.Fatalf("a tree with no signer registry answered %q", out.String())
	}

	// No tree is a setup failure, never an answer the rig could read as either word.
	out.Reset()
	assertCode(t, runSignerBaselineTo(nil, &out), exitSetup)
	if out.Len() != 0 {
		t.Fatalf("a refused question still printed an answer: %q", out.String())
	}
}

// 🔑 THE ASSIGNED ARTIFACT IS SIGNED BEFORE IT IS ASSIGNED. The platform refuses to assign an
// unsigned artifact on a default instance, so the signer and its signature over the very
// artifact update-assignment and update-rollout name must be seeded before either, and the
// signature must verify against that artifact's declared digest with the probe signer's key, as
// the platform checks it.
func TestTheAssignedArtifactIsSignedBeforeItIsAssigned(t *testing.T) {
	index := map[string]int{}
	for i, e := range allEntities() {
		index[e.Name] = i
	}
	for _, name := range []string{"artifact-available", "artifact-signer", "artifact-available-signature",
		"update-assignment", "update-rollout"} {
		if _, ok := index[name]; !ok {
			t.Fatalf("the table has no %q row", name)
		}
	}
	before := func(a, b string) {
		t.Helper()
		if index[a] >= index[b] {
			t.Errorf("%s is seeded at %d, after %s at %d; it must come first", a, index[a], b, index[b])
		}
	}
	before("artifact-available", "artifact-available-signature")
	before("artifact-signer", "artifact-available-signature")
	before("artifact-available-signature", "update-assignment")
	before("artifact-available-signature", "update-rollout")

	sig, _ := tableEntityNamed("artifact-available-signature")
	assign, _ := tableEntityNamed("update-assignment")
	rollout, _ := tableEntityNamed("update-rollout")
	avail, _ := tableEntityNamed("artifact-available")
	st := newState("t")
	signed := sig.Vars(st)
	if got, want := signed["artifactToken"], st.tok("artifact-available"); got != want {
		t.Errorf("the signature is attached to %v, want the assigned artifact %v", got, want)
	}
	if got := assign.Vars(st)["artifactToken"]; got != signed["artifactToken"] {
		t.Errorf("update-assignment assigns %v, but the signature is on %v", got, signed["artifactToken"])
	}
	req, _ := rollout.Vars(st)["req"].(map[string]any)
	if got := req["artifactToken"]; got != signed["artifactToken"] {
		t.Errorf("update-rollout rolls out %v, but the signature is on %v", got, signed["artifactToken"])
	}
	if signed["signerToken"] != probeSignerToken() {
		t.Errorf("the signature names signer %v, want the probe signer %s", signed["signerToken"], probeSignerToken())
	}

	// The signature verifies over the declared digest, as AddSignature checks it.
	declared, _ := avail.Vars(st)["req"].(map[string]any)
	digestText, _ := declared["digest"].(string)
	raw := sha256.Sum256([]byte(probeArtifactContent))
	if digestText != probeArtifactDigest() {
		t.Fatalf("artifact-available declares %q, want the probe digest", digestText)
	}
	sigBytes, err := base64.StdEncoding.DecodeString(signed["signature"].(string))
	if err != nil {
		t.Fatalf("the signature is not base64: %v", err)
	}
	if !ed25519.Verify(probeSignerKey().Public().(ed25519.PublicKey), raw[:], sigBytes) {
		t.Fatal("the signature does not verify over the assigned artifact's digest with the probe signer's key")
	}

	// The trust is read back where it is settled, so verify fails if an upgrade loses it.
	if !strings.Contains(sig.Fields, "trustStatus") {
		t.Errorf("artifact-available-signature reads %q, want its trustStatus", sig.Fields)
	}
	if !strings.Contains(assign.Fields, "trustStatus") {
		t.Errorf("update-assignment reads %q, want its artifact's trustStatus", assign.Fields)
	}
	// ...and NOT where it moves after the row is recorded.
	if strings.Contains(avail.Fields, "trustStatus") {
		t.Errorf("artifact-available reads %q: its trust changes when the next rows sign it, so verify "+
			"would report a healthy instance as changed", avail.Fields)
	}
}
