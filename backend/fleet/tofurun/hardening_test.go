// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package tofurun

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// A secret that straddles the cut of the diagnostic tail is scrubbed BEFORE the cut, so neither half
// of it is shown.
func TestDiagnosticTailScrubsBeforeItCuts(t *testing.T) {
	secret := "s3cr3t-value-that-is-long-enough-to-straddle"
	// Place the secret so the tailCap cut lands in the middle of it.
	raw := strings.Repeat("x", 5000) + secret + strings.Repeat("y", tailCap-len(secret)/2)
	tail := diagnosticTail([]byte(raw), []string{secret})
	require.NotContains(t, tail, secret[:12], "the front half of the secret survived the cut")
	require.NotContains(t, tail, secret[len(secret)/2:], "the back half of the secret survived the cut")
	require.LessOrEqual(t, len(tail), tailCap)

	// Control: scrubbing AFTER cutting (the defect) would have shown half of it.
	cut := raw[len(raw)-tailCap:]
	require.Contains(t, scrub(cut, []string{secret}), secret[len(secret)/2:], "the control: cut-then-scrub leaks the half")
}

func TestSecretBearingTypesRedactEveryVerb(t *testing.T) {
	cfg := Config{Encryption: "enc-secret-key-0123456789"}
	s := &Session{env: []string{"PG_CONN_STR=postgres://u:pw-secret@h/d"}, secrets: []string{"pw-secret"}}
	r := &Runner{cfg: cfg}
	for _, v := range []any{cfg, &cfg, s, r} {
		for _, f := range []string{"%v", "%+v", "%#v", "%s", "%q", "%x", "%d"} {
			out := fmt.Sprintf(f, v)
			require.NotContains(t, out, "enc-secret-key", f)
			require.NotContains(t, out, "pw-secret", f)
		}
	}
}

func TestCredentialNamesAreAllowListed(t *testing.T) {
	for _, ok := range []string{"GOOGLE_OAUTH_ACCESS_TOKEN", "GOOGLE_APPLICATION_CREDENTIALS", "CLOUDSDK_AUTH_ACCESS_TOKEN"} {
		_, err := NewCredential(ClassApply, "p", map[string]string{ok: "v"})
		require.NoError(t, err, ok)
	}
	_, err := NewCredential(ClassApply, "p", map[string]string{"FAKE_X": "v"})
	require.Error(t, err)
	_, err = NewCredentialAllowing(ClassApply, "p", map[string]string{"FAKE_X": "v"}, "FAKE_")
	require.NoError(t, err, "a further prefix is allowed only when the caller names it")
	_, err = NewCredentialAllowing(ClassApply, "p", map[string]string{"TF_LOG": "TRACE"}, "TF_")
	require.Error(t, err, "TF_ stays the runner's even when a caller allows the prefix")
}

func TestModuleRefusesTofuOverridesAndShadows(t *testing.T) {
	for name := range map[string]string{"override.tofu": "x", "b_override.tofu": "x", "a.override.tofu": "x"} {
		_, _, err := inspectModule(writeTree(t, map[string]string{"main.tf": "#\n", name: "x"}))
		require.ErrorIs(t, err, ErrModuleRefused, name)
	}
	_, _, err := inspectModule(writeTree(t, map[string]string{"main.tf": "#\n", "main.tofu": "# replaces main.tf\n"}))
	require.ErrorIs(t, err, ErrModuleRefused, "a .tofu file shadows the .tf of the same name")
	// Control: a .tofu file with no .tf of that name is ordinary.
	_, _, err = inspectModule(writeTree(t, map[string]string{"main.tf": "#\n", "extra.tofu": "#\n"}))
	require.NoError(t, err)
}

func TestErroredPlanIsARefusalFact(t *testing.T) {
	doc := []byte(`{"format_version":"1.2","errored":true,"resource_changes":[]}`)
	f := Extract(doc)
	require.NotEmpty(t, f.Facts)
	require.NotEmpty(t, f.Facts[0].Refusal)
	require.False(t, allowT.Judge(KindCreate, f).Allowed)
	// Control: the same document not errored is an empty plan, allowed.
	ok := Extract([]byte(`{"format_version":"1.2","errored":false,"resource_changes":[]}`))
	require.True(t, allowT.Judge(KindCreate, ok).Allowed)
}

func TestDeletionProtectionIsAForbiddenPathExceptForUnprotect(t *testing.T) {
	flip := func(kind Kind) Verdict {
		return allowT.Judge(kind, facts(t, rc("t.a", "t", []string{"update"}, map[string]any{"deletion_protection": true, "name": "n"}, map[string]any{"deletion_protection": false, "name": "n"})))
	}
	for _, k := range []Kind{KindCreate, KindChange, KindUpgrade, KindRecover, KindDriftCheck, KindDestroy} {
		require.False(t, flip(k).Allowed, string(k))
	}
	require.True(t, flip(KindUnprotect).Allowed)
	require.True(t, flip(KindReprotect).Allowed)

	// An UNPROTECT run may change nothing else, in the same resource or another.
	both := facts(t, rc("t.a", "t", []string{"update"}, map[string]any{"deletion_protection": true, "name": "n"}, map[string]any{"deletion_protection": false, "name": "m"}))
	require.False(t, allowT.Judge(KindUnprotect, both).Allowed)
	other := facts(t, rc("t.b", "t", []string{"update"}, map[string]any{"k": 1}, map[string]any{"k": 2}))
	require.False(t, allowT.Judge(KindUnprotect, other).Allowed)
	del := facts(t, rc("t.a", "t", []string{"delete"}, map[string]any{"deletion_protection": true}, nil))
	require.False(t, allowT.Judge(KindUnprotect, del).Allowed)

	// A resource born protected is not a change of its protection.
	born := facts(t, rc("t.a", "t", []string{"create"}, nil, map[string]any{"deletion_protection": true}))
	require.True(t, allowT.Judge(KindCreate, born).Allowed)
	// A nested attribute of the same name counts.
	nested := facts(t, rc("t.a", "t", []string{"update"}, map[string]any{"cfg": map[string]any{"deletion_protection": true}}, map[string]any{"cfg": map[string]any{"deletion_protection": false}}))
	require.False(t, allowT.Judge(KindChange, nested).Allowed)
}

func TestProtectionKindsAreKnownButNotAdmittedByTheWriterSlot(t *testing.T) {
	for _, k := range []Kind{KindUnprotect, KindReprotect} {
		c, err := ClassFor(k)
		require.NoError(t, err)
		require.Equal(t, ClassApply, c)
	}
}
