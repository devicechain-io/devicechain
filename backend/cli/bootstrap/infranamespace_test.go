// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/terraform-exec/tfexec"
	tfjson "github.com/hashicorp/terraform-json"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// fakeState is a state file that manages exactly the addresses it is given, and a
// Show that can fail on demand.
type fakeState struct {
	addresses []string
	showErr   error

	imported  []string
	importID  string
	importErr error
}

func (f *fakeState) Show(context.Context, ...tfexec.ShowOption) (*tfjson.State, error) {
	if f.showErr != nil {
		return nil, f.showErr
	}
	// Addresses are placed one module deep, as the real ones are: the namespace is
	// module.namespace.*, the database credentials module.cnpg_rdb.*. A flat fixture
	// would pass against a walker that never descends.
	root := &tfjson.StateModule{}
	for _, a := range f.addresses {
		root.ChildModules = append(root.ChildModules, &tfjson.StateModule{
			Address:   moduleOf(a),
			Resources: []*tfjson.StateResource{{Address: a}},
		})
	}
	return &tfjson.State{Values: &tfjson.StateValues{RootModule: root}}, nil
}

func (f *fakeState) Import(_ context.Context, address, id string, _ ...tfexec.ImportOption) error {
	if f.importErr != nil {
		return f.importErr
	}
	f.imported = append(f.imported, address)
	f.importID = id
	return nil
}

// moduleOf is the fixture's own idea of a module prefix, deliberately not shared with
// the walker under test.
func moduleOf(address string) string {
	parts := strings.Split(address, ".")
	if len(parts) < 2 {
		return ""
	}
	return parts[0] + "." + parts[1]
}

// 🔴 THE NAMESPACE IS IMPORTED, NOT CREATED BY THE APPLY, AND NOT SWITCHED OFF.
//
// The Kubernetes provider's namespace resource does not adopt, so leaving the apply to
// create a namespace that is already there fails outright. Turning the toggle off
// instead plans a DESTROY of a namespace holding every database, the broker and every
// credential. Import is the only door that leaves the resource count where it is.
func TestTheInfrastructureNamespaceIsHandedToOpenTofuByImport(t *testing.T) {
	f := &fakeState{}
	if err := adoptInfraNamespace(context.Background(), f, "dc-system", []string{"kubeconfig_context=kind-dc"}); err != nil {
		t.Fatalf("adopting the namespace: %v", err)
	}

	// Literals: these name a resource in a tree this package does not compile, so
	// nothing else would catch them moving.
	if len(f.imported) != 1 || f.imported[0] != "module.namespace.kubernetes_namespace_v1.this[0]" {
		t.Errorf("imported %v, not the namespace resource the root declares", f.imported)
	}
	if f.importID != "dc-system" {
		t.Errorf("imported under id %q, so OpenTofu would manage a different namespace", f.importID)
	}
}

// ...and only once. `tofu import` refuses an address already in state, so a second
// bootstrap that asked again would fail on an instance that is entirely healthy.
func TestAnAlreadyManagedNamespaceIsNotImportedAgain(t *testing.T) {
	f := &fakeState{addresses: []string{"module.namespace.kubernetes_namespace_v1.this[0]"}}
	if err := adoptInfraNamespace(context.Background(), f, "dc-system", nil); err != nil {
		t.Fatalf("a managed namespace was treated as a failure: %v", err)
	}
	if len(f.imported) != 0 {
		t.Errorf("imported %v over a resource already in state", f.imported)
	}
}

// 🔴 A STATE THAT CANNOT BE READ IS NOT AN EMPTY STATE. Reading it as "nothing
// managed here" imports over an instance that already manages the namespace, and a
// double-managed namespace is one two runs can each decide to delete.
func TestAnUnreadableStateStopsTheImportRatherThanGuessing(t *testing.T) {
	boom := errors.New("state file is corrupt")
	f := &fakeState{showErr: boom}
	err := adoptInfraNamespace(context.Background(), f, "dc-system", nil)
	if err == nil {
		t.Fatal("an unreadable state was read as an unmanaged namespace")
	}
	if !errors.Is(err, boom) {
		t.Errorf("the failure lost its cause: %v", err)
	}
	if len(f.imported) != 0 {
		t.Error("it imported anyway")
	}
}

// A first bootstrap has a state file with no values at all, and that IS an answer:
// nothing is managed, so the namespace is imported.
func TestAFirstBootstrapImportsTheNamespace(t *testing.T) {
	f := &fakeState{}
	has, err := stateHasAddress(context.Background(), f, "module.namespace.kubernetes_namespace_v1.this[0]")
	if err != nil {
		t.Fatal(err)
	}
	if has {
		t.Error("an empty state reported the namespace as managed")
	}
}

// The namespace carries the labels the module declares, so importing it produces no
// diff. Different labels would make every apply report drift on an object that is not
// drifting.
func TestTheCreatedNamespaceCarriesTheLabelsTheModuleDeclares(t *testing.T) {
	c := fake.NewSimpleClientset()
	if err := ensureInfraNamespace(context.Background(), c, "dc-system"); err != nil {
		t.Fatalf("creating the namespace: %v", err)
	}

	ns, err := c.CoreV1().Namespaces().Get(context.Background(), "dc-system", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for k, want := range map[string]string{
		"app.kubernetes.io/managed-by": "opentofu",
		"devicechain.io/component":     "infrastructure",
	} {
		if ns.Labels[k] != want {
			t.Errorf("label %s is %q, not %q: the import would leave a diff on every apply",
				k, ns.Labels[k], want)
		}
	}
}

// An existing namespace is left as it is — this only has to make sure one is there.
func TestAnExistingInfrastructureNamespaceIsLeftAlone(t *testing.T) {
	c := fake.NewSimpleClientset(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
		Name:   "dc-system",
		Labels: map[string]string{"app.kubernetes.io/managed-by": "somebody"},
	}})
	if err := ensureInfraNamespace(context.Background(), c, "dc-system"); err != nil {
		t.Fatalf("an existing namespace was treated as a failure: %v", err)
	}
	ns, _ := c.CoreV1().Namespaces().Get(context.Background(), "dc-system", metav1.GetOptions{})
	if ns.Labels["app.kubernetes.io/managed-by"] != "somebody" {
		t.Error("an existing namespace was restamped")
	}
}

// ...unless it is on its way out, which is not a namespace anything can be built
// into: Kubernetes refuses new content there, so the credentials write would fail
// several calls later with a sentence about new content.
func TestBuildingIntoATerminatingInfrastructureNamespaceIsRefusedClearly(t *testing.T) {
	deleting := metav1.NewTime(time.Now())
	c := fake.NewSimpleClientset(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
		Name:              "dc-system",
		DeletionTimestamp: &deleting,
		Finalizers:        []string{"kubernetes"},
	}})
	err := ensureInfraNamespace(context.Background(), c, "dc-system")
	if err == nil {
		t.Fatal("a terminating namespace was accepted")
	}
	if !strings.Contains(err.Error(), "still being deleted") {
		t.Errorf("the refusal does not say what is happening: %v", err)
	}
}

// 🔴 THE WALKER HAS TO DESCEND. Every address that matters lives inside a module, and
// tfjson nests a child module per level rather than flattening them — a walker that
// only read the root would report every retired resource as absent, which is the
// fence answering "safe" for exactly the instances it exists to stop.
func TestTheStateWalkerFindsAddressesInsideModules(t *testing.T) {
	nested := &tfjson.StateModule{
		ChildModules: []*tfjson.StateModule{{
			Address: "module.outer",
			ChildModules: []*tfjson.StateModule{{
				Address:   "module.outer.module.inner",
				Resources: []*tfjson.StateResource{{Address: "module.outer.module.inner.some_resource.x"}},
			}},
		}},
	}
	if !moduleHasAddress(nested, "module.outer.module.inner.some_resource.x") {
		t.Error("a resource two modules deep was reported as absent")
	}
	if moduleHasAddress(nested, "module.outer.module.inner.some_resource.y") {
		t.Error("an address that is not there was reported as present")
	}
}
