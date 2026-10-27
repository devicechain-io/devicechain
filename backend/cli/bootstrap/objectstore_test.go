// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	dcv1beta1 "github.com/devicechain-io/dc-k8s/api/v1beta1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
	"sigs.k8s.io/yaml"
)

func fsStore() *dcv1beta1.ObjectStoreSpec {
	return &dcv1beta1.ObjectStoreSpec{Backend: dcv1beta1.ObjectStoreFilesystem, Size: "2Gi",
		StorageClass: "fast", AccessMode: "ReadWriteMany"}
}

func s3Store() *dcv1beta1.ObjectStoreSpec {
	return &dcv1beta1.ObjectStoreSpec{Backend: dcv1beta1.ObjectStoreS3, S3: &dcv1beta1.S3ObjectStoreSpec{
		Bucket: "dc-objects", Region: "eu-west-1", Endpoint: "https://minio.example.com:9000", UsePathStyle: true,
		CredentialsSecret: &dcv1beta1.SecretReference{Namespace: "storage", Name: "minio-keys"},
	}}
}

// --- the flags -------------------------------------------------------------------

func TestResolveObjectStore(t *testing.T) {
	cases := []struct {
		name    string
		flags   ObjectStoreFlags
		local   bool
		want    *dcv1beta1.ObjectStoreSpec
		wantErr string
	}{
		{name: "unset on a dcctl-created cluster is a small filesystem store", local: true,
			want: &dcv1beta1.ObjectStoreSpec{Backend: "filesystem", Size: "5Gi"}},
		{name: "unset on a cluster dcctl was pointed at is no store", local: false, want: nil},
		{name: "none overrides the local default", local: true, flags: ObjectStoreFlags{Mode: "none"}, want: nil},
		{name: "size alone follows the local default", local: true, flags: ObjectStoreFlags{Size: "3Gi"},
			want: &dcv1beta1.ObjectStoreSpec{Backend: "filesystem", Size: "3Gi"}},
		{name: "filesystem with every detail", flags: ObjectStoreFlags{Mode: "filesystem", Size: "2Gi",
			StorageClass: "fast", AccessMode: "ReadWriteMany"}, want: fsStore()},
		{name: "filesystem on a remote cluster defaults its size", flags: ObjectStoreFlags{Mode: "filesystem"},
			want: &dcv1beta1.ObjectStoreSpec{Backend: "filesystem", Size: "5Gi"}},
		{name: "s3", flags: ObjectStoreFlags{Mode: "s3", S3Bucket: "dc-objects", S3Region: "eu-west-1",
			S3Endpoint: "https://minio.example.com:9000", S3PathStyle: true, S3Credentials: "storage/minio-keys"},
			want: s3Store()},
		{name: "s3 with the pod identity", flags: ObjectStoreFlags{Mode: "s3", S3Bucket: "dc-objects", S3Region: "us-east-1"},
			want: &dcv1beta1.ObjectStoreSpec{Backend: "s3", S3: &dcv1beta1.S3ObjectStoreSpec{Bucket: "dc-objects", Region: "us-east-1"}}},

		{name: "details with no mode on a remote cluster", flags: ObjectStoreFlags{Size: "1Gi"}, wantErr: "need --object-store"},
		{name: "s3 flags with the local default", local: true, flags: ObjectStoreFlags{S3Bucket: "b"}, wantErr: "need --object-store s3"},
		{name: "unknown mode", flags: ObjectStoreFlags{Mode: "gcs"}, wantErr: "not one of none, filesystem or s3"},
		{name: "none with a detail", flags: ObjectStoreFlags{Mode: "none", Size: "1Gi"}, wantErr: "takes no other"},
		{name: "none with an s3 detail", flags: ObjectStoreFlags{Mode: "none", S3Bucket: "bucket"}, wantErr: "takes no other"},
		{name: "filesystem with s3 flags", flags: ObjectStoreFlags{Mode: "filesystem", S3Bucket: "bucket"}, wantErr: "would be ignored"},
		{name: "s3 with a volume size", flags: ObjectStoreFlags{Mode: "s3", S3Bucket: "bucket", S3Region: "x1", Size: "1Gi"}, wantErr: "describe a volume"},
		{name: "s3 without a bucket", flags: ObjectStoreFlags{Mode: "s3", S3Region: "us-east-1"}, wantErr: "bucket"},
		{name: "s3 without region or endpoint", flags: ObjectStoreFlags{Mode: "s3", S3Bucket: "bucket"}, wantErr: "region (AWS) or an endpoint"},
		{name: "bad size", flags: ObjectStoreFlags{Mode: "filesystem", Size: "lots"}, wantErr: "quantity"},
		{name: "zero size", flags: ObjectStoreFlags{Mode: "filesystem", Size: "0"}, wantErr: "greater than zero"},
		{name: "bad access mode", flags: ObjectStoreFlags{Mode: "filesystem", AccessMode: "ReadOnlyMany"}, wantErr: "access mode"},
		{name: "bad storage class", flags: ObjectStoreFlags{Mode: "filesystem", StorageClass: "Not Valid"}, wantErr: "storage class"},
		{name: "endpoint with credentials in the URL", flags: ObjectStoreFlags{Mode: "s3", S3Bucket: "bucket",
			S3Endpoint: "https://ZZUSERINFOZZ:ZZPASSZZ@minio.example.com"}, wantErr: "credentials in its URL"},
		{name: "endpoint that is not a URL", flags: ObjectStoreFlags{Mode: "s3", S3Bucket: "bucket", S3Endpoint: "minio:9000"}, wantErr: "http:// or https://"},
		{name: "a key pasted as the credentials reference", flags: ObjectStoreFlags{Mode: "s3", S3Bucket: "bucket",
			S3Region: "us-east-1", S3Credentials: "ZZKEYZZAKIAIOSFODNN7"}, wantErr: "<namespace>/<name>"},
		{name: "a key=value pasted as the credentials reference", flags: ObjectStoreFlags{Mode: "s3", S3Bucket: "bucket",
			S3Region: "us-east-1", S3Credentials: "ns/ZZKEYZZ=abc"}, wantErr: "not a valid Kubernetes name"},
		{name: "endpoint with a query", flags: ObjectStoreFlags{Mode: "s3", S3Bucket: "bucket", S3Endpoint: "https://minio.example.com?X-Amz-Signature=ZZSIGZZ"}, wantErr: "without a query or fragment"},
		{name: "endpoint with a fragment", flags: ObjectStoreFlags{Mode: "s3", S3Bucket: "bucket", S3Endpoint: "https://minio.example.com#ZZFRAGZZ"}, wantErr: "without a query or fragment"},
		{name: "s3 on a dcctl-created cluster needs a credentials Secret", local: true,
			flags: ObjectStoreFlags{Mode: "s3", S3Bucket: "bucket", S3Region: "us-east-1"}, wantErr: "needs --object-store-s3-credentials-secret"},
		{name: "bucket with uppercase", flags: ObjectStoreFlags{Mode: "s3", S3Bucket: "My_Bucket", S3Region: "us-east-1"}, wantErr: "valid bucket name"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := ResolveObjectStore(c.flags, c.local)
			if c.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), c.wantErr) {
					t.Fatalf("err = %v, want one containing %q (got %+v)", err, c.wantErr, got)
				}
				for _, leaked := range []string{"ZZUSERINFOZZ", "ZZPASSZZ", "ZZKEYZZ"} {
					if strings.Contains(err.Error(), leaked) {
						t.Errorf("the refusal echoes the value it refused (%s): %v", leaked, err)
					}
				}
				return
			}
			if err != nil {
				t.Fatalf("refused: %v", err)
			}
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("got %+v, want %+v", got, c.want)
			}
		})
	}
}

func TestOnlyADcctlCreatedClusterDefaultsToAStore(t *testing.T) {
	if !DefaultsToFilesystemObjectStore("local", ClusterBinding{Managed: true}) {
		t.Error("a kind cluster dcctl created should default to a filesystem store")
	}
	if DefaultsToFilesystemObjectStore("local", ClusterBinding{Managed: false}) {
		t.Error("a cluster dcctl was pointed at (--kube-context) must default to no store")
	}
	if DefaultsToFilesystemObjectStore("gke", ClusterBinding{Managed: true}) {
		t.Error("only the local provider's cluster has the default")
	}
}

// A declaration read back from the cluster is judged by the same sentences, so a
// hand-edited one is refused as the flags are.
func TestADeclarationWithAnUnusableObjectStoreIsRefused(t *testing.T) {
	good := func(os *dcv1beta1.ObjectStoreSpec) dcv1beta1.InstanceSpec {
		return dcv1beta1.InstanceSpec{Provider: "local", Managed: true, ObjectStore: os}
	}
	if err := ValidateInstanceSpec(good(fsStore())); err != nil {
		t.Fatalf("a good filesystem store was refused: %v", err)
	}
	if err := ValidateInstanceSpec(good(s3Store())); err != nil {
		t.Fatalf("a good s3 store was refused: %v", err)
	}
	for name, bad := range map[string]*dcv1beta1.ObjectStoreSpec{
		"s3 without its block":     {Backend: "s3"},
		"filesystem with s3":       {Backend: "filesystem", Size: "1Gi", S3: &dcv1beta1.S3ObjectStoreSpec{Bucket: "bucket"}},
		"s3 with a volume":         {Backend: "s3", Size: "1Gi", S3: &dcv1beta1.S3ObjectStoreSpec{Bucket: "bucket", Region: "us-east-1"}},
		"filesystem without size":  {Backend: "filesystem"},
		"unknown backend":          {Backend: "azure"},
		"credentials in namespace": {Backend: "s3", S3: &dcv1beta1.S3ObjectStoreSpec{Bucket: "bucket", Region: "x1", CredentialsSecret: &dcv1beta1.SecretReference{Namespace: "Bad NS", Name: "n"}}},
	} {
		if err := ValidateInstanceSpec(good(bad)); err == nil {
			t.Errorf("%s: an unusable object store was accepted", name)
		}
	}
}

func TestARerunMayNotMoveAStoreBetweenBackends(t *testing.T) {
	base := func(os *dcv1beta1.ObjectStoreSpec) dcv1beta1.InstanceSpec {
		return dcv1beta1.InstanceSpec{Provider: "local", Cluster: "c", Managed: true, ObjectStore: os}
	}
	if err := ValidateInstanceSpecChange(base(fsStore()), base(s3Store())); err == nil ||
		!strings.Contains(err.Error(), "Nothing moves the objects") {
		t.Errorf("filesystem to s3 was allowed: %v", err)
	}
	// The counterweights: adding a store, resizing one and keeping one are ordinary.
	if err := ValidateInstanceSpecChange(base(nil), base(fsStore())); err != nil {
		t.Errorf("adding a store to an instance that had none was refused: %v", err)
	}
	bigger := fsStore()
	bigger.Size = "10Gi"
	if err := ValidateInstanceSpecChange(base(fsStore()), base(bigger)); err != nil {
		t.Errorf("resizing the store was refused: %v", err)
	}
	if err := ValidateInstanceSpecChange(base(s3Store()), base(s3Store())); err != nil {
		t.Errorf("an unchanged store was refused: %v", err)
	}
}

// --- persistence: the declaration is how a re-run keeps the store -------------------

func TestTheStoreSurvivesTheDeclarationRoundTrip(t *testing.T) {
	for name, store := range map[string]*dcv1beta1.ObjectStoreSpec{"none": nil, "filesystem": fsStore(), "s3": s3Store()} {
		t.Run(name, func(t *testing.T) {
			bootstrapSt := &State{Instance: "dc1", Profile: "default", ObjectStore: store}
			spec := InstanceSpecFrom(bootstrapSt, ClusterBinding{Managed: true}, "local")

			// Through the wire form the cluster stores it in.
			raw, err := json.Marshal(spec)
			if err != nil {
				t.Fatal(err)
			}
			var back dcv1beta1.InstanceSpec
			if err := json.Unmarshal(raw, &back); err != nil {
				t.Fatal(err)
			}
			if store == nil && strings.Contains(string(raw), "objectStore") {
				t.Errorf("an instance with no store wrote an objectStore block: %s", raw)
			}

			upgradeSt := &State{Instance: "dc1"}
			applyDeclaration(upgradeSt, back)
			if !reflect.DeepEqual(upgradeSt.ObjectStore, store) {
				t.Fatalf("the store did not survive: got %+v, want %+v", upgradeSt.ObjectStore, store)
			}
			// ...and what the chart is handed is the same either side of the round trip.
			b1, s1 := objectStoreChartValues(store, "dc1", false)
			b2, s2 := objectStoreChartValues(upgradeSt.ObjectStore, "dc1", false)
			if !reflect.DeepEqual(b1, b2) || !reflect.DeepEqual(s1, s2) {
				t.Errorf("a re-run renders a different store than the bootstrap did")
			}
		})
	}
}

// The declaration is cluster-scoped and widely readable. No credential may reach it.
func TestTheDeclarationNamesTheCredentialsSecretAndNeverItsContents(t *testing.T) {
	spec := InstanceSpecFrom(&State{Instance: "dc1", ObjectStore: s3Store()}, ClusterBinding{}, "local")
	raw, _ := json.Marshal(spec)
	for _, banned := range []string{awsAccessKeyID, awsSecretAccessKey, "AKIA"} {
		if strings.Contains(string(raw), banned) {
			t.Errorf("the declaration contains %q: %s", banned, raw)
		}
	}
	if !strings.Contains(string(raw), `"name":"minio-keys"`) {
		t.Errorf("the declaration does not name the Secret: %s", raw)
	}
}

// --- the composed config and the chart values, per mode ----------------------------

type composedStore struct {
	vals     map[string]interface{}
	doc      string
	manifest string
}

func composeStore(t *testing.T, store *dcv1beta1.ObjectStoreSpec) composedStore {
	t.Helper()
	st := profileState(false, false, "default")("dc-os")
	st.ObjectStore = store
	ch := chartForTest(t)
	vals, doc, err := releaseValues(t.Context(), ch, st, nil)
	if err != nil {
		t.Fatalf("composing the release values: %v", err)
	}
	manifest, err := renderChartClientSide(t.Context(), ch, vals)
	if err != nil {
		t.Fatalf("rendering the install values: %v", err)
	}
	return composedStore{vals: vals, doc: string(doc), manifest: manifest}
}

func (c composedStore) deployment(t *testing.T, area string) appsv1.Deployment {
	t.Helper()
	for _, d := range splitManifests(c.manifest) {
		var dep appsv1.Deployment
		if err := yaml.Unmarshal([]byte(d), &dep); err != nil || dep.Kind != "Deployment" {
			continue
		}
		if dep.Labels["devicechain.io/functional-area"] == area {
			return dep
		}
	}
	t.Fatalf("no Deployment for %s in the render", area)
	return appsv1.Deployment{}
}

func (c composedStore) pvc(t *testing.T) *corev1.PersistentVolumeClaim {
	t.Helper()
	for _, d := range splitManifests(c.manifest) {
		var pvc corev1.PersistentVolumeClaim
		if err := yaml.Unmarshal([]byte(d), &pvc); err == nil && pvc.Kind == "PersistentVolumeClaim" &&
			pvc.Name == "dci-dc-os-blob" {
			return &pvc
		}
	}
	return nil
}

func envOf(dep appsv1.Deployment, name string) *corev1.EnvVar {
	for _, e := range dep.Spec.Template.Spec.Containers[0].Env {
		if e.Name == name {
			e := e
			return &e
		}
	}
	return nil
}

func mountOf(dep appsv1.Deployment, name string) *corev1.VolumeMount {
	for _, m := range dep.Spec.Template.Spec.Containers[0].VolumeMounts {
		if m.Name == name {
			m := m
			return &m
		}
	}
	return nil
}

func TestNoStoreComposesNoStore(t *testing.T) {
	t.Parallel()
	c := composeStore(t, nil)
	if _, ok := c.vals["blobStorage"]; ok {
		t.Errorf("an instance with no store set blobStorage: %v", c.vals["blobStorage"])
	}
	loaded, err := loadInstanceConfigDocument([]byte(c.doc))
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Infrastructure.Blob.Directory != "" || loaded.Infrastructure.Blob.Bucket != "" {
		t.Errorf("the document names a store: %+v", loaded.Infrastructure.Blob)
	}
	if c.pvc(t) != nil {
		t.Error("a volume was rendered for an instance with no store")
	}
	if mountOf(c.deployment(t, "user-management"), "blob-store") != nil {
		t.Error("user-management mounts a store volume that was never asked for")
	}
}

func TestAFilesystemStoreComposesTheDocumentTheVolumeAndTheMount(t *testing.T) {
	t.Parallel()
	c := composeStore(t, fsStore())

	loaded, err := loadInstanceConfigDocument([]byte(c.doc))
	if err != nil {
		t.Fatal(err)
	}
	blob := loaded.Infrastructure.Blob
	if blob.Backend != "filesystem" || blob.Directory != objectStoreDirectory {
		t.Errorf("the document's store = %+v, want filesystem at %s", blob, objectStoreDirectory)
	}

	// The chart can no longer read the directory out of the document it does not hold, so
	// dcctl must restate where to mount it.
	bs, _ := c.vals["blobStorage"].(map[string]interface{})
	p, _ := bs["persistence"].(map[string]interface{})
	if p["mountPath"] != objectStoreDirectory {
		t.Errorf("blobStorage.persistence.mountPath = %v, want %s", p["mountPath"], objectStoreDirectory)
	}
	if _, stillThere := c.vals["instance"].(map[string]interface{})["config"]; stillThere {
		t.Error("instance.config is still in the install values")
	}

	pvc := c.pvc(t)
	if pvc == nil {
		t.Fatal("no claim was rendered")
	}
	if got := pvc.Spec.Resources.Requests.Storage().String(); got != "2Gi" {
		t.Errorf("claim size = %s, want 2Gi", got)
	}
	if pvc.Spec.StorageClassName == nil || *pvc.Spec.StorageClassName != "fast" {
		t.Errorf("claim storage class = %v, want fast", pvc.Spec.StorageClassName)
	}
	if len(pvc.Spec.AccessModes) != 1 || pvc.Spec.AccessModes[0] != corev1.ReadWriteMany {
		t.Errorf("claim access modes = %v, want ReadWriteMany", pvc.Spec.AccessModes)
	}
	m := mountOf(c.deployment(t, "user-management"), "blob-store")
	if m == nil || m.MountPath != objectStoreDirectory {
		t.Errorf("user-management mount = %+v, want the store at %s", m, objectStoreDirectory)
	}
}

// The default local store: nothing but a size.
func TestTheDefaultLocalStoreIsASmallClaimOnTheDefaultClass(t *testing.T) {
	t.Parallel()
	store, err := ResolveObjectStore(ObjectStoreFlags{}, true)
	if err != nil {
		t.Fatal(err)
	}
	c := composeStore(t, store)
	pvc := c.pvc(t)
	if pvc == nil {
		t.Fatal("no claim was rendered")
	}
	if got := pvc.Spec.Resources.Requests.Storage().String(); got != DefaultLocalObjectStoreSize {
		t.Errorf("claim size = %s, want %s", got, DefaultLocalObjectStoreSize)
	}
	if pvc.Spec.StorageClassName != nil {
		t.Errorf("the default store pinned a storage class: %v", *pvc.Spec.StorageClassName)
	}
}

func TestAnS3StoreComposesTheDocumentAndProjectsTheCredentialsFromAnOwnedSecret(t *testing.T) {
	t.Parallel()
	c := composeStore(t, s3Store())

	loaded, err := loadInstanceConfigDocument([]byte(c.doc))
	if err != nil {
		t.Fatal(err)
	}
	blob := loaded.Infrastructure.Blob
	if blob.Backend != "s3" || blob.Bucket != "dc-objects" || blob.Region != "eu-west-1" ||
		blob.Endpoint != "https://minio.example.com:9000" || !blob.UsePathStyle || blob.Directory != "" {
		t.Errorf("the document's store = %+v", blob)
	}
	if c.pvc(t) != nil {
		t.Error("an s3 store rendered a volume")
	}
	dep := c.deployment(t, "user-management")
	if mountOf(dep, "blob-store") != nil {
		t.Error("an s3 store mounted a volume")
	}
	for _, key := range []string{awsAccessKeyID, awsSecretAccessKey} {
		e := envOf(dep, key)
		if e == nil || e.ValueFrom == nil || e.ValueFrom.SecretKeyRef == nil {
			t.Fatalf("%s is not projected from a Secret: %+v", key, e)
		}
		if e.ValueFrom.SecretKeyRef.Name != "dci-dc-os-blob-s3" || e.ValueFrom.SecretKeyRef.Key != key {
			t.Errorf("%s comes from %+v, want dci-dc-os-blob-s3/%s", key, e.ValueFrom.SecretKeyRef, key)
		}
	}
	// Only the areas that use the store get the credentials.
	if e := envOf(c.deployment(t, "device-management"), awsAccessKeyID); e != nil {
		t.Error("an area that does not use the object store was handed the S3 credentials")
	}
	// Nothing credential-shaped is in the document or the values.
	for _, banned := range []string{awsSecretAccessKey, "minio-keys"} {
		if strings.Contains(c.doc, banned) {
			t.Errorf("the instance document contains %q", banned)
		}
	}
}

func TestAnS3StoreOnThePodIdentityProjectsNothing(t *testing.T) {
	t.Parallel()
	store := s3Store()
	store.S3.CredentialsSecret = nil
	c := composeStore(t, store)
	if e := envOf(c.deployment(t, "user-management"), awsAccessKeyID); e != nil {
		t.Errorf("credentials were projected with no Secret named: %+v", e)
	}
}

// --- the credentials copy ------------------------------------------------------------

func credSource(data map[string][]byte) *corev1.Secret {
	return &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "storage", Name: "minio-keys"}, Data: data}
}

func TestTheCredentialsAreCopiedIntoTheInstanceNamespaceAsAnOwnedSecret(t *testing.T) {
	src := credSource(map[string][]byte{
		awsAccessKeyID: []byte("AKIAEXAMPLE"), awsSecretAccessKey: []byte("s3cr3t"),
		"AWS_SESSION_TOKEN": []byte("not forwarded"), "unrelated": []byte("x"),
	})
	c := fake.NewSimpleClientset(src)
	st := &State{Instance: "acme", InstanceUID: testUID, ObjectStore: s3Store()}

	if err := precheckObjectStoreWith(t.Context(), c, st); err != nil {
		t.Fatalf("precheck refused a good Secret: %v", err)
	}
	if _, err := syncObjectStoreCredentials(t.Context(), c, st); err != nil {
		t.Fatalf("copy: %v", err)
	}
	got, err := c.CoreV1().Secrets(InstanceNamespace("acme")).Get(t.Context(), "dci-acme-blob-s3", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got.Annotations[annotationManagedBy] != managedByDcctl {
		t.Error("the copy carries no dcctl ownership, so a re-run could not update it")
	}
	flat := map[string]string{}
	for k, v := range got.StringData {
		flat[k] = v
	}
	for k, v := range got.Data {
		flat[k] = string(v)
	}
	if len(flat) != 2 || flat[awsAccessKeyID] != "AKIAEXAMPLE" || flat[awsSecretAccessKey] != "s3cr3t" {
		t.Errorf("the copy holds %v, want exactly the two credential keys", keysOf(flat))
	}

	// A rotation of the source reaches the copy on the next run.
	src.Data[awsSecretAccessKey] = []byte("rotated")
	if _, err := c.CoreV1().Secrets("storage").Update(t.Context(), src, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := syncObjectStoreCredentials(t.Context(), c, st); err != nil {
		t.Fatalf("re-copy: %v", err)
	}
	got, _ = c.CoreV1().Secrets(InstanceNamespace("acme")).Get(t.Context(), "dci-acme-blob-s3", metav1.GetOptions{})
	if v := got.StringData[awsSecretAccessKey] + string(got.Data[awsSecretAccessKey]); v != "rotated" {
		t.Errorf("the rotated key did not reach the copy: %q", v)
	}
}

func keysOf(m map[string]string) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestACredentialsSecretThatCannotServeIsRefused(t *testing.T) {
	st := &State{Instance: "acme", InstanceUID: testUID, ObjectStore: s3Store()}
	cases := map[string]struct {
		secret  *corev1.Secret
		wantErr string
	}{
		"absent":              {nil, "does not exist"},
		"no secret key":       {credSource(map[string][]byte{awsAccessKeyID: []byte("a")}), awsSecretAccessKey},
		"empty access key":    {credSource(map[string][]byte{awsAccessKeyID: []byte("  "), awsSecretAccessKey: []byte("b")}), awsAccessKeyID},
		"keys under bad name": {credSource(map[string][]byte{"access": []byte("a"), "secret": []byte("b")}), awsAccessKeyID},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			client := fake.NewSimpleClientset()
			if c.secret != nil {
				client = fake.NewSimpleClientset(c.secret)
			}
			_, err := syncObjectStoreCredentials(t.Context(), client, st)
			if err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Fatalf("err = %v, want one containing %q", err, c.wantErr)
			}
			if c.secret != nil && (strings.Contains(err.Error(), "s3cr3t") || strings.Contains(err.Error(), "AKIA")) {
				t.Errorf("the refusal prints a credential value: %v", err)
			}
			if _, gerr := client.CoreV1().Secrets(InstanceNamespace("acme")).Get(t.Context(), "dci-acme-blob-s3", metav1.GetOptions{}); gerr == nil {
				t.Error("a copy was written despite the refusal")
			}
		})
	}
}

func TestACredentialsSecretInTheInstanceNamespaceIsRefused(t *testing.T) {
	store := s3Store()
	store.S3.CredentialsSecret = &dcv1beta1.SecretReference{Namespace: InstanceNamespace("acme"), Name: "keys"}
	_, err := syncObjectStoreCredentials(t.Context(), fake.NewSimpleClientset(),
		&State{Instance: "acme", InstanceUID: testUID, ObjectStore: store})
	if err == nil || !strings.Contains(err.Error(), "own namespace") {
		t.Errorf("err = %v", err)
	}
}

func TestOtherStoresCopyNothing(t *testing.T) {
	for name, store := range map[string]*dcv1beta1.ObjectStoreSpec{"none": nil, "filesystem": fsStore(),
		"s3 on pod identity": {Backend: "s3", S3: &dcv1beta1.S3ObjectStoreSpec{Bucket: "bucket", Region: "us-east-1"}}} {
		c := fake.NewSimpleClientset()
		if _, err := syncObjectStoreCredentials(context.Background(), c, &State{Instance: "acme", ObjectStore: store}); err != nil {
			t.Errorf("%s: %v", name, err)
		}
		if len(c.Actions()) != 0 {
			t.Errorf("%s: touched the cluster: %v", name, c.Actions())
		}
	}
}
