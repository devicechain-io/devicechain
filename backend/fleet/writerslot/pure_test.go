// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package writerslot

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestAllTerminatedIsOnlyEverContainerStatuses(t *testing.T) {
	const pod = "pod-1"
	done := func(n ...bool) PodObservation {
		var cs []ContainerStatus
		for _, d := range n {
			cs = append(cs, ContainerStatus{Name: "c", Terminated: d})
		}
		return PodObservation{PodUID: pod, Containers: cs}
	}
	if !done(true).AllTerminated(pod) || !done(true, true).AllTerminated(pod) {
		t.Fatal("a pod whose every container terminated was not accepted")
	}
	// Every non-proof the spec names is refused: nothing observed, a container still running,
	// another pod, an observation of nothing.
	for name, obs := range map[string]PodObservation{
		"one container still running": done(true, false),
		"no containers at all":        {PodUID: pod},
		"a different pod":             {PodUID: "pod-2", Containers: []ContainerStatus{{Terminated: true}}},
		"an empty observation":        {},
	} {
		if obs.AllTerminated(pod) {
			t.Errorf("%s was accepted as termination", name)
		}
	}
	// Running and NodeReady describe liveness; they are never termination evidence.
	if (PodObservation{PodUID: pod, Running: true, NodeReady: true}).AllTerminated(pod) {
		t.Error("liveness fields were read as termination")
	}
}

func TestCheckClusterIdentityRefusesEachElementWithItsOwnReason(t *testing.T) {
	first := time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
	intent := Intent{Project: "p", Location: "l", CloudName: "dcf-abc", Nonce: "nonce", FirstSubmitAt: first}
	good := ObservedCluster{Project: "p", Location: "l", Name: "dcf-abc", NonceLabel: "nonce", CreateTime: first.Add(time.Minute), ResourceID: "id-1"}
	if r := CheckClusterIdentity(intent, good); r != ReasonVerified {
		t.Fatalf("the all-match case was refused: %s", r)
	}
	mut := func(f func(*ObservedCluster)) string { g := good; f(&g); return CheckClusterIdentity(intent, g) }
	cases := map[string]struct {
		got  string
		want string
	}{
		"name":                           {mut(func(o *ObservedCluster) { o.Name = "dcf-other" }), ReasonNameMismatch},
		"project":                        {mut(func(o *ObservedCluster) { o.Project = "q" }), ReasonProjectMismatch},
		"location":                       {mut(func(o *ObservedCluster) { o.Location = "m" }), ReasonLocationMismatch},
		"nonce":                          {mut(func(o *ObservedCluster) { o.NonceLabel = "wrong" }), ReasonNonceMismatch},
		"no label":                       {mut(func(o *ObservedCluster) { o.NonceLabel = "" }), ReasonNonceMismatch},
		"no resource id observed":        {mut(func(o *ObservedCluster) { o.ResourceID = "" }), ReasonIdentityMismatch},
		"created long before the intent": {mut(func(o *ObservedCluster) { o.CreateTime = first.Add(-10 * time.Minute) }), ReasonCreateBeforeIntent},
	}
	for name, c := range cases {
		if c.got != c.want {
			t.Errorf("%s: got %s want %s", name, c.got, c.want)
		}
	}
	// The skew boundary: exactly the margin before the intent is accepted, one nanosecond more is not.
	if r := mut(func(o *ObservedCluster) { o.CreateTime = first.Add(-defaultImportSkew) }); r != ReasonVerified {
		t.Errorf("creation exactly at the skew margin was refused: %s", r)
	}
	if r := mut(func(o *ObservedCluster) { o.CreateTime = first.Add(-defaultImportSkew - time.Nanosecond) }); r != ReasonCreateBeforeIntent {
		t.Errorf("creation just past the skew margin was accepted: %s", r)
	}
	// Once the API id is known, a different id under the same name is IDENTITY_MISMATCH.
	known := intent
	known.CloudResourceID = "id-1"
	if r := CheckClusterIdentity(known, good); r != ReasonVerified {
		t.Errorf("a matching id was refused: %s", r)
	}
	other := good
	other.ResourceID = "id-2"
	if r := CheckClusterIdentity(known, other); r != ReasonIdentityMismatch {
		t.Errorf("a different id under the same name: %s", r)
	}
	// No create time recorded for the intent is not a pass.
	noID := good
	noID.ResourceID = ""
	if r := CheckClusterIdentity(known, noID); r != ReasonIdentityMismatch {
		t.Errorf("a read naming no id was accepted against a known id: %s", r)
	}
	noIntent := intent
	noIntent.FirstSubmitAt = time.Time{}
	if r := CheckClusterIdentity(noIntent, good); r != ReasonCreateBeforeIntent {
		t.Errorf("an intent with no first_submit_at was accepted: %s", r)
	}
	// And an intent with no nonce never matches, not even an empty label.
	noNonce := intent
	noNonce.Nonce = ""
	empty := good
	empty.NonceLabel = ""
	if r := CheckClusterIdentity(noNonce, empty); r != ReasonNonceMismatch {
		t.Errorf("an empty nonce matched an empty label: %s", r)
	}
}

func TestCheckPoolImport(t *testing.T) {
	declared := []string{"database", "services"}
	if r := CheckPoolImport("id-1", "id-1", "database", declared); r != ReasonVerified {
		t.Errorf("a declared pool of the verified cluster was refused: %s", r)
	}
	if r := CheckPoolImport("id-1", "id-2", "database", declared); r != ReasonPoolParentNotVerified {
		t.Errorf("a pool of another cluster: %s", r)
	}
	if r := CheckPoolImport("", "", "database", declared); r != ReasonPoolParentNotVerified {
		t.Errorf("no verified cluster: %s", r)
	}
	if r := CheckPoolImport("id-1", "id-1", "stranger", declared); r != ReasonPoolNotDeclared {
		t.Errorf("an undeclared pool: %s", r)
	}
}

func TestStateRoleNameIsDerivedFromTheRunUID(t *testing.T) {
	n, err := StateRoleName("33333333-3333-3333-3333-333333333333")
	if err != nil || n != "fleet_run_33333333333333333333333333333333" {
		t.Fatalf("%q %v", n, err)
	}
	for _, bad := range []string{"", "x", "33333333-3333-3333-3333-33333333333;", `"; DROP ROLE x; --`} {
		if _, err := StateRoleName(bad); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
}

func TestDestroyAndImportExecutionFailLoudly(t *testing.T) {
	m := &Manager{}
	// DESTROY is refused before anything else is looked at, so no database is needed to see it.
	if _, err := m.Acquire(context.Background(), RunSpec{Kind: KindDestroy}); !errors.Is(err, ErrDestroyNotImplemented) {
		t.Fatalf("Acquire(DESTROY): %v", err)
	}
	if err := m.ExecuteImport(context.Background(), "a", "b"); !errors.Is(err, ErrImportNotImplemented) {
		t.Fatalf("ExecuteImport: %v", err)
	}
	var none Premises = NoPremises{}
	for _, mode := range []string{ModeKindJWKS, ModeGKEWI, "anything"} {
		if none.ExpiryHolds(mode) {
			t.Errorf("the default premises accepted EXPIRY in %s", mode)
		}
	}
}

// The data-class label is informational and is never authority for anything: no code of this
// package reads it. The check is on the source, because a label read cannot be seen in behaviour
// until someone relies on it.
func TestNoCodePathReadsALabelToAuthorize(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	var scanned int
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		scanned++
		for _, banned := range []string{"dc-fleet-data-class", "data_class", "DataClass"} {
			if strings.Contains(string(b), banned) {
				t.Errorf("%s mentions %q: the data-class label and column authorize nothing", f, banned)
			}
		}
	}
	if scanned == 0 {
		t.Fatal("scanned no source files")
	}
}

func TestVocabularyHelpers(t *testing.T) {
	if !in("HELD", allStates...) || in("FUTURE", allStates...) {
		t.Fatal("in() or allStates is wrong")
	}
	for _, s := range []string{StateFree, StateHeld, StateUncertain, StateExcluded, StateRecovering, StateQuarantined} {
		if !in(s, allStates...) {
			t.Errorf("%s missing from allStates", s)
		}
	}
	if lifecyclePermits(LifecycleDestroyed, KindCreate) || lifecyclePermits("FUTURE", KindCreate) {
		t.Error("a destroyed or unknown lifecycle permitted a kind")
	}
	if !lifecyclePermits(LifecycleTearingDown, KindRecover) || lifecyclePermits(LifecycleTearingDown, KindChange) {
		t.Error("TEARING_DOWN must permit only RECOVER (and the refused DESTROY)")
	}
}

// Nothing in this package reads the premise record yet, so the only ExpiryHolds a non-test file may
// declare is one that returns false. A permissive implementation landing here would let EXPIRY
// through without anything binding it to what E-f measured. Negative control: the helper
// expiryHoldsAlwaysFalse rejects a body that returns true.
func TestNoPremisesImplementationButTheRefusingOneExists(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	var found int
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, f, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range file.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok || fn.Recv == nil || fn.Name.Name != "ExpiryHolds" {
				continue
			}
			found++
			if !expiryHoldsAlwaysFalse(fn) {
				t.Errorf("%s: ExpiryHolds must be `return false` until a slice binds it to the premise record", fset.Position(fn.Pos()))
			}
		}
	}
	if found == 0 {
		t.Fatal("found no ExpiryHolds method: the scan is not looking at NoPremises")
	}
	// The control: a permissive body is recognised as one.
	src := `package x
type P struct{}
func (P) ExpiryHolds(string) bool { return true }
`
	cf, err := parser.ParseFile(token.NewFileSet(), "control.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	if expiryHoldsAlwaysFalse(cf.Decls[1].(*ast.FuncDecl)) {
		t.Fatal("the scan accepts `return true`: it cannot fail")
	}
}

func expiryHoldsAlwaysFalse(fn *ast.FuncDecl) bool {
	if fn.Body == nil || len(fn.Body.List) != 1 {
		return false
	}
	ret, ok := fn.Body.List[0].(*ast.ReturnStmt)
	if !ok || len(ret.Results) != 1 {
		return false
	}
	id, ok := ret.Results[0].(*ast.Ident)
	return ok && id.Name == "false"
}

func TestEachManagerIsItsOwnIncarnation(t *testing.T) {
	a, err := NewManager(nopDB(), "replica-1", Config{})
	if err != nil {
		t.Fatal(err)
	}
	b, err := NewManager(nopDB(), "replica-1", Config{})
	if err != nil {
		t.Fatal(err)
	}
	if a.HolderID() == b.HolderID() {
		t.Fatalf("two managers with one name share a lease holder id %q: a restart would renew a dead incarnation's lease", a.HolderID())
	}
	if !strings.HasPrefix(a.HolderID(), "replica-1#") {
		t.Fatalf("holder id %q lost the replica name", a.HolderID())
	}
}

// nopDB is a handle that never connects: gorm opens lazily and the automatic ping is disabled.
func nopDB() *gorm.DB {
	db, err := gorm.Open(postgres.New(postgres.Config{DSN: "host=127.0.0.1 port=1 user=x dbname=x sslmode=disable"}), &gorm.Config{DisableAutomaticPing: true})
	if err != nil {
		panic(err)
	}
	return db
}
