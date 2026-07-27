// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"fmt"
	"io/fs"
	"regexp"
	"strings"
	"testing"

	assets "github.com/devicechain-io/dc-deploy"
)

// The CloudNativePG operator (ADR-020 A2) is installed on every bootstrap, HA or
// not. The Barman Cloud backup plugin that gives it PITR (ADR-028) is NOT: it
// renders a cert-manager Issuer and two Certificates, so on a cluster with no
// cert-manager CRDs its Helm release fails outright and takes the bootstrap with
// it.
//
// That makes "cert-manager is off" and "database backups are on" an unbootable
// combination. infraVars emits both vars from a single append so they cannot
// drift; this is the test that says why, and that fails if someone splits them.

// varsMap turns the "name=value" list the apply is given into something
// assertable. Reading the OUTPUT of infraVars is the whole point: an assertion
// against the flags on State would keep passing after the vars stopped being
// wired to them, which is the only failure this test exists to catch.
func varsMap(t *testing.T, st *State) map[string]string {
	t.Helper()

	out := map[string]string{}
	for _, v := range infraVars(st) {
		name, value, ok := strings.Cut(v, "=")
		if !ok {
			t.Fatalf("infraVars produced %q, which is not name=value", v)
		}
		// Last occurrence wins, matching how tofu treats repeated -var.
		out[name] = value
	}
	return out
}

// infraStates is the matrix of bootstraps whose vars must satisfy the invariant.
// It is deliberately broader than the one path that disables cert-manager today,
// because the invariant is about the RELATIONSHIP between the two vars, not about
// compact — a future preset that drops cert-manager for its own reasons has to
// fail here rather than at apply time on someone's cluster.
func infraStates() map[string]*State {
	states := map[string]*State{}
	for _, compact := range []bool{false, true} {
		for _, noTLS := range []bool{false, true} {
			for _, ha := range []bool{false, true} {
				st := compactState(compact)
				st.NoTLS = noTLS
				st.HA = ha
				name := fmt.Sprintf("compact=%v/noTLS=%v/ha=%v", compact, noTLS, ha)
				states[name] = st
			}
		}
	}
	return states
}

func TestDisablingCertManagerAlsoDisablesDatabaseBackups(t *testing.T) {
	// Positive control. Every assertion below is of the form "if cert-manager is
	// off then ...", so a matrix in which cert-manager is never off satisfies all
	// of them while measuring nothing — and that is not a hypothetical, it is what
	// this test becomes the day compact stops dropping cert-manager. Counting the
	// cases that actually reach the interesting branch is what keeps a green run
	// meaningful.
	reached := 0

	for name, st := range infraStates() {
		t.Run(name, func(t *testing.T) {
			vars := varsMap(t, st)
			if vars["enable_cert_manager"] != "false" {
				return
			}
			reached++

			if got := vars["enable_database_backups"]; got != "false" {
				t.Errorf("this bootstrap turns cert-manager OFF but leaves database backups at %q.\n"+
					"  The barman-cloud plugin's chart renders an Issuer and two Certificates, so its\n"+
					"  Helm release fails against the missing CRDs and the whole bootstrap fails with it.\n"+
					"  Emit both vars from the same append in infraVars — a comment is not a coupling.",
					firstNonEmpty(got, "<unset>"))
			}
		})
	}

	if reached == 0 {
		t.Fatal("no bootstrap in the matrix disabled cert-manager, so every assertion above was " +
			"vacuously true and this test proved nothing. Either infraStates no longer covers the " +
			"path that drops cert-manager, or nothing drops it any more — in which case delete this " +
			"test rather than leaving it green and blind")
	}
	t.Logf("the invariant was exercised on %d of the matrix's bootstraps", reached)
}

// The other direction is NOT an invariant and must not become one by accident:
// backups off with cert-manager on is a perfectly good install (someone who wants
// TLS but keeps their database backups elsewhere). What must hold is that a
// DEFAULT bootstrap ends up with backups, and that claim spans two languages —
// dcctl passes no enable_database_backups at all on the default path, so the
// answer lives entirely in the OpenTofu variable's default.
//
// So this reads BOTH halves. An assertion on the Go side alone would be vacuous
// (absent var, nothing to compare) and an assertion on the tofu side alone would
// keep passing after dcctl started overriding it. The first version of this test
// checked `if got, ok := vars[...]; ok && got != "true"` and passed for exactly
// the wrong reason: the key is never present.
func TestTheDefaultBootstrapKeepsDatabaseBackupsOn(t *testing.T) {
	// Half one: dcctl must not turn it off on the default path.
	vars := varsMap(t, compactState(false))
	if got := vars["enable_database_backups"]; got == "false" {
		t.Errorf("a default bootstrap passes enable_database_backups=false.\n" +
			"  An install with the CNPG operator and no backup plugin has database HA and NO\n" +
			"  point-in-time recovery, and nothing about the Cluster resources shows the difference.")
	}
	if got := vars["enable_cert_manager"]; got == "false" {
		t.Errorf("a default bootstrap passes enable_cert_manager=false, which takes backups with it")
	}

	// Half two: with dcctl silent, the OpenTofu default is the whole answer.
	if got := tofuVariableDefault(t, "enable_database_backups"); got != "true" {
		t.Errorf("the OpenTofu default for enable_database_backups is %q, not \"true\".\n"+
			"  dcctl passes no value for it on the default path, so this default IS whether a\n"+
			"  DeviceChain install is backed up.", got)
	}
}

// tofuVariableDefault reads a variable's default out of the EMBEDDED OpenTofu
// root — the same bytes the apply runs, not a copy of the repo file — and fails
// if it cannot find the variable at all, so a rename surfaces as a failure rather
// than as an empty string that quietly matches nothing.
func tofuVariableDefault(t *testing.T, name string) string {
	t.Helper()

	body, err := fs.ReadFile(assets.OpenTofu(), "variables.tf")
	if err != nil {
		t.Fatalf("reading the embedded variables.tf: %v", err)
	}

	// Match `variable "name" { ... default = <value>` up to the first default in
	// the block. Deliberately narrow: this is a pin on one value, not a HCL parser.
	re := regexp.MustCompile(`(?s)variable\s+"` + regexp.QuoteMeta(name) + `"\s*\{.*?\n\s*default\s*=\s*(\S+)`)
	m := re.FindSubmatch(body)
	if m == nil {
		t.Fatalf("no variable %q with a default was found in the embedded variables.tf.\n"+
			"  If it was renamed, this test is no longer pinning anything and must follow it.", name)
	}
	return strings.Trim(string(m[1]), `"`)
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
