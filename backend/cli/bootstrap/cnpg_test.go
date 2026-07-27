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

// infraStates is the matrix of bootstraps whose vars must satisfy the invariants.
// It is deliberately broader than the paths that disable a prerequisite today,
// because the invariants are about the RELATIONSHIP between vars, not about any
// one preset — a future preset that drops cert-manager for its own reasons has to
// fail here rather than at apply time on someone's cluster.
func infraStates() map[string]*State {
	states := map[string]*State{}
	for _, compact := range []bool{false, true} {
		for _, noTLS := range []bool{false, true} {
			for _, ha := range []bool{false, true} {
				for _, noCNPG := range []bool{false, true} {
					st := compactState(compact)
					st.NoTLS = noTLS
					st.HA = ha
					st.NoCNPG = noCNPG
					name := fmt.Sprintf("compact=%v/noTLS=%v/ha=%v/noCNPG=%v", compact, noTLS, ha, noCNPG)
					states[name] = st
				}
			}
		}
	}
	return states
}

// backupPrereqs are the things the barman-cloud plugin cannot be installed
// without. Each is an implication — prerequisite off ⇒ backups off — and each
// would otherwise fail during `tofu apply`, i.e. at step 2 of a bootstrap, after
// the credentials and the root-key escrow have already been written.
var backupPrereqs = []struct {
	name string
	why  string
}{
	{"enable_cert_manager",
		"the plugin's chart renders an Issuer and two Certificates, so against missing " +
			"cert-manager CRDs its Helm release fails outright"},
	{"enable_cnpg",
		"the plugin is an extension of the CloudNativePG operator, so installing it " +
			"without the operator installs an extension of nothing"},
}

func TestDisablingABackupPrerequisiteAlsoDisablesDatabaseBackups(t *testing.T) {
	// Positive control, per prerequisite. Every assertion below has the form "if
	// this prerequisite is off then ...", so a matrix in which it is never off
	// satisfies it while measuring nothing. That is not hypothetical: it is what
	// the cert-manager half becomes the day compact stops dropping cert-manager.
	// Counting the cases that reach the branch is what keeps a green run meaningful.
	reached := map[string]int{}

	for name, st := range infraStates() {
		t.Run(name, func(t *testing.T) {
			vars := varsMap(t, st)
			for _, p := range backupPrereqs {
				if vars[p.name] != "false" {
					continue
				}
				reached[p.name]++

				if got := vars["enable_database_backups"]; got != "false" {
					t.Errorf("this bootstrap turns %s OFF but leaves database backups at %q.\n"+
						"  %s, and the whole bootstrap fails with it.\n"+
						"  Emit both vars from the same append in infraVars — a comment is not a coupling.",
						p.name, firstNonEmpty(got, "<unset>"), p.why)
				}
			}
		})
	}

	for _, p := range backupPrereqs {
		if reached[p.name] == 0 {
			t.Errorf("no bootstrap in the matrix disabled %s, so its assertion was vacuously true "+
				"and proved nothing. Either infraStates no longer covers the path that drops it, or "+
				"nothing drops it any more — in which case remove it from backupPrereqs rather than "+
				"leaving it green and blind", p.name)
			continue
		}
		t.Logf("%s: the invariant was exercised on %d of the matrix's bootstraps", p.name, reached[p.name])
	}
}

// The other direction is NOT an invariant and must not become one by accident:
// backups off with cert-manager on is a perfectly good install (someone who wants
// TLS but keeps their database backups elsewhere). What must hold is that a
// DEFAULT bootstrap ends up with backups.
//
// 🔑 The EFFECTIVE value is the thing to assert, and it is why this reads across
// two languages. dcctl passes no enable_database_backups at all on the default
// path, so a Go-side comparison has nothing to compare: `vars[k] == "false"` and
// `if got, ok := vars[k]; ok && got != "true"` are BOTH unreachable, and an
// adversarial review caught the second version of this test still passing for
// that reason after the first had been "fixed". Resolving the var against the
// OpenTofu default — the way an actual apply resolves it — is the only form that
// fails when either half moves.
//
// Backups also require the operator, so the operator's own default is pinned too:
// flipping enable_cnpg to false turns PITR off just as completely, and would
// otherwise leave every assertion here green.
func TestTheDefaultBootstrapKeepsDatabaseBackupsOn(t *testing.T) {
	for _, tc := range []struct {
		name  string
		what  string
		fails string
	}{
		{"enable_database_backups", "the barman-cloud plugin, and with it all point-in-time recovery",
			"an install with the CNPG operator and no backup plugin has database HA and NO PITR, " +
				"and nothing about the Cluster resources shows the difference"},
		{"enable_cnpg", "the CloudNativePG operator, which the backup plugin is useless without",
			"the plugin would be installed against no operator"},
		{"enable_cert_manager", "cert-manager, whose absence takes the backup plugin with it",
			"the plugin's Issuer and Certificates have no CRDs to be created against"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := effectiveInfraVar(t, compactState(false), tc.name); got != "true" {
				t.Errorf("a default bootstrap resolves %s to %q, not \"true\", so it does not install %s.\n"+
					"  Consequence: %s.",
					tc.name, got, tc.what, tc.fails)
			}
		})
	}
}

// The value pinned above only reaches the plugin through one wiring line, and that
// line is ordinary HCL nobody would think to look at. Pin it: hard-coding
// `enable_backup_plugin = false` in the module call turns every install's backups
// off while leaving enable_database_backups=true and every assertion above green.
func TestTheBackupFlagIsActuallyWiredToThePlugin(t *testing.T) {
	body, err := fs.ReadFile(assets.OpenTofu(), "main.tf")
	if err != nil {
		t.Fatalf("reading the embedded main.tf: %v", err)
	}

	const wiring = "enable_backup_plugin   = var.enable_database_backups"
	if !strings.Contains(string(body), wiring) {
		t.Errorf("the cnpg module call no longer contains %q.\n"+
			"  enable_database_backups is only the plugin's switch while this line connects them;\n"+
			"  without it the variable is decoration and the tests that pin its default prove nothing.\n"+
			"  If the wiring was legitimately reformatted, update this string — do not delete the test.",
			wiring)
	}
}

// The pins above are only as good as the reader underneath them, and the first
// version of that reader was silently wrong: it ran past its variable's closing
// brace and reported the NEXT variable's default. So the reader gets its own
// assertions, on real variables at the positions where an unbounded scan differs
// from a bounded one.
func TestTheVariableReaderStaysInsideItsOwnBlock(t *testing.T) {
	for _, tc := range []struct{ name, want, why string }{
		{"cnpg_namespace", "cnpg-system",
			"a mid-file variable whose neighbours also declare defaults"},
		{"monitoring_grafana_ingress_host", "",
			"the LAST variable in the file, which has no following declaration to bound it — " +
				"an empty-string default is also the value an off-by-one is most likely to mangle"},
		{"enable_cnpg", "true",
			"a bool immediately followed by a string variable, which is the shape that made the " +
				"unbounded regex return timescale_image's default"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tofuVariableDefault(t, tc.name); got != tc.want {
				t.Errorf("tofuVariableDefault(%q) = %q, want %q (%s).\n"+
					"  A reader that returns a neighbour's value makes every pin above meaningless.",
					tc.name, got, tc.want, tc.why)
			}
		})
	}
}

// effectiveInfraVar resolves a variable the way the apply does: an explicit -var
// from dcctl wins, and otherwise the OpenTofu default applies.
func effectiveInfraVar(t *testing.T, st *State, name string) string {
	t.Helper()

	if v, ok := varsMap(t, st)[name]; ok {
		return v
	}
	return tofuVariableDefault(t, name)
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

	// 🔴 BOUNDED TO THE VARIABLE'S OWN BLOCK, which the first version was not.
	// `variable "x" {...}.*?default` with (?s) happily runs past the closing brace
	// into the NEXT variable's default and reports it as this one's — verified
	// against the real file: deleting enable_database_backups' default made this
	// return timescale_image's. Where the neighbour is a bool that reads "true",
	// the test then passes for a variable that has no default at all.
	//
	// Splitting on top-level `variable "` declarations first makes the miss
	// impossible rather than unlikely. Still not an HCL parser, and does not need
	// to be: it pins one scalar in a file this repo controls.
	chunk := variableBlock(t, string(body), name)

	re := regexp.MustCompile(`(?m)^\s*default\s*=\s*(\S+)`)
	m := re.FindStringSubmatch(chunk)
	if m == nil {
		t.Fatalf("variable %q declares no default in the embedded variables.tf.\n"+
			"  A variable with no default is REQUIRED, and dcctl passes no value for this one —\n"+
			"  so the apply would fail rather than quietly install the wrong thing. Either restore\n"+
			"  the default or make dcctl pass it explicitly.", name)
	}
	return strings.Trim(m[1], `"`)
}

// variableBlock returns the source of one `variable "name" { ... }` declaration,
// bounded by the next top-level declaration rather than by brace matching —
// enough to stop a scan escaping into the following variable, and immune to the
// nested braces that appear in validation blocks and object defaults.
func variableBlock(t *testing.T, source, name string) string {
	t.Helper()

	const decl = "\nvariable \""
	start := strings.Index(source, decl+name+"\"")
	if start < 0 {
		t.Fatalf("no variable %q was found in the embedded variables.tf.\n"+
			"  If it was renamed, this test is no longer pinning anything and must follow it.", name)
	}
	rest := source[start+1:]

	// The next top-level `variable "` ends this block. The last variable in the
	// file has no successor, which is the -1 case.
	if end := strings.Index(rest[1:], decl); end >= 0 {
		return rest[:end+1]
	}
	return rest
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
