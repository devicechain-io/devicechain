// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	gotypes "go/types"
	"strings"
	"testing"

	dcv1beta1 "github.com/devicechain-io/dc-k8s/api/v1beta1"
	"github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/natsauth"
	tfjson "github.com/hashicorp/terraform-json"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
)

// --- whether an upgrade applies the infrastructure at all ----------------------

// 🔴 THE DEFAULT IS THE FIX. An upgrade that plans and applies nothing for the broker and
// the event store unless asked is the defect this whole path exists to close, so the
// default — no flag — must read as "applies", and only --skip-infrastructure as "does
// not". Held by value here; that Upgrade asks this predicate, and asks it in the right
// place, is held by TestUpgradePlansAndAppliesTheInfrastructureOnlyUnderItsGate.
func TestAnUpgradeAppliesTheInfrastructureUnlessToldNotTo(t *testing.T) {
	if !appliesInfrastructure(&State{}) {
		t.Error("an upgrade with no flags does not apply the instance's broker and event store")
	}
	if appliesInfrastructure(&State{SkipInfrastructure: true}) {
		t.Error("an upgrade under --skip-infrastructure applies the instance's broker and event store anyway")
	}
}

// 🔴 THE FLAG HAS TO ARRIVE. The command's test follows --skip-infrastructure as far as
// UpgradeOptions; this follows it from there onto the State the upgrade acts on, through
// the real hydration, both ways — a hydration that dropped it would apply the
// infrastructure an operator asked it to leave, and one that set it would bring back
// the original defect.
func TestHydrationCarriesSkipInfrastructureOntoTheState(t *testing.T) {
	provider, err := Get("local")
	if err != nil {
		t.Fatal(err)
	}
	creds, err := natsauth.GenerateCredentials()
	if err != nil {
		t.Fatal(err)
	}
	deployed := &config.InstanceConfiguration{}
	deployed.Infrastructure.Nats.Tls.Enabled = true
	deployed.Infrastructure.Nats.Tls.Ca = "-----RUNNING CA-----"
	deployed.Infrastructure.Nats.Auth.CalloutIssuerSeed = creds.IssuerSeed
	deployed.Infrastructure.Nats.Auth.Password = creds.ServicePassword

	prevDecl, prevDeployed := readInstanceDeclaration, lookupDeployedInstance
	t.Cleanup(func() { readInstanceDeclaration, lookupDeployedInstance = prevDecl, prevDeployed })
	readInstanceDeclaration = func(context.Context, string, string) (*dcv1beta1.Instance, error) {
		return atVersion(t, "ghcr.io/devicechain-io", "v0.17.0"), nil
	}
	lookupDeployedInstance = func(context.Context, string, string) (*config.InstanceConfiguration, error) {
		return deployed, nil
	}
	stubOperatorCheck(t, nil)

	for _, skip := range []bool{true, false} {
		written := aWritableState()
		written.Instance = "prod"
		c := fake.NewSimpleClientset(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
			Name: "kube-system", UID: types.UID(testClusterUID)}})
		writeInstallThenBootstrapSecrets(t, c, written)
		settleStringDataLikeAnAPIServer(t, c)
		if err := writeInstalled(context.Background(), c, aCompleteInstall(), installClock); err != nil {
			t.Fatal(err)
		}
		st, err := hydrateUpgradeState(context.Background(), c, provider,
			ClusterBinding{KubeContext: "gke_p_z_c", Cluster: "devicechain"},
			UpgradeOptions{Options: Options{Instance: "prod"}, SkipInfrastructure: skip})
		if err != nil {
			t.Fatal(err)
		}
		if st.SkipInfrastructure != skip || appliesInfrastructure(st) == skip {
			t.Errorf("--skip-infrastructure=%v hydrated to SkipInfrastructure=%v (applies the infrastructure: %v)",
				skip, st.SkipInfrastructure, appliesInfrastructure(st))
		}
	}
}

// guardsOf returns the condition of every if statement whose BODY contains a call to
// name inside fn — the conditions that must all hold for the call to run. An if whose
// Init or Else holds the call does not guard it, and is not returned.
func guardsOf(fn *ast.FuncDecl, name string) (calls int, guards [][]string) {
	var stack []ast.Node
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		if n == nil {
			stack = stack[:len(stack)-1]
			return true
		}
		if call, ok := n.(*ast.CallExpr); ok {
			if id, ok := call.Fun.(*ast.Ident); ok && id.Name == name {
				calls++
				var conds []string
				for _, s := range stack {
					if ifs, ok := s.(*ast.IfStmt); ok && ifs.Body.Pos() <= call.Pos() && call.End() <= ifs.Body.End() {
						conds = append(conds, gotypes.ExprString(ifs.Cond))
					}
				}
				guards = append(guards, conds)
			}
		}
		stack = append(stack, n)
		return true
	})
	return calls, guards
}

func upgradeFuncDecl(t *testing.T) (*ast.FuncDecl, *token.FileSet) {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "upgrade.go", nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range file.Decls {
		if fd, ok := d.(*ast.FuncDecl); ok && fd.Name.Name == "Upgrade" && fd.Recv == nil {
			return fd, fset
		}
	}
	t.Fatal("upgrade.go declares no Upgrade")
	return nil, nil
}

// 🔴 ASSERTED ON THE SOURCE BECAUSE Upgrade NEEDS A CLUSTER, AND THE ORDER TEST ALONE
// CANNOT SEE THE CONDITION. TestTheUpgradePlansBeforeItWritesAndAppliesBeforeTheServices
// proves the calls exist and their order; inverting the condition above them — so that
// an ordinary upgrade plans and applies nothing — left it, and the whole package, green.
// This holds each call to exactly the guard it must sit under: the plan to
// appliesInfrastructure(st), whose value TestAnUpgradeAppliesTheInfrastructureUnlessToldNotTo
// holds, and the apply to the plan existing.
//
// The certificate renewal's wait is held the same way. Under --skip-infrastructure it is
// the only wait between a broker restart and the services rolling onto it.
func TestUpgradePlansAndAppliesTheInfrastructureOnlyUnderItsGate(t *testing.T) {
	upgrade, _ := upgradeFuncDecl(t)
	for _, c := range []struct {
		call, guard string
	}{
		{"planUpgradeInfra", "appliesInfrastructure(st)"},
		{"applyUpgradeInfra", "infra != nil"},
		{"waitForBrokerRollout", "restarted"},
	} {
		calls, guards := guardsOf(upgrade, c.call)
		if calls != 1 {
			t.Errorf("Upgrade calls %s %d times, want exactly once", c.call, calls)
			continue
		}
		if len(guards[0]) != 1 || guards[0][0] != c.guard {
			t.Errorf("Upgrade's call to %s runs under %q, want exactly `if %s`", c.call,
				strings.Join(guards[0], " && "), c.guard)
		}
	}
}

// --- an unfinished broker roll ---------------------------------------------------

// 🔴 THE DOCUMENTED REMEDY HAS TO BE REACHABLE. A broker server killed for memory, or left
// Pending for want of the CPU it requests, during an upgrade's roll is fixed by raising
// nats_memory_limit (or lowering a request) and running the upgrade again — and only the
// upgrade's apply delivers the new value. A precheck that refused every broker short of a
// server refused exactly that re-run, and nothing else applies a running instance's
// broker. A roll left unfinished is let through, and said; a broker short of a server on
// the template it already runs is still refused (TestSettleRefusesWhatCannotBeAppliedSafely).
func TestSettleLetsAnUnfinishedBrokerRollThrough(t *testing.T) {
	creds, err := natsauth.GenerateCredentials()
	if err != nil {
		t.Fatal(err)
	}
	st := &State{Instance: "prod", KubeContext: "gke_p_z_c", Values: map[string]string{
		"natsCA": "ca", "natsCalloutIssuerSeed": creds.IssuerSeed, "natsServicePassword": creds.ServicePassword}}
	stuck := healthyInfra("16Gi", "32Gi")
	stuck.Broker.Status.UpdateRevision = "dc-nats-new"
	stuck.Broker.Status.UpdatedReplicas = 1
	stuck.Broker.Status.ReadyReplicas = 2
	stuck.BrokerPods = []corev1.Pod{{ObjectMeta: metav1.ObjectMeta{Name: "dc-nats-2"},
		Status: corev1.PodStatus{Phase: corev1.PodRunning}}}
	stubUpgradeReads(t, clusterArchiveState{Exists: true}, stuck, natsauth.DeployedHashes{})

	if err := settleUpgradeInfraInputs(context.Background(), st); err != nil {
		t.Fatalf("an upgrade re-run over a broker whose roll did not finish was refused, so the "+
			"documented remedy (raise nats_memory_limit, run the upgrade again) cannot be applied: %v", err)
	}
	if len(st.InfraNotes) != 1 || !strings.Contains(st.InfraNotes[0], "last roll did not finish") ||
		!strings.Contains(st.InfraNotes[0], "dc-nats-2 Running") {
		t.Errorf("letting an unfinished roll through is not said, with the server that is stuck: %q", st.InfraNotes)
	}
}

// --- the event store's instance pods ---------------------------------------------

// 🔴 A JOB POD IS NOT AN INSTANCE. CloudNativePG runs initdb and join jobs under the same
// cluster label; counted, they make a healthy store read as having more pods than
// instances, and the precheck refuses — or the wait times out on — a store with nothing
// wrong with it. Through a real clientset, so the label selector is exercised too.
func TestEventStorePodImagesCountsOnlyLiveInstancePods(t *testing.T) {
	ns := InstanceNamespace("prod")
	pod := func(name, image string, labels map[string]string, mutate func(*corev1.Pod)) *corev1.Pod {
		l := map[string]string{"cnpg.io/cluster": TsdbClusterName}
		for k, v := range labels {
			l[k] = v
		}
		p := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, Labels: l},
			Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "postgres", Image: image}, {Name: "plugin", Image: "sidecar"}}},
			Status:     corev1.PodStatus{Phase: corev1.PodRunning},
		}
		if mutate != nil {
			mutate(p)
		}
		return p
	}
	now := metav1.Now()
	c := fake.NewSimpleClientset(
		pod("dc-tsdb-1", "tsdb:2", nil, nil),
		pod("dc-tsdb-2", "tsdb:2", nil, nil),
		pod("dc-tsdb-3-join-abc", "tsdb:1", map[string]string{"cnpg.io/jobRole": "join"}, nil),
		pod("dc-tsdb-1-initdb-xyz", "tsdb:1", map[string]string{"cnpg.io/jobRole": "initdb"}, nil),
		pod("dc-tsdb-4", "tsdb:1", nil, func(p *corev1.Pod) { p.DeletionTimestamp = &now; p.Finalizers = []string{"x"} }),
		pod("dc-tsdb-5", "tsdb:1", nil, func(p *corev1.Pod) { p.Status.Phase = corev1.PodSucceeded }),
		pod("dc-tsdb-6", "tsdb:1", nil, func(p *corev1.Pod) { p.Status.Phase = corev1.PodFailed }),
		// Another cluster's instance, which the selector must not reach.
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "other-1", Namespace: ns,
			Labels: map[string]string{"cnpg.io/cluster": "other"}},
			Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "postgres", Image: "tsdb:1"}}}},
	)
	got, err := eventStorePodImages(context.Background(), c, ns)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, ",") != "tsdb:2,tsdb:2" {
		t.Errorf("event store instance images = %q, want the two live instances' only", got)
	}
	if ok, why := eventStoreReady(anEventStore(2, "8Gi", "tsdb:2"), got); !ok {
		t.Errorf("a healthy two-instance store with job and finished pods beside it reads unhealthy: %s", why)
	}
}

// --- the spelling an upgrade passes for a volume ----------------------------------

// 🔴 THE BROKER'S CEILING IS DERIVED FROM THE SIZE STRING. A JetStream volume created at
// 12288Mi reads back from the API as 12Gi; passed as that, the broker's max_file_store
// goes from 11059Mi to 10Gi and every server rolls, over a volume that did not change.
// The state recorded the spelling the volume was created with, and that is what is
// passed — but only for the same quantity: a recorded size the live volume no longer
// has is not what an upgrade keeps.
func TestTheUpgradePassesAVolumeSizeAsTheStateRecordedIt(t *testing.T) {
	stateWith := func(js, store string) *tfjson.State {
		return &tfjson.State{Values: &tfjson.StateValues{RootModule: &tfjson.StateModule{Resources: []*tfjson.StateResource{
			{Address: natsReleaseAddress, AttributeValues: valuesAttr(t, map[string]interface{}{"config": map[string]interface{}{
				"jetstream": map[string]interface{}{"fileStore": map[string]interface{}{"pvc": map[string]interface{}{"size": js}}}}})},
			{Address: tsdbReleaseAddress, AttributeValues: valuesAttr(t, map[string]interface{}{
				"storage": map[string]interface{}{"size": store}})},
		}}}}
	}
	rec := aCompleteInstall()
	for name, c := range map[string]struct {
		state       *tfjson.State
		wantJS      string
		wantStoreIn string
	}{
		"same quantity, other spelling": {stateWith("12288Mi", "8192Mi"), "nats_jetstream_storage=12288Mi", "timescale_storage=8192Mi"},
		"recorded size no longer live":  {stateWith("10Gi", "4Gi"), "nats_jetstream_storage=12Gi", "timescale_storage=8Gi"},
		"nothing recorded":              {&tfjson.State{}, "nats_jetstream_storage=12Gi", "timescale_storage=8Gi"},
	} {
		t.Run(name, func(t *testing.T) {
			st := &State{Instance: "prod", KubeContext: "gke_p_z_c", Install: &rec,
				LiveVolumes: liveVolumes{JetStream: "12Gi", EventStore: "8Gi"},
				Values:      map[string]string{"natsCA": "ca"}}
			p, err := prepareUpgradePlan(context.Background(), st, &fakeUpgradeTofu{plans: []*tfjson.Plan{{}}, state: c.state}, t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			vars := strings.Join(p.vars, "\n")
			for _, want := range []string{c.wantJS, c.wantStoreIn} {
				if !strings.Contains(vars, want+"\n") && !strings.HasSuffix(vars, want) {
					t.Errorf("the apply does not pass %q:\n%s", want, vars)
				}
			}
		})
	}
}
