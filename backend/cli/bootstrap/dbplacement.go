// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/fatih/color"
	"github.com/hashicorp/terraform-exec/tfexec"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/validation"
)

// Database placement: `dcctl install --database-node-selector` / `--database-toleration`.
//
// 🔑 THE INSTALL'S SETTING, AND ONLY THE INSTALL'S. It places the shared relational
// store (cluster root) and the event store of every instance bootstrapped on the
// cluster (instance root). A bootstrap has no placement flags: it follows the install
// record, like --ha and --backup-snapshot-class, so the two stores cannot be placed
// differently with neither choice recorded.
//
// What is NOT placed: NATS, the services, the backup object store, the operators and
// the monitoring stack. NATS in particular stays off the database nodes. It carries no
// toleration, so a tainted database pool never receives a server, and under --ha its
// three servers run one per node: on database nodes sized for memory, one would share a
// node with the event-store primary, which is already the busiest pod on the cluster.

const (
	selectorFlag   = "--database-node-selector"
	tolerationFlag = "--database-toleration"
	// reservedTaintPrefix is the namespace of the taints Kubernetes itself puts on a
	// failing node. Tolerating one would override the node-loss fuse the cnpg-cluster
	// chart sets, so the chart, the module and this parser all refuse it.
	reservedTaintPrefix = "node.kubernetes.io/"
)

// DatabasePlacement is where the database instances may run, as settled by
// ParseDatabasePlacement.
//
// 🔑 CANONICAL STRINGS, NOT A MAP AND A SLICE, so the type is comparable: InstallSettings
// is compared with != to decide whether a re-install changes the cluster
// (refuseAReinstallThatWouldHurt), and a map field there would not compile. Only
// ParseDatabasePlacement produces a non-zero value, and validate re-parses a recorded
// one, so the accessors below never meet a string they cannot split.
type DatabasePlacement struct {
	// NodeSelector is "key=value" pairs, sorted by key, joined by ",".
	NodeSelector string `json:"nodeSelector,omitempty"`
	// Tolerations is "key[=value][:Effect]" entries, sorted and de-duplicated, joined
	// by ",". "key=value" is operator Equal, a bare "key" is Exists.
	Tolerations string `json:"tolerations,omitempty"`
}

// dbToleration is one toleration as both OpenTofu roots type it (database_tolerations)
// and as the cluster root reports it back.
type dbToleration struct {
	Key      string `json:"key"`
	Operator string `json:"operator"`
	Value    string `json:"value"`
	Effect   string `json:"effect"`
}

// ParseDatabasePlacement settles the two flags from argv, before any cluster is
// touched. It is also the reader of a recorded placement and of the cluster apply's
// outputs, so argv, the record and the apply have one parser between them.
func ParseDatabasePlacement(selectors, tolerations []string) (DatabasePlacement, error) {
	labels := map[string]string{}
	for _, s := range selectors {
		key, value, ok := strings.Cut(s, "=")
		if !ok {
			return DatabasePlacement{}, fmt.Errorf("%s %q is not key=value: name the label your database "+
				"nodes carry, as `kubectl get nodes --show-labels` prints it", selectorFlag, s)
		}
		if errs := validation.IsQualifiedName(key); len(errs) > 0 {
			return DatabasePlacement{}, fmt.Errorf("%s %q: %q is not a label key: %s", selectorFlag, s, key,
				strings.Join(errs, "; "))
		}
		// An empty value is allowed: role labels such as node-role.kubernetes.io/db= have one.
		if errs := validation.IsValidLabelValue(value); len(errs) > 0 {
			return DatabasePlacement{}, fmt.Errorf("%s %q: %q is not a label value: %s", selectorFlag, s, value,
				strings.Join(errs, "; "))
		}
		if prev, seen := labels[key]; seen && prev != value {
			return DatabasePlacement{}, fmt.Errorf("%s names label %s twice, as %q and %q: a node carries one "+
				"value per label, so no node could match both", selectorFlag, key, prev, value)
		}
		labels[key] = value
	}

	seen := map[string]bool{}
	var tols []string
	for _, s := range tolerations {
		t, err := parseToleration(s)
		if err != nil {
			return DatabasePlacement{}, err
		}
		if c := t.canonical(); !seen[c] {
			seen[c] = true
			tols = append(tols, c)
		}
	}
	if len(tols) > 0 && len(labels) == 0 {
		return DatabasePlacement{}, fmt.Errorf("%s lets the databases onto a tainted node, it does not put "+
			"them there; add %s with the label those nodes carry", tolerationFlag, selectorFlag)
	}

	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	pairs := make([]string, 0, len(keys))
	for _, k := range keys {
		pairs = append(pairs, k+"="+labels[k])
	}
	sort.Strings(tols)
	return DatabasePlacement{NodeSelector: strings.Join(pairs, ","), Tolerations: strings.Join(tols, ",")}, nil
}

// parseToleration reads one --database-toleration entry, written as `kubectl taint`
// writes a taint: key=value:Effect, key:Effect, key=value or key. It is split on the
// LAST ':' and then the first '=': neither a key nor a value can contain either.
func parseToleration(s string) (dbToleration, error) {
	rest, effect := s, ""
	if i := strings.LastIndex(s, ":"); i >= 0 {
		rest, effect = s[:i], s[i+1:]
		switch corev1.TaintEffect(effect) {
		case corev1.TaintEffectNoSchedule, corev1.TaintEffectPreferNoSchedule, corev1.TaintEffectNoExecute:
		default:
			return dbToleration{}, fmt.Errorf("%s %q: the effect %q is not NoSchedule, PreferNoSchedule or "+
				"NoExecute (leave it off, with its ':', to tolerate the taint whatever its effect)",
				tolerationFlag, s, effect)
		}
	}
	key, value, hasValue := strings.Cut(rest, "=")
	if errs := validation.IsQualifiedName(key); len(errs) > 0 {
		return dbToleration{}, fmt.Errorf("%s %q: %q is not a taint key: %s", tolerationFlag, s, key,
			strings.Join(errs, "; "))
	}
	if strings.HasPrefix(key, reservedTaintPrefix) {
		return dbToleration{}, fmt.Errorf("%s %q names a taint Kubernetes itself puts on failing nodes. "+
			"Tolerating it would keep a database on a dead node instead of failing it over; tolerate the "+
			"taint your database nodes carry", tolerationFlag, s)
	}
	if !hasValue {
		return dbToleration{Key: key, Operator: string(corev1.TolerationOpExists), Effect: effect}, nil
	}
	if value == "" {
		return dbToleration{}, fmt.Errorf("%s %q has an empty value, which tolerates only a taint whose "+
			"value is empty; use %s:Effect to tolerate the taint whatever its value", tolerationFlag, s, key)
	}
	if errs := validation.IsValidLabelValue(value); len(errs) > 0 {
		return dbToleration{}, fmt.Errorf("%s %q: %q is not a taint value: %s", tolerationFlag, s, value,
			strings.Join(errs, "; "))
	}
	return dbToleration{Key: key, Operator: string(corev1.TolerationOpEqual), Value: value, Effect: effect}, nil
}

// canonical is the entry as the flag spells it, and as the record stores it.
func (t dbToleration) canonical() string {
	s := t.Key
	if t.Operator == string(corev1.TolerationOpEqual) {
		s += "=" + t.Value
	}
	if t.Effect != "" {
		s += ":" + t.Effect
	}
	return s
}

// IsZero reports a placement that places nothing.
func (p DatabasePlacement) IsZero() bool { return p == DatabasePlacement{} }

// nodeSelector is the labels as a map. Never nil, so it encodes as {} rather than null.
func (p DatabasePlacement) nodeSelector() map[string]string {
	m := map[string]string{}
	for _, pair := range splitPlacementList(p.NodeSelector) {
		k, v, _ := strings.Cut(pair, "=")
		m[k] = v
	}
	return m
}

// tolerations is the entries as the roots type them. Never nil, so it encodes as [].
func (p DatabasePlacement) tolerations() []dbToleration {
	out := []dbToleration{}
	for _, s := range splitPlacementList(p.Tolerations) {
		// Canonical by construction (see the type), so this cannot fail.
		t, _ := parseToleration(s)
		out = append(out, t)
	}
	return out
}

// describe renders the placement for the operator.
func (p DatabasePlacement) describe() string {
	if p.IsZero() {
		return "none"
	}
	s := "nodes labelled " + p.NodeSelector
	if p.Tolerations != "" {
		s += ", tolerating " + p.Tolerations
	}
	return s
}

// infraVars renders the placement for OpenTofu: JSON, which is an HCL expression, so
// `-var` reads it as the map and the list it means.
//
// 🔴 EMITTED ON EVERY RUN, EMPTY INCLUDED, the way the HA topology is. Both roots
// declare the two variables, so a value stated here leaves nothing for a
// terraform.tfvars in an instance's infra directory to decide: an event store placed
// somewhere the cluster was never told about is not reachable through dcctl. Empty is
// the roots' own default, so stating it changes nothing on an unplaced cluster.
func (p DatabasePlacement) infraVars() []string {
	sel, _ := json.Marshal(p.nodeSelector())
	tols, _ := json.Marshal(p.tolerations())
	return []string{"database_node_selector=" + string(sel), "database_tolerations=" + string(tols)}
}

// splitPlacementList splits a canonical list; "" is no entries, not one empty one.
func splitPlacementList(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, ",")
}

// databasePlacement is where this run's databases go.
//
// 🔑 ONE READING, like backupSnapshotClass. infraVars emits it, the install record
// stores it, and the preflight checks it. A bootstrap follows the record: an event
// store placed differently from the relational store would be a placement nobody chose.
func databasePlacement(st *State) DatabasePlacement {
	if st.Install != nil {
		return st.Install.Settings.DatabasePlacement
	}
	return st.DatabasePlacement
}

// defaultDatabaseInstances is how many instances each database runs at the cluster's
// --ha setting, by default: the roots' own `(var.ha ? 3 : 1)`, which
// TestTheDatabaseInstanceDefaultIsTheRoots holds this to. A database runs one instance
// per node (its anti-affinity is required on the hostname), so this is also how many
// usable nodes a placement needs. A postgres_instances or timescale_instances pinned in
// a terraform.tfvars overrides it without dcctl knowing; the cnpg-cluster module
// counts against the real number at plan, as the backstop for that path.
func defaultDatabaseInstances(ha bool) int {
	if ha {
		return 3
	}
	return 1
}

// usableNodes sorts the nodes carrying every selected label into those a database
// instance could be scheduled on and, for the rest, why not. Kubernetes' toleration
// rule, restricted to the two operators the flag can write: same key; Exists, or Equal
// with the same value; no effect matches every effect. A PreferNoSchedule taint keeps
// no pod off, so it is not counted against a node.
//
// The rule is written out rather than taken from corev1.Toleration.ToleratesTaint,
// whose signature changed in k8s.io/api v0.36 (it gained a logger and a feature
// switch), and the cnpg-cluster module states the same rule in HCL for its own count.
func usableNodes(p DatabasePlacement, nodes []corev1.Node) (usable, refused []string) {
	selector, tols := p.nodeSelector(), p.tolerations()
	for _, n := range nodes {
		if !matchesLabels(n.Labels, selector) {
			continue
		}
		if n.Spec.Unschedulable {
			refused = append(refused, n.Name+" (cordoned)")
			continue
		}
		blocked := ""
		for _, taint := range n.Spec.Taints {
			if taint.Effect != corev1.TaintEffectNoSchedule && taint.Effect != corev1.TaintEffectNoExecute {
				continue
			}
			if !slices.ContainsFunc(tols, func(t dbToleration) bool { return tolerates(t, taint) }) {
				blocked = taintString(taint)
				break
			}
		}
		if blocked != "" {
			refused = append(refused, fmt.Sprintf("%s (taint %s is not tolerated)", n.Name, blocked))
			continue
		}
		usable = append(usable, n.Name)
	}
	return usable, refused
}

func matchesLabels(labels, selector map[string]string) bool {
	for k, v := range selector {
		if got, ok := labels[k]; !ok || got != v {
			return false
		}
	}
	return true
}

func tolerates(t dbToleration, taint corev1.Taint) bool {
	if t.Key != taint.Key {
		return false
	}
	if t.Effect != "" && t.Effect != string(taint.Effect) {
		return false
	}
	return t.Operator == string(corev1.TolerationOpExists) || t.Value == taint.Value
}

func taintString(t corev1.Taint) string {
	s := t.Key
	if t.Value != "" {
		s += "=" + t.Value
	}
	return s + ":" + string(t.Effect)
}

// databasePlacementShortfall is the counting rule, split from the cluster access so it
// can be asserted on without a cluster.
func databasePlacementShortfall(p DatabasePlacement, ha bool, nodes []corev1.Node) error {
	if p.IsZero() {
		return nil
	}
	want := defaultDatabaseInstances(ha)
	usable, refused := usableNodes(p, nodes)
	if len(usable) >= want {
		return nil
	}
	var found string
	switch {
	case len(refused) == 0 && len(usable) == 0:
		found = "No node carries those labels; `kubectl get nodes --show-labels` lists what they carry."
	default:
		found = fmt.Sprintf("%d node(s) carry those labels", len(usable)+len(refused))
		if len(usable) > 0 {
			found += ", usable: " + strings.Join(usable, ", ")
		}
		if len(refused) > 0 {
			found += "; kept out: " + strings.Join(refused, ", ")
		}
		found += "."
	}
	topology := "one instance"
	if ha {
		topology = fmt.Sprintf("--ha runs %d instances of each, one per node, so it needs %d", want, want)
	}
	fix := "Tolerate the taint with " + tolerationFlag + " (as key=value:Effect), label the nodes you " +
		"mean, wait for a cordoned node to come back, or drop " + selectorFlag
	return fmt.Errorf("%s %s: the databases cannot be placed there. Each database needs a usable node "+
		"for every instance (%s) and %d of the selected nodes can take one. %s %s",
		selectorFlag, p.NodeSelector, topology, len(usable), found, fix)
}

// checkDatabasePlacement refuses a placement the cluster's nodes cannot hold, before
// anything is written. Same shape as checkHaNodeCapacity: a dry run that cannot READ
// the nodes says so and goes on; what the nodes SAY is fatal on both paths.
func checkDatabasePlacement(ctx context.Context, st *State) error {
	p := databasePlacement(st)
	if p.IsZero() {
		return nil
	}
	nodes, err := listNodes(ctx, st.KubeContext)
	if err != nil {
		if st.DryRun {
			fmt.Println(color.YellowString(
				"  could not check whether this cluster has nodes for the database placement (%v); the plan "+
					"below assumes it does.", err))
			return nil
		}
		return fmt.Errorf("checking the cluster's nodes can take the database placement: %w", err)
	}
	// OUTSIDE the softening above, for the reason checkHaNodeCapacity gives.
	return databasePlacementShortfall(p, st.HA, nodes)
}

// placementFromOutputs reads the placement the cluster apply handed the relational
// store's chart back out of the cluster root's outputs, through the same parser argv
// goes through.
//
// 🔴 AN ABSENT OUTPUT IS AN ERROR, A NULL ONE IS NO PLACEMENT. Reading "the root no
// longer exports this" as "unplaced" would record an install whose relational store
// sits on the chosen nodes as one whose instances' event stores go anywhere.
func placementFromOutputs(outputs map[string]tfexec.OutputMeta) (DatabasePlacement, error) {
	selMeta, ok := outputs["database_node_selector"]
	if !ok {
		return DatabasePlacement{}, fmt.Errorf("the cluster prerequisite root did not export %q", "database_node_selector")
	}
	tolMeta, ok := outputs["database_tolerations"]
	if !ok {
		return DatabasePlacement{}, fmt.Errorf("the cluster prerequisite root did not export %q", "database_tolerations")
	}
	var sel map[string]string
	if err := json.Unmarshal(selMeta.Value, &sel); err != nil {
		return DatabasePlacement{}, fmt.Errorf("decoding database_node_selector output: %w", err)
	}
	var tols []dbToleration
	if err := json.Unmarshal(tolMeta.Value, &tols); err != nil {
		return DatabasePlacement{}, fmt.Errorf("decoding database_tolerations output: %w", err)
	}
	selectors := make([]string, 0, len(sel))
	for k, v := range sel {
		selectors = append(selectors, k+"="+v)
	}
	entries := make([]string, 0, len(tols))
	for _, t := range tols {
		if t.Operator == string(corev1.TolerationOpEqual) && t.Value == "" {
			// canonical() would write "key=", which the parser refuses for its own reason;
			// say what the apply actually reported instead.
			return DatabasePlacement{}, fmt.Errorf("the cluster apply reports a database toleration %q with "+
				"operator Equal and no value, which dcctl never asks for", t.Key)
		}
		entries = append(entries, t.canonical())
	}
	p, err := ParseDatabasePlacement(selectors, entries)
	if err != nil {
		return DatabasePlacement{}, fmt.Errorf("the cluster apply reports a database placement dcctl cannot "+
			"read: %w", err)
	}
	return p, nil
}
