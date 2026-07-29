// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import "testing"

// The metrics block dcctl hands the chart decides which alerting rules render at
// all, so a value that silently fails to cross this seam does not produce a
// broken alert — it produces NO alert, on an instance whose dashboards all look
// healthy. These pin the two directions that matter for cnpgNamespace, which
// gates both the CloudNativePG operator's PodMonitor and the control-plane
// alerting rules (ADR-020 A1.5).

func metricsBlock(t *testing.T, st *State) map[string]interface{} {
	t.Helper()
	vals := helmValues(st)
	m, ok := vals["metrics"].(map[string]interface{})
	if !ok {
		t.Fatalf("helmValues produced no metrics block: %#v", vals["metrics"])
	}
	return m
}

func TestHelmValuesPassesTheCNPGNamespaceThrough(t *testing.T) {
	st := &State{Instance: "prod", Values: map[string]string{
		cnpgNamespaceKey: "cnpg-system",
	}}

	if got := metricsBlock(t, st)["cnpgNamespace"]; got != "cnpg-system" {
		t.Errorf("cnpgNamespace = %#v, want %q", got, "cnpg-system")
	}
}

func TestHelmValuesLeavesTheCNPGNamespaceEmptyWhenTheInfraDidNotReportOne(t *testing.T) {
	// The state an install with no CloudNativePG operator leaves behind: the
	// `cnpg_namespace` OpenTofu output is null, so applyInfra clears the key and
	// nothing sets it again.
	//
	// 🔑 The assertion is that it stays EMPTY, not that it falls back to
	// "cnpg-system". A fallback here would render a PodMonitor selecting an
	// operator that is not there, plus a control-plane alert scoped to a
	// namespace with no deployments in it — which selects an empty vector and so
	// can never fire, while `kubectl get prometheusrule` shows it present and
	// healthy. databaseNamespace deliberately DOES fall back, because there an
	// empty namespace label matches no series and silently disables alerts that
	// should be on. The two values want OPPOSITE defaults, which is exactly the
	// kind of asymmetry that gets "tidied" into consistency by someone reading
	// only one of them.
	st := &State{Instance: "prod", Values: map[string]string{}}

	if got := metricsBlock(t, st)["cnpgNamespace"]; got != "" {
		t.Errorf("cnpgNamespace = %#v, want empty", got)
	}
}

func TestHelmValuesStillFallsBackForTheDatabaseNamespace(t *testing.T) {
	// The counterweight to the test above. If someone "fixes" the asymmetry by
	// making both keys behave the same way, one of these two fails whichever
	// direction they choose.
	st := &State{Instance: "prod", Values: map[string]string{}}

	if got := metricsBlock(t, st)["databaseNamespace"]; got != infraNamespace {
		t.Errorf("databaseNamespace = %#v, want the %q fallback", got, infraNamespace)
	}
}
