// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"

	dcv1beta1 "github.com/devicechain-io/dc-k8s/api/v1beta1"
	"github.com/devicechain-io/dc-k8s/functionalarea"
	"helm.sh/helm/v3/pkg/releaseutil"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
	"sigs.k8s.io/yaml"
)

// The default size is the chart's own. A second opinion on how big a store is would drift.
func TestTheDefaultStoreSizeIsTheChartsOwn(t *testing.T) {
	ch := chartForTest(t)
	bs, _ := ch.Values["blobStorage"].(map[string]interface{})
	p, _ := bs["persistence"].(map[string]interface{})
	if got := p["size"]; got != DefaultLocalObjectStoreSize {
		t.Errorf("dcctl's default store size is %q and the chart's blobStorage.persistence.size is %v",
			DefaultLocalObjectStoreSize, got)
	}
}

// --- callers ------------------------------------------------------------------------
//
// 🔴 THE HELPERS ARE TESTED ABOVE; THESE TEST THAT THE FUNCTIONS THAT MATTER CALL THEM.
// helmInstall needs a cluster and a Helm release to run, and stepCheckClusterSingletons a
// cluster to read, so the call is held by the source, the way the object-store rollout check
// is. A mutant that deleted either call passed every helper test.

// callsIn returns the names of the functions called inside the named top-level function, and
// whether any call's results are discarded into blank identifiers only.
func callsIn(t *testing.T, file, fn string) (calls map[string]token.Pos, blanked map[string]bool) {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), file, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	calls, blanked = map[string]token.Pos{}, map[string]bool{}
	var decl *ast.FuncDecl
	for _, d := range f.Decls {
		if fd, ok := d.(*ast.FuncDecl); ok && fd.Name.Name == fn {
			decl = fd
		}
	}
	if decl == nil {
		t.Fatalf("%s no longer declares %s", file, fn)
	}
	ast.Inspect(decl, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.AssignStmt:
			allBlank := true
			for _, l := range n.Lhs {
				if id, ok := l.(*ast.Ident); !ok || id.Name != "_" {
					allBlank = false
				}
			}
			if allBlank {
				for _, r := range n.Rhs {
					if c, ok := r.(*ast.CallExpr); ok {
						if id, ok := c.Fun.(*ast.Ident); ok {
							blanked[id.Name] = true
						}
					}
				}
			}
		case *ast.CallExpr:
			if id, ok := n.Fun.(*ast.Ident); ok {
				calls[id.Name] = n.Pos()
			}
		}
		return true
	})
	return calls, blanked
}

func TestHelmInstallCopiesTheObjectStoreCredentialsAndStampsTheirDigest(t *testing.T) {
	calls, blanked := callsIn(t, "helm.go", "helmInstall")
	for _, name := range []string{"syncObjectStoreCredentials", "withObjectStoreCredentialsDigest"} {
		if _, ok := calls[name]; !ok {
			t.Errorf("helmInstall never calls %s: the credentials are not copied, or a rotated key rolls nothing", name)
		}
		if blanked[name] {
			t.Errorf("helmInstall discards the result of %s", name)
		}
	}
	// ...before the release is installed or upgraded, because the pods read the Secret at
	// container start.
	if calls["syncObjectStoreCredentials"] > calls["newHelmUpgrade"] && calls["newHelmUpgrade"] != 0 {
		t.Error("the credentials are copied after the release is upgraded")
	}
}

func TestTheSingletonStepChecksTheObjectStoreBeforeAnythingIsWritten(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "singletons.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var decl *ast.FuncDecl
	for _, d := range f.Decls {
		if fd, ok := d.(*ast.FuncDecl); ok && fd.Name.Name == "stepCheckClusterSingletons" {
			decl = fd
		}
	}
	if decl == nil {
		t.Fatal("singletons.go no longer declares stepCheckClusterSingletons")
	}
	// The verdict that matters is the one a real run RETURNS: an `if err := precheckObjectStore(...);
	// err != nil { return ... }`. The rehearsal's own call, which only reports, does not count.
	returning := 0
	ast.Inspect(decl, func(n ast.Node) bool {
		is, ok := n.(*ast.IfStmt)
		if !ok {
			return true
		}
		as, ok := is.Init.(*ast.AssignStmt)
		if !ok || len(as.Rhs) != 1 {
			return true
		}
		call, ok := as.Rhs[0].(*ast.CallExpr)
		if !ok {
			return true
		}
		if id, ok := call.Fun.(*ast.Ident); !ok || id.Name != "precheckObjectStore" {
			return true
		}
		for _, stmt := range is.Body.List {
			if _, ok := stmt.(*ast.ReturnStmt); ok {
				returning++
			}
		}
		return true
	})
	if returning != 1 {
		t.Errorf("stepCheckClusterSingletons returns the verdict of precheckObjectStore in %d places, want 1: "+
			"an absent credentials Secret or a store that cannot be mounted is found ten minutes into the "+
			"install, not here", returning)
	}
}

func TestThePrecheckAsksBothQuestions(t *testing.T) {
	calls, _ := callsIn(t, "objectstore.go", "precheckObjectStore")
	for _, name := range []string{"checkObjectStoreMountable", "usesCredentialsSecret"} {
		if _, ok := calls[name]; !ok {
			t.Errorf("precheckObjectStore never calls %s", name)
		}
	}
	if _, ok := calls["precheckObjectStoreWith"]; !ok {
		t.Error("precheckObjectStore never reads the credentials Secret")
	}
}

// --- the credentials: no echo, labels, newline, rotation --------------------------------

const (
	sentinelAccess  = "ZZACCESSKEYZZ"
	sentinelSecret  = "ZZSECRETVALUEZZ"
	sentinelSession = "ZZSESSIONTOKENZZ"
)

// capture runs fn with stdout and stderr redirected and returns both.
func capture(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	oldOut, oldErr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = w, w
	done := make(chan string)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()
	func() {
		defer func() { os.Stdout, os.Stderr = oldOut, oldErr; _ = w.Close() }()
		fn()
	}()
	return <-done
}

func sourceSecret(data map[string]string) *corev1.Secret {
	s := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "storage", Name: "minio-keys"}, Data: map[string][]byte{}}
	for k, v := range data {
		s.Data[k] = []byte(v)
	}
	return s
}

func TestNoCredentialValueReachesAnErrorOrTheTerminal(t *testing.T) {
	st := &State{Instance: "acme", InstanceUID: testUID, ObjectStore: s3Store()}
	cases := map[string]map[string]string{
		"secret key missing":     {awsAccessKeyID: sentinelAccess, "AWS_SESSION_TOKEN": sentinelSession},
		"access key blank":       {awsAccessKeyID: "  ", awsSecretAccessKey: sentinelSecret, "AWS_SESSION_TOKEN": sentinelSession},
		"whitespace in the key":  {awsAccessKeyID: sentinelAccess, awsSecretAccessKey: "ZZSECRET VALUEZZ"},
		"control char in a key":  {awsAccessKeyID: sentinelAccess + "\x01", awsSecretAccessKey: sentinelSecret},
		"a well-formed Secret":   {awsAccessKeyID: sentinelAccess, awsSecretAccessKey: sentinelSecret, "AWS_SESSION_TOKEN": sentinelSession},
		"a different key naming": {"access": sentinelAccess, "secret": sentinelSecret},
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			client := fake.NewSimpleClientset(sourceSecret(data))
			var errs []error
			out := capture(t, func() {
				errs = append(errs, precheckObjectStoreWith(context.Background(), client, st))
				_, e := syncObjectStoreCredentials(context.Background(), client, st)
				errs = append(errs, e)
			})
			for _, leak := range []string{sentinelAccess, sentinelSecret, sentinelSession, "ZZSECRET VALUEZZ"} {
				if strings.Contains(out, leak) {
					t.Errorf("%q reached stdout or stderr: %s", leak, out)
				}
				for _, e := range errs {
					if e != nil && strings.Contains(e.Error(), leak) {
						t.Errorf("%q reached an error: %v", leak, e)
					}
				}
			}
		})
	}
}

func TestTheCopyIsLabelledAndOwnedSoItCanBeFoundAndUpdated(t *testing.T) {
	client := fake.NewSimpleClientset(sourceSecret(map[string]string{awsAccessKeyID: sentinelAccess, awsSecretAccessKey: sentinelSecret}))
	st := &State{Instance: "acme", InstanceUID: testUID, ObjectStore: s3Store()}
	if _, err := syncObjectStoreCredentials(context.Background(), client, st); err != nil {
		t.Fatal(err)
	}
	got, err := client.CoreV1().Secrets(InstanceNamespace("acme")).Get(context.Background(), "dci-acme-blob-s3", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got.Labels["devicechain.io/instance"] != "acme" {
		t.Errorf("the copy carries no instance label: %v", got.Labels)
	}
	if got.Namespace != InstanceNamespace("acme") {
		t.Errorf("the copy is in %s", got.Namespace)
	}
}

func TestASingleTrailingNewlineIsTrimmedFromACopiedValue(t *testing.T) {
	for _, tail := range []string{"\n", "\r\n"} {
		client := fake.NewSimpleClientset(sourceSecret(map[string]string{
			awsAccessKeyID: sentinelAccess + tail, awsSecretAccessKey: sentinelSecret + tail}))
		st := &State{Instance: "acme", InstanceUID: testUID, ObjectStore: s3Store()}
		if _, err := syncObjectStoreCredentials(context.Background(), client, st); err != nil {
			t.Fatalf("%q: %v", tail, err)
		}
		got, _ := client.CoreV1().Secrets(InstanceNamespace("acme")).Get(context.Background(), "dci-acme-blob-s3", metav1.GetOptions{})
		if v := got.StringData[awsAccessKeyID] + string(got.Data[awsAccessKeyID]); v != sentinelAccess {
			t.Errorf("%q: the copied access key is %q", tail, v)
		}
	}
	// Two newlines are not one: the second is not repaired.
	client := fake.NewSimpleClientset(sourceSecret(map[string]string{
		awsAccessKeyID: sentinelAccess + "\n\n", awsSecretAccessKey: sentinelSecret}))
	if _, err := syncObjectStoreCredentials(context.Background(),
		client, &State{Instance: "acme", InstanceUID: testUID, ObjectStore: s3Store()}); err == nil {
		t.Error("a value ending in two newlines was accepted")
	}
}

// 🔴 A ROTATED KEY MUST CHANGE THE POD TEMPLATE. The env is a secretKeyRef, read once at
// container start, and the copy keeps its name when its content changes, so without a digest
// the pods keep the old key after a same-version `dcctl upgrade`.
func TestRotatingTheKeyChangesTheDigestAndTheRenderedPodAnnotation(t *testing.T) {
	t.Parallel()
	src := sourceSecret(map[string]string{awsAccessKeyID: sentinelAccess, awsSecretAccessKey: "secret-one"})
	client := fake.NewSimpleClientset(src)
	st := &State{Instance: "acme", InstanceUID: testUID, ObjectStore: s3Store()}

	first, err := syncObjectStoreCredentials(context.Background(), client, st)
	if err != nil || first == "" {
		t.Fatalf("digest = %q, err = %v", first, err)
	}
	again, _ := syncObjectStoreCredentials(context.Background(), client, st)
	if again != first {
		t.Error("the digest changed with nothing rotated, which would roll the pods on every upgrade")
	}
	src.Data[awsSecretAccessKey] = []byte("secret-two")
	if _, err := client.CoreV1().Secrets("storage").Update(context.Background(), src, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	second, _ := syncObjectStoreCredentials(context.Background(), client, st)
	if second == first {
		t.Fatal("rotating the secret key did not change the digest")
	}
	for _, d := range []string{first, second} {
		if strings.Contains(d, "secret-") {
			t.Errorf("the digest contains the credential: %s", d)
		}
	}

	annotation := func(digest string) string {
		c := composeStoreWith(t, s3Store(), nil, func(vals map[string]interface{}) {
			withObjectStoreCredentialsDigest(vals, digest)
		})
		return c.deployment(t, "user-management").Spec.Template.Annotations["checksum/blob-s3-credentials"]
	}
	a1, a2 := annotation(first), annotation(second)
	if a1 != first || a2 != second || a1 == a2 {
		t.Errorf("rendered annotations %q and %q do not follow the digests %q and %q", a1, a2, first, second)
	}
	if got := annotation(""); got != "" {
		t.Errorf("an instance with no digest carries the annotation %q", got)
	}
	// Only the areas that read the credentials roll.
	c := composeStoreWith(t, s3Store(), nil, func(v map[string]interface{}) { withObjectStoreCredentialsDigest(v, first) })
	if _, has := c.deployment(t, "device-management").Spec.Template.Annotations["checksum/blob-s3-credentials"]; has {
		t.Error("an area that does not use the store rolls when the key rotates")
	}
}

func TestTheDigestSeparatesItsFields(t *testing.T) {
	a := objectStoreCredentialsDigest(map[string]string{awsAccessKeyID: "ab", awsSecretAccessKey: "c"})
	b := objectStoreCredentialsDigest(map[string]string{awsAccessKeyID: "a", awsSecretAccessKey: "bc"})
	if a == b {
		t.Error("two different credential pairs share a digest")
	}
}

// composeStoreWith is composeStore with a State tweak and a tweak of the install values.
func composeStoreWith(t *testing.T, store *dcv1beta1.ObjectStoreSpec, stTweak func(*State), valsTweak func(map[string]interface{})) composedStore {
	t.Helper()
	st := profileState(false, false, "default")("dc-os")
	st.ObjectStore = store
	if stTweak != nil {
		stTweak(st)
	}
	ch := chartForTest(t)
	vals, doc, err := releaseValues(t.Context(), ch, st, nil)
	if err != nil {
		t.Fatalf("composing the release values: %v", err)
	}
	if valsTweak != nil {
		valsTweak(vals)
	}
	manifest, err := renderChartClientSide(t.Context(), ch, vals)
	if err != nil {
		t.Fatalf("rendering the install values: %v", err)
	}
	return composedStore{vals: vals, doc: string(doc), manifest: manifest}
}

// --- colocation and the mount guard --------------------------------------------------

func managedLocal(st *State) {
	st.Provider = "local"
	st.Binding = ClusterBinding{Managed: true}
}

func TestADcctlCreatedClusterColocatesTheStoresPods(t *testing.T) {
	t.Parallel()
	c := composeStoreWith(t, fsStore0(), managedLocal, nil)
	bs, _ := c.vals["blobStorage"].(map[string]interface{})
	p, _ := bs["persistence"].(map[string]interface{})
	if p["colocate"] != true {
		t.Fatalf("persistence = %v: a managed cluster's store is not colocated", p)
	}
	dep := c.deployment(t, "user-management")
	aff := dep.Spec.Template.Spec.Affinity
	if aff == nil || aff.PodAffinity == nil || len(aff.PodAffinity.RequiredDuringSchedulingIgnoredDuringExecution) != 1 {
		t.Fatalf("no required pod affinity: %+v", aff)
	}
	term := aff.PodAffinity.RequiredDuringSchedulingIgnoredDuringExecution[0]
	if term.TopologyKey != "kubernetes.io/hostname" || term.LabelSelector.MatchLabels["devicechain.io/blob-store"] != "mounted" {
		t.Errorf("affinity term = %+v", term)
	}
	if dep.Spec.Template.Labels["devicechain.io/blob-store"] != "mounted" {
		t.Error("the pod does not carry the label its own affinity selects, so it could not be admitted")
	}
	// An area that does not mount the store is not pinned.
	if other := c.deployment(t, "device-management").Spec.Template.Spec.Affinity; other != nil && other.PodAffinity != nil {
		t.Error("an area that does not mount the store is pinned to its node")
	}
}

func fsStore0() *dcv1beta1.ObjectStoreSpec {
	return &dcv1beta1.ObjectStoreSpec{Backend: dcv1beta1.ObjectStoreFilesystem, Size: "5Gi"}
}

func TestOtherClustersDoNotColocate(t *testing.T) {
	t.Parallel()
	remote := composeStoreWith(t, fsStore0(), nil, nil)
	if _, set := remote.vals["blobStorage"].(map[string]interface{})["persistence"].(map[string]interface{})["colocate"]; set {
		t.Error("a cluster dcctl was pointed at was told to colocate")
	}
	rwx := fsStore0()
	rwx.AccessMode = "ReadWriteMany"
	c := composeStoreWith(t, rwx, managedLocal, nil)
	if _, set := c.vals["blobStorage"].(map[string]interface{})["persistence"].(map[string]interface{})["colocate"]; set {
		t.Error("a ReadWriteMany store was told to colocate")
	}
}

// The chart's own guard against several pods on one ReadWriteOnce claim learns the rule:
// colocation is a placement answer to it, not an exemption from it.
func TestTheChartRefusesTwoReplicasOnAReadWriteOnceClaimUnlessColocated(t *testing.T) {
	t.Parallel()
	two := func(v map[string]interface{}) {
		v["functionalAreas"] = map[string]interface{}{"user-management": map[string]interface{}{"replicas": 2}}
	}
	ch := chartForTest(t)
	st := profileState(false, false, "default")("dc-os")
	st.ObjectStore = fsStore0()
	vals, _, err := releaseValues(t.Context(), ch, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	two(vals)
	if _, err := renderChartClientSide(t.Context(), ch, vals); err == nil ||
		!strings.Contains(err.Error(), "not ReadWriteMany") {
		t.Fatalf("two replicas on a ReadWriteOnce claim rendered: %v", err)
	}

	st2 := profileState(false, false, "default")("dc-os")
	st2.ObjectStore = fsStore0()
	managedLocal(st2)
	vals2, _, err := releaseValues(t.Context(), ch, st2, nil)
	if err != nil {
		t.Fatal(err)
	}
	two(vals2)
	if _, err := renderChartClientSide(t.Context(), ch, vals2); err != nil {
		t.Errorf("a colocated claim was refused: %v", err)
	}
}

func TestDcctlRefusesUpFrontAStoreThatMorePodsWouldMountThanOneNodeCanHold(t *testing.T) {
	two := func(v map[string]interface{}) {
		v["functionalAreas"] = map[string]interface{}{"user-management": map[string]interface{}{"replicas": 2}}
	}
	remote := &State{Instance: "dc-os", Profile: "default", Values: map[string]string{}, ObjectStore: fsStore0()}
	if err := checkObjectStoreMountableWith(t.Context(), remote, two); err == nil ||
		!strings.Contains(err.Error(), "ReadWriteMany") {
		t.Fatalf("a shared ReadWriteOnce store was accepted up front: %v", err)
	}
	// Fine: one pod, a shared volume, a bucket, nothing, or a colocated managed cluster.
	if err := checkObjectStoreMountableWith(t.Context(), remote, nil); err != nil {
		t.Errorf("one pod on a ReadWriteOnce store was refused: %v", err)
	}
	rwx := *remote
	rwx.ObjectStore = fsStore0()
	rwx.ObjectStore.AccessMode = "ReadWriteMany"
	if err := checkObjectStoreMountableWith(t.Context(), &rwx, two); err != nil {
		t.Errorf("a ReadWriteMany store was refused: %v", err)
	}
	managed := *remote
	managedLocal(&managed)
	if err := checkObjectStoreMountableWith(t.Context(), &managed, two); err != nil {
		t.Errorf("a colocated store was refused: %v", err)
	}
	for _, other := range []*dcv1beta1.ObjectStoreSpec{nil, s3Store()} {
		o := *remote
		o.ObjectStore = other
		if err := checkObjectStoreMountableWith(t.Context(), &o, two); err != nil {
			t.Errorf("a store that mounts no volume was refused: %v", err)
		}
	}
}

// The count itself, over a manifest the current chart cannot render (it takes a second
// consuming area): two Deployments mounting the store volume are two pods on one node.
func TestTwoAreasOnOneReadWriteOnceStoreAreRefused(t *testing.T) {
	dep := func(area string, replicas int) string {
		return "apiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: " + area +
			"\n  labels:\n    devicechain.io/functional-area: " + area +
			"\nspec:\n  replicas: " + string(rune('0'+replicas)) +
			"\n  template:\n    spec:\n      volumes:\n        - name: blob-store\n"
	}
	other := "apiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: x\nspec:\n  replicas: 3\n  template:\n    spec:\n      volumes:\n        - name: other\n"
	two := dep("user-management", 1) + "---\n" + dep("update-management", 1) + "---\n" + other
	err := refuseSharedReadWriteOnce(two)
	if err == nil || !strings.Contains(err.Error(), "user-management x1") || !strings.Contains(err.Error(), "update-management x1") {
		t.Fatalf("two single-replica areas on one ReadWriteOnce store: %v", err)
	}
	if err := refuseSharedReadWriteOnce(dep("user-management", 1) + "---\n" + other); err != nil {
		t.Errorf("one pod was refused: %v", err)
	}
}

// --- the chart ----------------------------------------------------------------------

func TestTheChartKeepsTheStoreVolumeWhenHelmWouldDeleteIt(t *testing.T) {
	t.Parallel()
	c := composeStoreWith(t, fsStore0(), nil, nil)
	pvc := c.pvc(t)
	if pvc == nil {
		t.Fatal("no claim")
	}
	if pvc.Annotations["helm.sh/resource-policy"] != "keep" {
		t.Errorf("the claim can be deleted by Helm: %v", pvc.Annotations)
	}
}

func TestTheChartRefusesS3CredentialsForAFilesystemStoreAndUnknownS3Keys(t *testing.T) {
	ch := chartForTest(t)
	vals := inlineValues()
	vals["blobStorage"] = map[string]interface{}{"s3": map[string]interface{}{"credentialsSecret": "x"}}
	if _, err := renderChartClientSide(t.Context(), ch, vals); err == nil ||
		!strings.Contains(err.Error(), "credentialsSecret") {
		t.Errorf("S3 credentials with the filesystem backend rendered: %v", err)
	}
	vals["blobStorage"] = map[string]interface{}{"s3": map[string]interface{}{"credentialsSecert": "x"}}
	if _, err := renderChartClientSide(t.Context(), ch, vals); err == nil {
		t.Error("a misspelled blobStorage.s3 key was accepted")
	}
}

func TestTheServiceAccountCarriesItsAnnotations(t *testing.T) {
	t.Parallel()
	c := composeStoreWith(t, nil, nil, func(v map[string]interface{}) {
		v["serviceAccount"] = map[string]interface{}{
			"create": true, "automountToken": false,
			"annotations": map[string]interface{}{"eks.amazonaws.com/role-arn": "arn:aws:iam::1:role/dc"},
		}
	})
	if !strings.Contains(c.manifest, "eks.amazonaws.com/role-arn: arn:aws:iam::1:role/dc") {
		t.Error("the ServiceAccount annotation did not render")
	}
}

// --- the declaration across a re-run -----------------------------------------------------

func TestARerunWithNoStoreFlagKeepsTheDeclaredStore(t *testing.T) {
	declared := fsStore()
	read := func(spec *dcv1beta1.ObjectStoreSpec) func(context.Context, string, string) (*dcv1beta1.Instance, error) {
		return func(context.Context, string, string) (*dcv1beta1.Instance, error) {
			inst := aDeclaration(dcv1beta1.PhaseReady, "")
			inst.UID = "7f0d4f2e-0000-4000-8000-000000000001"
			inst.Spec = dcv1beta1.InstanceSpec{Provider: "local", Profile: "default", ObjectStore: spec}
			return inst, nil
		}
	}
	run := func(t *testing.T, declared *dcv1beta1.ObjectStoreSpec, st *State) (written *dcv1beta1.ObjectStoreSpec, wrote bool) {
		prevRead, prevWrite := readInstanceDeclaration, writeInstanceDeclaration
		t.Cleanup(func() { readInstanceDeclaration, writeInstanceDeclaration = prevRead, prevWrite })
		readInstanceDeclaration = read(declared)
		writeInstanceDeclaration = func(_ context.Context, _, _ string, spec dcv1beta1.InstanceSpec, _ string, _ bool) error {
			written, wrote = spec.ObjectStore, true
			return nil
		}
		if err := stepDeclareInstance(context.Background(), st); err != nil {
			t.Fatal(err)
		}
		return written, wrote
	}

	t.Run("no flag: carried forward over an empty resolution", func(t *testing.T) {
		st := &State{Instance: "prod", Provider: "local", Profile: "default"}
		got, _ := run(t, declared, st)
		if !reflect.DeepEqual(got, declared) {
			t.Errorf("a flagless re-run wrote %+v over the declared %+v", got, declared)
		}
	})
	t.Run("no flag: carried forward over a different resolved default", func(t *testing.T) {
		st := &State{Instance: "prod", Provider: "local", Profile: "default",
			ObjectStore: &dcv1beta1.ObjectStoreSpec{Backend: "filesystem", Size: "5Gi"}}
		got, _ := run(t, declared, st)
		if !reflect.DeepEqual(got, declared) {
			t.Errorf("the flags' default %+v replaced the declared store %+v", got, declared)
		}
	})
	t.Run("an instance declared with none stays with none", func(t *testing.T) {
		st := &State{Instance: "prod", Provider: "local", Profile: "default",
			ObjectStore: &dcv1beta1.ObjectStoreSpec{Backend: "filesystem", Size: "5Gi"}}
		got, _ := run(t, nil, st)
		if got != nil {
			t.Errorf("a store was invented for an instance declared without one: %+v", got)
		}
	})
	t.Run("an explicit flag is the operator's word", func(t *testing.T) {
		bigger := fsStore()
		bigger.Size = "9Gi"
		st := &State{Instance: "prod", Provider: "local", Profile: "default", ObjectStore: bigger, ObjectStoreExplicit: true}
		got, _ := run(t, declared, st)
		if !reflect.DeepEqual(got, bigger) {
			t.Errorf("an explicit store was overwritten by the declared one: %+v", got)
		}
	})
	t.Run("a rehearsal reads nothing", func(t *testing.T) {
		prev := readInstanceDeclaration
		t.Cleanup(func() { readInstanceDeclaration = prev })
		readInstanceDeclaration = func(context.Context, string, string) (*dcv1beta1.Instance, error) {
			t.Error("a dry run read the declaration")
			return nil, nil
		}
		if err := stepDeclareInstance(context.Background(), &State{Instance: "prod", DryRun: true}); err != nil {
			t.Fatal(err)
		}
	})
}

func TestEveryRefusedChangeToADeclaredStore(t *testing.T) {
	spec := func(os *dcv1beta1.ObjectStoreSpec) dcv1beta1.InstanceSpec {
		return dcv1beta1.InstanceSpec{Provider: "local", Cluster: "c", Managed: true, ObjectStore: os}
	}
	fs := func(mut func(*dcv1beta1.ObjectStoreSpec)) *dcv1beta1.ObjectStoreSpec {
		o := &dcv1beta1.ObjectStoreSpec{Backend: "filesystem", Size: "5Gi", StorageClass: "fast", AccessMode: "ReadWriteMany"}
		if mut != nil {
			mut(o)
		}
		return o
	}
	s3 := func(mut func(*dcv1beta1.S3ObjectStoreSpec)) *dcv1beta1.ObjectStoreSpec {
		o := s3Store()
		o.S3.CredentialsSecret = nil
		if mut != nil {
			mut(o.S3)
		}
		return o
	}
	refused := map[string][2]*dcv1beta1.ObjectStoreSpec{
		"removal of a volume":  {fs(nil), nil},
		"removal of a bucket":  {s3(nil), nil},
		"volume to bucket":     {fs(nil), s3(nil)},
		"bucket to volume":     {s3(nil), fs(nil)},
		"a smaller volume":     {fs(nil), fs(func(o *dcv1beta1.ObjectStoreSpec) { o.Size = "1Gi" })},
		"a storage class":      {fs(nil), fs(func(o *dcv1beta1.ObjectStoreSpec) { o.StorageClass = "slow" })},
		"dropping the class":   {fs(nil), fs(func(o *dcv1beta1.ObjectStoreSpec) { o.StorageClass = "" })},
		"an access mode":       {fs(nil), fs(func(o *dcv1beta1.ObjectStoreSpec) { o.AccessMode = "ReadWriteOnce" })},
		"a different bucket":   {s3(nil), s3(func(o *dcv1beta1.S3ObjectStoreSpec) { o.Bucket = "other-bucket" })},
		"a different endpoint": {s3(nil), s3(func(o *dcv1beta1.S3ObjectStoreSpec) { o.Endpoint = "https://other.example.com" })},
		"a different region":   {s3(nil), s3(func(o *dcv1beta1.S3ObjectStoreSpec) { o.Region = "us-west-2" })},
	}
	for name, c := range refused {
		t.Run("refuses "+name, func(t *testing.T) {
			err := ValidateInstanceSpecChange(spec(c[0]), spec(c[1]))
			if err == nil {
				t.Fatal("accepted")
			}
			for _, leak := range []string{"other-bucket", "other.example.com", "us-west-2"} {
				if strings.Contains(err.Error(), leak) {
					t.Errorf("the refusal echoes %q", leak)
				}
			}
		})
	}

	allowed := map[string][2]*dcv1beta1.ObjectStoreSpec{
		"adding a volume":   {nil, fs(nil)},
		"adding a bucket":   {nil, s3(nil)},
		"growing a volume":  {fs(nil), fs(func(o *dcv1beta1.ObjectStoreSpec) { o.Size = "10Gi" })},
		"the same volume":   {fs(nil), fs(nil)},
		"unset mode as RWO": {fs(func(o *dcv1beta1.ObjectStoreSpec) { o.AccessMode = "" }), fs(func(o *dcv1beta1.ObjectStoreSpec) { o.AccessMode = "ReadWriteOnce" })},
		"another credentials": {s3(nil), s3(func(o *dcv1beta1.S3ObjectStoreSpec) {
			o.CredentialsSecret = &dcv1beta1.SecretReference{Namespace: "n", Name: "other"}
		})},
		"the path style": {s3(nil), s3(func(o *dcv1beta1.S3ObjectStoreSpec) { o.UsePathStyle = !o.UsePathStyle })},
		"nothing at all": {nil, nil},
	}
	for name, c := range allowed {
		allowedFrom, allowedTo := c[0], c[1]
		if name == "unset mode as RWO" {
			allowedFrom.StorageClass, allowedTo.StorageClass = "fast", "fast"
		}
		t.Run("allows "+name, func(t *testing.T) {
			if err := ValidateInstanceSpecChange(spec(allowedFrom), spec(allowedTo)); err != nil {
				t.Errorf("refused: %v", err)
			}
		})
	}
	_ = resource.MustParse
}

// The chart's pod-count guard counts pods across the consuming areas: user-management and
// update-management are two pods on one ReadWriteOnce claim. Colocation is its one exception.
func TestTwoConsumingAreasNeedAColocatedOrSharedClaim(t *testing.T) {
	t.Parallel()
	areas, err := ResolveEnabledAreas("default", []string{"update-management"})
	if err != nil {
		t.Fatal(err)
	}
	remote := &State{Instance: "dc-os", Profile: "default", EnableAreas: []string{"update-management"},
		EnabledAreas: areas, Values: map[string]string{}, ObjectStore: fsStore0()}
	err = checkObjectStoreMountable(t.Context(), remote)
	if err == nil || !strings.Contains(err.Error(), "update-management") {
		t.Fatalf("two areas on a ReadWriteOnce store were accepted up front: %v", err)
	}

	managed := *remote
	managedLocal(&managed)
	if err := checkObjectStoreMountable(t.Context(), &managed); err != nil {
		t.Errorf("a colocated store was refused: %v", err)
	}
	// ...and the same State really renders, with both areas pinned together.
	c := composeStoreWith(t, fsStore0(), func(st *State) {
		managedLocal(st)
		st.EnableAreas, st.EnabledAreas = remote.EnableAreas, areas
	}, nil)
	for _, a := range []string{"user-management", "update-management"} {
		aff := c.deployment(t, a).Spec.Template.Spec.Affinity
		if aff == nil || aff.PodAffinity == nil {
			t.Errorf("%s is not colocated with the other pods that mount the claim", a)
		}
	}

	rwx := *remote
	rwx.ObjectStore = fsStore0()
	rwx.ObjectStore.AccessMode = "ReadWriteMany"
	if err := checkObjectStoreMountable(t.Context(), &rwx); err != nil {
		t.Errorf("a ReadWriteMany store was refused: %v", err)
	}
}

// --- update-management in the default profile ----------------------------------------

// updateManagementConfigIn reads the config update-management receives out of a rendered
// manifest (the per-area ConfigMap), as a decoded map.
func updateManagementConfigIn(t *testing.T, manifest string) map[string]interface{} {
	t.Helper()
	for _, doc := range releaseutil.SplitManifests(manifest) {
		var cm corev1.ConfigMap
		if yaml.Unmarshal([]byte(doc), &cm) != nil || cm.Kind != "ConfigMap" {
			continue
		}
		raw, ok := cm.Data["update-management"]
		if !ok {
			continue
		}
		var out map[string]interface{}
		if err := json.Unmarshal([]byte(raw), &out); err != nil {
			t.Fatalf("update-management's config is not JSON: %v", err)
		}
		return out
	}
	t.Fatal("no ConfigMap carries update-management's config: the area is not rendered")
	return nil
}

// podsMountingTheStore counts the pods in a render that mount the store volume, per area.
func podsMountingTheStore(t *testing.T, c composedStore) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	for _, a := range []string{"user-management", "update-management"} {
		for _, v := range c.deployment(t, a).Spec.Template.Spec.Volumes {
			if v.Name == "blob-store" {
				out[a] = true
			}
		}
	}
	return out
}

// 🔴 THE DEAD END THE DEFAULT PROFILE WOULD OTHERWISE BE. update-management is in `default`,
// so an instance on a cluster dcctl did not create, whose filesystem store is a ReadWriteOnce
// volume user-management already mounts, would be refused at bootstrap and at every upgrade,
// and its store cannot be changed. The area instead runs with no store: ONE pod mounts the
// claim, the preflight passes, and the service is told objectStore none so it refuses artifacts
// loudly rather than writing into a volume it does not have.
func TestUpdateManagementRunsWithoutTheStoreWhereTheVolumeCannotBeShared(t *testing.T) {
	t.Parallel()
	if areas, err := functionalarea.ResolveEnabled("default", nil); err != nil ||
		!slices.Contains(areas, functionalarea.UpdateMgmt) {
		t.Fatalf("update-management must be in the default profile (%v, %v)", areas, err)
	}
	remote := &State{Instance: "dc-os", Profile: "default", Values: map[string]string{}, ObjectStore: fsStore0()}
	if !updateStoreMountless(remote) {
		t.Fatal("a ReadWriteOnce store on a cluster dcctl was pointed at must run update-management without the store")
	}
	if err := checkObjectStoreMountable(t.Context(), remote); err != nil {
		t.Fatalf("the preflight refused an instance that mounts the store in ONE pod: %v", err)
	}

	c := composeStoreWith(t, fsStore0(), nil, nil)
	mounts := podsMountingTheStore(t, c)
	if !mounts["user-management"] || mounts["update-management"] {
		t.Errorf("pods mounting the claim = %v; want user-management only", mounts)
	}
	cfg := updateManagementConfigIn(t, c.manifest)
	updates, _ := cfg["updates"].(map[string]interface{})
	if updates["objectStore"] != "none" {
		t.Errorf("update-management is not told to run without the store: %v", cfg)
	}
}

// ...and everywhere the store CAN be shared, or the operator asked for the area, nothing degrades:
// the area mounts the store and is told nothing.
func TestUpdateManagementKeepsTheStoreWhereItCanBeShared(t *testing.T) {
	t.Parallel()
	rwx := fsStore0()
	rwx.AccessMode = "ReadWriteMany"
	cases := map[string]struct {
		store *dcv1beta1.ObjectStoreSpec
		st    func(*State)
	}{
		"a ReadWriteMany volume":              {rwx, nil},
		"a cluster dcctl created (colocated)": {fsStore0(), managedLocal},
		"an S3 store":                         {s3Store(), nil},
	}
	for name, tc := range cases {
		st := &State{Instance: "dc-os", Profile: "default", Values: map[string]string{}, ObjectStore: tc.store}
		if tc.st != nil {
			tc.st(st)
		}
		if updateStoreMountless(st) {
			t.Errorf("%s: update-management was taken off the store", name)
		}
	}
	// Asked for by name: the refusal stays (the operator wants a working artifact plane).
	asked := &State{Instance: "dc-os", Profile: "default", EnableAreas: []string{"update-management"},
		Values: map[string]string{}, ObjectStore: fsStore0()}
	if updateStoreMountless(asked) {
		t.Error("an explicitly requested update-management was silently taken off the store")
	}
	// The full profile on a ReadWriteOnce store keeps today's refusal as well.
	full := &State{Instance: "dc-os", Profile: "full", Values: map[string]string{}, ObjectStore: fsStore0()}
	if updateStoreMountless(full) {
		t.Error("the full profile was silently taken off the store")
	}

	c := composeStoreWith(t, rwx, nil, nil)
	if !podsMountingTheStore(t, c)["update-management"] {
		t.Error("update-management does not mount a shareable store")
	}
	if updates, _ := updateManagementConfigIn(t, c.manifest)["updates"].(map[string]interface{}); updates["objectStore"] != nil {
		t.Errorf("a shareable store renders objectStore = %v", updates["objectStore"])
	}
}

// The setting is dcctl's: an overlay may not set it (it would put the area back on a volume it
// cannot share, or take it off a store it should use).
func TestTheOverlayCannotSetTheUpdateObjectStore(t *testing.T) {
	t.Parallel()
	overlay := map[string]interface{}{"functionalAreas": map[string]interface{}{"update-management": map[string]interface{}{
		"config": map[string]interface{}{"updates": map[string]interface{}{"objectStore": "none"}}}}}
	if err := ValidateValuesOverlay(overlay); err == nil || !strings.Contains(err.Error(), "objectStore") {
		t.Fatalf("an overlay setting updates.objectStore was accepted: %v", err)
	}
}
