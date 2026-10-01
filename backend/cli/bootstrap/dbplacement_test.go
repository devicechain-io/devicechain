// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"slices"
	"strings"
	"testing"
	"time"

	assets "github.com/devicechain-io/dc-deploy"
	"github.com/hashicorp/terraform-exec/tfexec"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// Database placement (`dcctl install --database-node-selector` / `--database-toleration`),
// end to end through dcctl: argv, the variables each OpenTofu root is handed, the
// install record every bootstrap follows, and the node check that refuses a placement
// the cluster cannot hold before anything is written.

const (
	poolLabel   = "devicechain.io/pool=database"
	poolTaint   = "dedicated=database:NoSchedule"
	poolTaintEx = "dedicated:NoSchedule"
)

func mustPlacement(t *testing.T, selectors, tolerations []string) DatabasePlacement {
	t.Helper()
	p, err := ParseDatabasePlacement(selectors, tolerations)
	if err != nil {
		t.Fatalf("ParseDatabasePlacement(%q, %q): %v", selectors, tolerations, err)
	}
	return p
}

// The pool placement the docs and the GKE configuration use.
func poolPlacement(t *testing.T) DatabasePlacement {
	return mustPlacement(t, []string{poolLabel}, []string{poolTaint})
}

func TestDatabasePlacementIsCheckedFromArgv(t *testing.T) {
	for _, tc := range []struct {
		name                   string
		selectors, tolerations []string
		wantSelector, wantTols string
	}{
		{"nothing", nil, nil, "", ""},
		{"a label", []string{"a=b"}, nil, "a=b", ""},
		{"a role label with an empty value", []string{"node-role.kubernetes.io/db="}, nil, "node-role.kubernetes.io/db=", ""},
		{"the GKE pool label and the GKE pool taint, as its outputs print them",
			[]string{"cloud.google.com/gke-nodepool=database"}, []string{poolTaint},
			"cloud.google.com/gke-nodepool=database", poolTaint},
		{"Equal with an effect", []string{"a=b"}, []string{"k=v:NoSchedule"}, "a=b", "k=v:NoSchedule"},
		{"Exists with an effect", []string{"a=b"}, []string{"k:NoExecute"}, "a=b", "k:NoExecute"},
		{"Exists with no effect", []string{"a=b"}, []string{"k"}, "a=b", "k"},
		{"Equal with no effect", []string{"a=b"}, []string{"k=v"}, "a=b", "k=v"},
		{"sorted and de-duplicated", []string{"z=1", "a=2", "z=1"}, []string{"k:NoSchedule", "b=c", "k:NoSchedule"},
			"a=2,z=1", "b=c,k:NoSchedule"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := mustPlacement(t, tc.selectors, tc.tolerations)
			want := DatabasePlacement{NodeSelector: tc.wantSelector, Tolerations: tc.wantTols}
			if got != want {
				t.Errorf("got %+v, want %+v", got, want)
			}
			// The record is read back through the same parser, so the canonical form must
			// parse to itself.
			if again := mustPlacement(t, splitPlacementList(got.NodeSelector), splitPlacementList(got.Tolerations)); again != got {
				t.Errorf("the canonical form %+v re-parsed as %+v", got, again)
			}
		})
	}

	for _, tc := range []struct {
		name                   string
		selectors, tolerations []string
		flag, entry            string
	}{
		{"a selector with no =", []string{"noequals"}, nil, "--database-node-selector", "noequals"},
		{"a selector with no key", []string{"=v"}, nil, "--database-node-selector", "=v"},
		{"a selector with a bad value", []string{"a=-bad"}, nil, "--database-node-selector", "a=-bad"},
		{"one label with two values", []string{"k=v", "k=w"}, nil, "--database-node-selector", `"w"`},
		{"an Equal toleration with an empty value", []string{"a=b"}, []string{"k="}, "--database-toleration", "k="},
		{"an unknown effect", []string{"a=b"}, []string{"k=v:Bogus"}, "--database-toleration", "k=v:Bogus"},
		{"an empty effect after the colon", []string{"a=b"}, []string{"k=v:"}, "--database-toleration", "k=v:"},
		{"a toleration value that is not a label value", []string{"a=b"}, []string{"k=bad value:NoSchedule"},
			"--database-toleration", "k=bad value:NoSchedule"},
		{"a reserved taint", []string{"a=b"}, []string{"node.kubernetes.io/not-ready:NoExecute"},
			"--database-toleration", "node.kubernetes.io/not-ready:NoExecute"},
		{"a toleration with no key", []string{"a=b"}, []string{":NoSchedule"}, "--database-toleration", ":NoSchedule"},
		{"a toleration with nothing to place", nil, []string{poolTaint}, "--database-toleration", "--database-node-selector"},
	} {
		t.Run("refuses "+tc.name, func(t *testing.T) {
			p, err := ParseDatabasePlacement(tc.selectors, tc.tolerations)
			if err == nil {
				t.Fatalf("accepted as %+v", p)
			}
			if !strings.Contains(err.Error(), tc.flag) || !strings.Contains(err.Error(), tc.entry) {
				t.Errorf("the refusal does not name the flag %q and the entry %q: %v", tc.flag, tc.entry, err)
			}
		})
	}
}

// Both roots declare the two variables, so the placement reaches the relational store
// and the event store. It is stated on every run, empty included, so nothing else (a
// terraform.tfvars in an infra directory) is left to decide it.
func TestThePlacementReachesBothRootsOnEveryRun(t *testing.T) {
	route := func(t *testing.T, st *State) (cluster, instance []string) {
		t.Helper()
		c, i, err := splitVars(infraVars(st))
		if err != nil {
			t.Fatal(err)
		}
		return c, i
	}
	for _, tc := range []struct {
		name      string
		placement DatabasePlacement
		want      []string
	}{
		{"placed", poolPlacement(t), []string{
			`database_node_selector={"devicechain.io/pool":"database"}`,
			`database_tolerations=[{"key":"dedicated","operator":"Equal","value":"database","effect":"NoSchedule"}]`,
		}},
		{"a selector alone, two labels, an Exists toleration",
			mustPlacement(t, []string{"b=2", "a=1"}, []string{poolTaintEx}), []string{
				`database_node_selector={"a":"1","b":"2"}`,
				`database_tolerations=[{"key":"dedicated","operator":"Exists","value":"","effect":"NoSchedule"}]`,
			}},
		{"not placed", DatabasePlacement{}, []string{`database_node_selector={}`, `database_tolerations=[]`}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := aWritableState()
			st.DatabasePlacement = tc.placement
			cluster, instance := route(t, st)
			for name, vars := range map[string][]string{"cluster": cluster, "instance": instance} {
				var got []string
				for _, v := range vars {
					if strings.HasPrefix(v, "database_node_selector=") || strings.HasPrefix(v, "database_tolerations=") {
						got = append(got, v)
					}
				}
				if !slices.Equal(got, tc.want) {
					t.Errorf("the %s root was handed %q, want %q", name, got, tc.want)
				}
			}
		})
	}
}

// A bootstrap places its event store where the install record says, never by a value
// of its own.
func TestABootstrapPlacesItsEventStoreWhereTheInstallSaid(t *testing.T) {
	placed := `database_node_selector={"devicechain.io/pool":"database"}`
	instanceVars := func(t *testing.T, st *State) []string {
		t.Helper()
		_, i, err := splitVars(infraVars(st))
		if err != nil {
			t.Fatal(err)
		}
		return i
	}

	rec := aCompleteInstall()
	rec.Settings.DatabasePlacement = poolPlacement(t)
	st := aWritableState()
	st.Install = &rec
	if got := instanceVars(t, st); !slices.Contains(got, placed) {
		t.Errorf("a bootstrap on a placed cluster handed its event store %v, without %s", got, placed)
	}

	rec = aCompleteInstall()
	st = aWritableState()
	st.Install = &rec
	st.DatabasePlacement = poolPlacement(t)
	if got := instanceVars(t, st); !slices.Contains(got, `database_node_selector={}`) {
		t.Errorf("a bootstrap on a cluster installed without placement placed its event store anyway: %v", got)
	}
}

// 🔴 A SCHEMA-4 RECORD IS REFUSED. It cannot say where the databases were placed, so a
// bootstrap from it would put its event store wherever the scheduler chose -- off the
// nodes the relational store is confined to.
func TestAnInstallRecordFromBeforePlacementIsRefused(t *testing.T) {
	c := fake.NewSimpleClientset()
	if err := writeInstalled(context.Background(), c, aCompleteInstall(), installClock); err != nil {
		t.Fatal(err)
	}
	cm, _ := c.CoreV1().ConfigMaps("dc-system").Get(context.Background(), "dc-install", metav1.GetOptions{})
	var rec InstallRecord
	if err := json.Unmarshal([]byte(cm.Data["install.json"]), &rec); err != nil {
		t.Fatal(err)
	}
	if rec.Schema != 5 {
		t.Fatalf("an install record is written at schema %d, want 5", rec.Schema)
	}
	rec.Schema = 4
	body, _ := json.Marshal(rec)
	cm.Data["install.json"] = string(body)
	if _, err := c.CoreV1().ConfigMaps("dc-system").Update(context.Background(), cm, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	_, err := readInstallRecord(context.Background(), c, testClusterUID)
	if !errors.Is(err, ErrInstallRecordSchema) {
		t.Fatalf("a schema-4 install record read as %v, want ErrInstallRecordSchema", err)
	}
	if !strings.Contains(err.Error(), "Re-run `dcctl install`") {
		t.Errorf("the refusal does not say how to bring the record up to date: %v", err)
	}
}

// The option reaches the State an install applies from, the record's settings, and
// survives the trip through the cluster.
func TestTheInstallRecordCarriesThePlacement(t *testing.T) {
	want := poolPlacement(t)
	st := installState(ClusterBinding{KubeContext: "kind-devicechain"}, "local",
		InstallOptions{DatabasePlacement: want})
	if got := installSettingsFor(st).DatabasePlacement; got != want {
		t.Errorf("an install asked for %+v records %+v", want, got)
	}

	rec := aCompleteInstall()
	rec.Settings.DatabasePlacement, rec.Outputs.DatabasePlacement = want, want
	c := fake.NewSimpleClientset()
	if err := writeInstalled(context.Background(), c, rec, installClock); err != nil {
		t.Fatal(err)
	}
	got, err := readInstallRecord(context.Background(), c, testClusterUID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Settings.DatabasePlacement != want || got.Outputs.DatabasePlacement != want {
		t.Errorf("the record read back with settings %+v and outputs %+v, want %+v",
			got.Settings.DatabasePlacement, got.Outputs.DatabasePlacement, want)
	}
}

// What the settings promise, the apply must have delivered, and a placement dcctl
// could not have written is not one to place an event store by.
func TestARecordWhoseApplyDidNotDeliverThePlacementIsRefused(t *testing.T) {
	placed := poolPlacement(t)
	for _, tc := range []struct {
		name              string
		settings, outputs DatabasePlacement
		refused           string
	}{
		{"asked for, not delivered", placed, DatabasePlacement{}, "the cluster apply reports none"},
		{"delivered, not asked for", DatabasePlacement{}, placed, "asks for the databases on none"},
		{"a different label delivered", placed, mustPlacement(t, []string{"other=x"}, []string{poolTaint}),
			"the cluster apply reports nodes labelled other=x"},
		{"not canonical", DatabasePlacement{NodeSelector: "z=1,a=2"}, DatabasePlacement{NodeSelector: "z=1,a=2"},
			"cannot be read"},
		{"unreadable", DatabasePlacement{NodeSelector: "noequals"}, DatabasePlacement{NodeSelector: "noequals"},
			"cannot be read"},
		{"asked for and delivered", placed, placed, ""},
		{"neither", DatabasePlacement{}, DatabasePlacement{}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := aCompleteInstall()
			rec.Settings.DatabasePlacement, rec.Outputs.DatabasePlacement = tc.settings, tc.outputs
			err := writeInstalled(context.Background(), fake.NewSimpleClientset(), rec, installClock)
			switch {
			case tc.refused == "" && err != nil:
				t.Errorf("refused: %v", err)
			case tc.refused != "" && err == nil:
				t.Errorf("written as installed; want a refusal mentioning %q", tc.refused)
			case tc.refused != "" && !strings.Contains(err.Error(), tc.refused):
				t.Errorf("refused, but not for the placement: %v (want %q)", err, tc.refused)
			}
		})
	}
}

// The placement the cluster apply handed the relational store is read back into the
// record's outputs through the argv parser; an absent output is an error, not "none".
func TestTheClusterApplysPlacementIsRecorded(t *testing.T) {
	base := func() map[string]tfexec.OutputMeta {
		return map[string]tfexec.OutputMeta{
			"backup_endpoint_url":          {Value: []byte(`"http://e"`)},
			"backup_credentials_secret":    {Value: []byte(`"s"`)},
			"backup_access_key_id_key":     {Value: []byte(`"a"`)},
			"backup_secret_access_key_key": {Value: []byte(`"k"`)},
			"backup_bucket_tsdb":           {Value: []byte(`"b"`)},
			"namespace":                    {Value: []byte(`"dc-system"`)},
			"postgres_cluster_name":        {Value: []byte(`"dc-rdb"`)},
			"postgres_max_connections":     {Value: []byte(`600`)},
			"database_node_selector":       {Value: []byte(`{"devicechain.io/pool":"database"}`)},
			"database_tolerations": {Value: []byte(
				`[{"key":"dedicated","operator":"Equal","value":"database","effect":"NoSchedule"},` +
					`{"key":"spot","operator":"Exists","value":"","effect":""}]`)},
		}
	}
	got, err := clusterOutputs(base())
	if err != nil {
		t.Fatal(err)
	}
	want := mustPlacement(t, []string{poolLabel}, []string{poolTaint, "spot"})
	if got.DatabasePlacement != want {
		t.Errorf("the apply reported %+v, the record holds %+v", want, got.DatabasePlacement)
	}

	for name, value := range map[string]string{"empty": "", "null": "null"} {
		outputs := base()
		sel, tols := `{}`, `[]`
		if value == "null" {
			sel, tols = "null", "null"
		}
		outputs["database_node_selector"] = tfexec.OutputMeta{Value: []byte(sel)}
		outputs["database_tolerations"] = tfexec.OutputMeta{Value: []byte(tols)}
		if got, err := clusterOutputs(outputs); err != nil || !got.DatabasePlacement.IsZero() {
			t.Errorf("%s outputs read as %+v (%v), want no placement", name, got.DatabasePlacement, err)
		}
	}

	for _, missing := range []string{"database_node_selector", "database_tolerations"} {
		outputs := base()
		delete(outputs, missing)
		if _, err := clusterOutputs(outputs); err == nil || !strings.Contains(err.Error(), missing) {
			t.Errorf("an apply that did not export %s decoded: %v", missing, err)
		}
	}

	outputs := base()
	outputs["database_tolerations"] = tfexec.OutputMeta{Value: []byte(`"dedicated"`)}
	if _, err := clusterOutputs(outputs); err == nil {
		t.Error("an unreadable database_tolerations output decoded")
	}
	outputs = base()
	outputs["database_tolerations"] = tfexec.OutputMeta{Value: []byte(`[{"key":"dedicated","operator":"Equal","value":"","effect":""}]`)}
	if _, err := clusterOutputs(outputs); err == nil || !strings.Contains(err.Error(), "operator Equal and no value") {
		t.Errorf("an Equal toleration with no value decoded: %v", err)
	}
}

// Changing the placement under running instances is refused like every other setting,
// and that includes a flagless re-run of a placed cluster.
func TestAReinstallThatChangesPlacementUnderInstancesIsRefused(t *testing.T) {
	withClusterInstances(t, []string{"prod"}, nil)
	st, settings := reinstallState()
	settings.DatabasePlacement = poolPlacement(t)
	err := refuseAReinstallThatWouldHurt(context.Background(), st, installed(), settings, stateHere())
	if err == nil {
		t.Fatal("placing the databases under a running instance was not refused")
	}
	if !strings.Contains(err.Error(), "databases on nodes labelled "+poolLabel) {
		t.Errorf("the refusal does not name the change: %v", err)
	}

	last := installed()
	last.Settings.DatabasePlacement = poolPlacement(t)
	_, flagless := reinstallState()
	if err := refuseAReinstallThatWouldHurt(context.Background(), st, last, flagless, stateHere()); err == nil {
		t.Error("re-running install without the flags on a placed cluster under a running instance was not refused")
	}

	withClusterInstances(t, nil, nil)
	if err := refuseAReinstallThatWouldHurt(context.Background(), st, installed(), settings, stateHere()); err != nil {
		t.Errorf("with no instances running the change was refused: %v", err)
	}
}

func node(name string, labels map[string]string, cordoned bool, taints ...corev1.Taint) corev1.Node {
	return corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels},
		Spec:       corev1.NodeSpec{Unschedulable: cordoned, Taints: taints},
	}
}

var (
	poolLabels    = map[string]string{"devicechain.io/pool": "database", "kubernetes.io/os": "linux"}
	dedicated     = corev1.Taint{Key: "dedicated", Value: "database", Effect: corev1.TaintEffectNoSchedule}
	dedicatedPref = corev1.Taint{Key: "dedicated", Value: "database", Effect: corev1.TaintEffectPreferNoSchedule}
)

func dbNodes(n int, taints ...corev1.Taint) []corev1.Node {
	var out []corev1.Node
	for i := range n {
		out = append(out, node(fmt.Sprintf("db-%d", i), poolLabels, false, taints...))
	}
	return out
}

// The counting rule: usable means carrying every label, not cordoned, and every
// NoSchedule/NoExecute taint tolerated -- one per instance at the cluster's --ha
// setting.
func TestDatabasePlacementGuard(t *testing.T) {
	selectorOnly := mustPlacement(t, []string{poolLabel}, nil)
	exists := mustPlacement(t, []string{poolLabel}, []string{poolTaintEx})
	wrongValue := mustPlacement(t, []string{poolLabel}, []string{"dedicated=other:NoSchedule"})
	wrongEffect := mustPlacement(t, []string{poolLabel}, []string{"dedicated=database:NoExecute"})
	anyEffect := mustPlacement(t, []string{poolLabel}, []string{"dedicated=database"})
	twoLabels := mustPlacement(t, []string{poolLabel, "disk=ssd"}, nil)
	services := node("svc-0", map[string]string{"devicechain.io/pool": "services"}, false)

	for _, tc := range []struct {
		name      string
		placement DatabasePlacement
		ha        bool
		nodes     []corev1.Node
		refused   []string // every fragment must appear; nil means accepted
	}{
		{"no placement places nothing and never refuses", DatabasePlacement{}, true, nil, nil},
		{"three matching untainted, --ha", selectorOnly, true, append(dbNodes(3), services), nil},
		{"one matching, no --ha", selectorOnly, false, dbNodes(1), nil},
		{"none matching", selectorOnly, false, []corev1.Node{services},
			[]string{"No node carries those labels", poolLabel}},
		{"every label must match", twoLabels, false, dbNodes(3), []string{"No node carries those labels"}},
		{"🔴 two usable under --ha", selectorOnly, true, dbNodes(2),
			[]string{"--ha runs 3 instances", "2 of the selected nodes can take one", "db-0", "db-1"}},
		{"tainted, untolerated", selectorOnly, true, dbNodes(3, dedicated),
			[]string{"0 of the selected nodes", "db-0 (taint dedicated=database:NoSchedule is not tolerated)"}},
		{"tainted, tolerated", poolPlacement(t), true, dbNodes(3, dedicated), nil},
		{"tainted, tolerated by Exists", exists, true, dbNodes(3, dedicated), nil},
		{"tainted, tolerated with no effect", anyEffect, true, dbNodes(3, dedicated), nil},
		{"tainted, wrong value", wrongValue, false, dbNodes(1, dedicated), []string{"is not tolerated"}},
		{"tainted, wrong effect", wrongEffect, false, dbNodes(1, dedicated), []string{"is not tolerated"}},
		{"PreferNoSchedule keeps no pod off", selectorOnly, true, dbNodes(3, dedicatedPref), nil},
		{"cordoned does not count", selectorOnly, true,
			append(dbNodes(2), node("db-c", poolLabels, true)), []string{"db-c (cordoned)", "2 of the selected"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := databasePlacementShortfall(tc.placement, tc.ha, tc.nodes)
			if tc.refused == nil {
				if err != nil {
					t.Errorf("refused: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("accepted; want a refusal naming %q", tc.refused)
			}
			for _, f := range tc.refused {
				if !strings.Contains(err.Error(), f) {
					t.Errorf("the refusal does not say %q: %v", f, err)
				}
			}
		})
	}
}

// The instance count dcctl checks against is the roots' own default. A root that moved
// its default without this would have dcctl pass a placement the plan then refuses
// after dcctl's first writes, or refuse one the plan accepts.
func TestTheDatabaseInstanceDefaultIsTheRoots(t *testing.T) {
	want := fmt.Sprintf("(var.ha ? %d : %d)", defaultDatabaseInstances(true), defaultDatabaseInstances(false))
	for _, tc := range []struct {
		root fs.FS
		line string
	}{
		{assets.OpenTofuCluster(), "instances = var.postgres_instances != 0 ? var.postgres_instances : " + want},
		{assets.OpenTofuInstance(), "instances = var.timescale_instances != 0 ? var.timescale_instances : " + want},
	} {
		src, err := fs.ReadFile(tc.root, "main.tf")
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(src), tc.line) {
			t.Errorf("the root no longer says %q", tc.line)
		}
	}
}

func stubListNodes(t *testing.T, nodes []corev1.Node, err error) *int {
	t.Helper()
	orig := listNodes
	t.Cleanup(func() { listNodes = orig })
	calls := 0
	listNodes = func(context.Context, string) ([]corev1.Node, error) {
		calls++
		return nodes, err
	}
	return &calls
}

// A failed READ is softened on a dry run only; what the nodes SAY is fatal on both.
func TestAPlacementReadIsSoftenedOnlyOnADryRun(t *testing.T) {
	placed := func(dry bool) *State {
		return &State{DryRun: dry, DatabasePlacement: poolPlacement(t), Values: map[string]string{}}
	}
	stubListNodes(t, nil, errors.New("listing the cluster's nodes: nope"))
	if err := checkDatabasePlacement(t.Context(), placed(true)); err != nil {
		t.Errorf("a dry run failed on nodes it could not read: %v", err)
	}
	if err := checkDatabasePlacement(t.Context(), placed(false)); err == nil ||
		!strings.Contains(err.Error(), "database placement") {
		t.Errorf("a real run went on without reading the nodes: %v", err)
	}

	stubListNodes(t, []corev1.Node{node("svc-0", nil, false)}, nil)
	for _, dry := range []bool{true, false} {
		if err := checkDatabasePlacement(t.Context(), placed(dry)); err == nil {
			t.Errorf("dry-run=%v: nodes that cannot take the databases passed", dry)
		}
	}

	calls := stubListNodes(t, nil, errors.New("must not be asked"))
	if err := checkDatabasePlacement(t.Context(), &State{Values: map[string]string{}}); err != nil || *calls != 0 {
		t.Errorf("an unplaced run read the nodes %d time(s) (%v)", *calls, err)
	}
}

// 🔴 THE INSTALL, NOT JUST THE CHECK. Install consults the guard before its dry-run
// branch and before any write, so a dry run is enough to reach it -- and the refusal
// is the placement's, not something later.
func TestInstallConsultsThePlacementGuard(t *testing.T) {
	deadKubeconfig(t)
	install := func(t *testing.T, ha bool, placement DatabasePlacement) error {
		t.Helper()
		// Bounded: the dry run's claim report asks the dead cluster, and client-go
		// retries an unreachable endpoint; that report is best-effort either way.
		ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
		defer cancel()
		var err error
		captureOutput(t, func() {
			err = Install(ctx, &fakeProvider{name: "local", ensureBind: ClusterBinding{KubeContext: "dead"}},
				InstallOptions{
					Options: Options{KubeContext: "dead", DryRun: true,
						ImageRegistry: "example.invalid/dc", ImageVersion: "v0.0.0-test"},
					HA:                ha,
					DatabasePlacement: placement,
				})
		})
		return err
	}

	// Three untainted services nodes, so the --ha NATS check (which counts only
	// untainted nodes) passes and the refusal can only be the placement's.
	services := []corev1.Node{node("s0", nil, false), node("s1", nil, false), node("s2", nil, false)}
	selectorOnly := mustPlacement(t, []string{poolLabel}, nil)

	stubListNodes(t, append(dbNodes(3, dedicated), services...), nil)
	if err := install(t, false, selectorOnly); err == nil || !strings.Contains(err.Error(), "is not tolerated") {
		t.Errorf("an install onto nodes whose taint it does not tolerate was not refused for it: %v", err)
	}
	stubListNodes(t, append(dbNodes(2, dedicated), services...), nil)
	if err := install(t, true, poolPlacement(t)); err == nil || !strings.Contains(err.Error(), "--ha runs 3 instances") {
		t.Errorf("an --ha install onto two usable nodes was not refused for it: %v", err)
	}
	stubListNodes(t, append(dbNodes(3, dedicated), services...), nil)
	if err := install(t, true, poolPlacement(t)); err != nil {
		t.Errorf("an install the nodes can hold was refused: %v", err)
	}
}

// 🔴 THE STEP, NOT JUST THE CHECK. A bootstrap on a placed cluster whose nodes can no
// longer take the event store is refused in the step that runs before the instance is
// declared; the store is not even asked.
func TestABootstrapRefusesUnusablePlacementBeforeDeclaringTheInstance(t *testing.T) {
	stubNamespacePrecheck(t)
	stubSingletons(t, clusterSingletons{}, nil)
	storeAsks := stubSharedStore(t, instanceStore{}, errors.New("the store must not be reached"))
	reads := stubListNodes(t, append(dbNodes(2, dedicated), node("db-x", poolLabels, true, dedicated)), nil)

	rec := installed()
	rec.Settings.HA = true
	rec.Settings.DatabasePlacement, rec.Outputs.DatabasePlacement = poolPlacement(t), poolPlacement(t)
	st := &State{Instance: "beta", IngressHost: "beta.localhost", Values: map[string]string{}}
	FollowInstall(st, rec)
	err := stepCheckClusterSingletons(context.Background(), st)
	if err == nil || !strings.Contains(err.Error(), "db-x (cordoned)") {
		t.Fatalf("a bootstrap whose database nodes cannot take its event store was not refused for it: %v", err)
	}
	if *reads != 1 || *storeAsks != 0 {
		t.Errorf("the nodes were read %d time(s) and the store asked %d time(s); want 1 and 0", *reads, *storeAsks)
	}

	// A cluster installed without placement does not look.
	reads = stubListNodes(t, nil, errors.New("must not be asked"))
	st = &State{Instance: "beta", IngressHost: "beta.localhost", Values: map[string]string{}}
	FollowInstall(st, installed())
	_ = stepCheckClusterSingletons(context.Background(), st)
	if *reads != 0 {
		t.Errorf("a cluster without placement had its nodes read %d time(s)", *reads)
	}
}
