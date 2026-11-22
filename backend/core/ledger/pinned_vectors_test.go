// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package ledger

import (
	"encoding/json"
	"strings"
	"testing"
)

// Digests are persisted by the engine release, so their bytes are fixed forever once used. These
// vectors pin them, including explicit nulls for every absent ceiling: a change such as adding
// omitempty to a nullable field passes every relative test (it still differs from a populated
// policy) but moves every stored digest, and only a fixed vector notices.
func TestPinnedDigestVectors(t *testing.T) {
	five := int64(5)
	for _, tc := range []struct {
		name              string
		safety            Safety
		contract, request string
	}{
		{"all ceilings null", Safety{SettleDeadlineSeconds: 60},
			"sha256:9cc7ff6f922e330968a875337a53ee6367196615b6faf16069b86892487388aa",
			"sha256:712694765628484d28ad3ef4fbbf8c39a6c369216056b99a15be44a83208022d"},
		{"rate only", Safety{MaxPostsPerSecond: &five, SettleDeadlineSeconds: 60},
			"sha256:dccbe205d4fe340109013f9a0d0fbad44eeeee9731be5be568e5d33d12356b1e",
			"sha256:1b1878c1dfb437fe107cdd96797bb1018685acc21bb6cca41a09324116b99a47"},
		{"failure threshold only", Safety{FailureThreshold: &five, SettleDeadlineSeconds: 60},
			"sha256:faa45a40c56933f5b65f6ca87cd18602f7e958c38f7bcf49d59ac52306d9e83a",
			"sha256:7e6b0e435c938885958c6cdab5b3ab9fd1b645c618a8ca870c8d39814a6d9301"},
		{"ambiguity threshold only", Safety{AmbiguityThreshold: &five, SettleDeadlineSeconds: 60},
			"sha256:1de6da60663f8aca59cecf46ddc8a005383bfa4c224ded2575a2221e31a065ec",
			"sha256:479a7d27ae337501beaedcae86c20c7b5d20ca13058ee62898e71d2f1d2a0b0a"},
	} {
		contract := ContractDigestInput{V: 1, Kind: "config.set", Intent: map[string]any{"key": "k"}, PlanDigest: "sha256:00",
			Safety: tc.safety, Approval: Approval{Kind: "NONE", PolicyVersion: 1}}
		if got, err := ContractDigest(contract); err != nil || got != tc.contract {
			t.Errorf("%s: ContractDigest = %s, %v; want %s", tc.name, got, err, tc.contract)
		}
		request := RequestDigestInput{Kind: "config.set", Intent: map[string]any{"key": "k"}, TargetTokens: []string{"b", "a"}, Safety: tc.safety}
		if got, err := RequestDigest(request); err != nil || got != tc.request {
			t.Errorf("%s: RequestDigest = %s, %v; want %s", tc.name, got, err, tc.request)
		}
	}
	want := `{"approval":{"kind":"NONE","policyVersion":1},"intent":{"key":"k"},"kind":"config.set",` +
		`"overrides":{"lineSelection":null,"overrideOrdinals":null,"reversalOf":null},"planDigest":"sha256:00",` +
		`"safety":{"ambiguityThreshold":null,"failureThreshold":null,"maxPostsPerSecond":null,"settleDeadlineSeconds":60},"v":1}`
	got, err := Canonical(ContractDigestInput{V: 1, Kind: "config.set", Intent: map[string]any{"key": "k"}, PlanDigest: "sha256:00",
		Safety: Safety{SettleDeadlineSeconds: 60}, Approval: Approval{Kind: "NONE", PolicyVersion: 1}})
	if err != nil || string(got) != want {
		t.Fatalf("canonical contract bytes moved:\n got %s\nwant %s (%v)", got, want, err)
	}
}

// JSON allows an exponent that expands to gigabytes; canonicalization refuses it instead of
// allocating, at both ends of the scale.
func TestCanonicalRefusesAnOversizedNumber(t *testing.T) {
	for _, n := range []string{"1e9223372036854775806", "1e-9223372036854775806", "1e1025", "1e-1025", "1" + strings.Repeat("1", 1025)} {
		if _, err := Canonical(json.Number(n)); err == nil {
			t.Errorf("%s: an oversized number must be refused", n[:min(len(n), 24)])
		}
	}
	if got, err := Canonical(json.Number("1e1000")); err != nil || len(got) != 1001 {
		t.Errorf("a large but bounded number must still canonicalize: %d bytes, %v", len(got), err)
	}
}
