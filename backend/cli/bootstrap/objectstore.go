// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	dcv1beta1 "github.com/devicechain-io/dc-k8s/api/v1beta1"
	"helm.sh/helm/v3/pkg/releaseutil"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/yaml"
)

// The instance's object store: where tenant branding logos and update artifacts live.
//
// 🔴 IT IS FIXED CONFIGURATION. The services read it from the mounted instance document
// and cannot change it while running, so it is chosen here, recorded on the Instance
// declaration (so `dcctl upgrade` and a second operator converge to the same store), and
// adopted by rolling the pods. None of it is a runtime setting.
//
// 🔴 AND NOTHING SECRET IS EVER A VALUE IN IT. An S3 access key is not a flag, not a field
// of the declaration and not in the instance document: the operator names an EXISTING
// Secret, dcctl reads it from the cluster API and copies the two keys into a Secret it owns
// in the instance's namespace, and the chart projects that Secret into the pods' environment
// where the AWS SDK's own credential chain finds it.
const (
	// objectStoreDirectory is where the filesystem store is mounted. Any absolute path would
	// do (the chart mounts the volume exactly where the document says to write); it is a
	// constant so that every instance dcctl builds agrees, and so the document and the
	// volume cannot name two places.
	objectStoreDirectory = "/var/lib/devicechain/blob"

	// DefaultLocalObjectStoreSize is the filesystem store's size when none is given. It is the
	// chart's own default for blobStorage.persistence.size, and TestTheDefaultStoreSizeIsTheChartsOwn
	// holds the two equal: a second opinion on how big a store is would drift. 5Gi is room for a
	// few firmware images on a developer machine.
	DefaultLocalObjectStoreSize = "5Gi"

	// ObjectStoreNone, the flag's spelling of "no store". It is never a value in the
	// declaration: an instance with no store has no objectStore block at all.
	ObjectStoreNone = "none"

	// The keys the credentials Secret must carry, named by the AWS SDK's credential chain.
	awsAccessKeyID     = "AWS_ACCESS_KEY_ID"
	awsSecretAccessKey = "AWS_SECRET_ACCESS_KEY"
)

var (
	// The S3 naming rules, restated: 3-63 characters of lowercase letters, digits, dots and
	// hyphens, starting and ending alphanumeric.
	s3BucketPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$`)
	s3RegionPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,30}[a-z0-9]$`)

	objectStoreAccessModes = []string{"ReadWriteOnce", "ReadWriteMany"}
)

// ObjectStoreFlags are the bootstrap flags that choose the object store, as written. A
// zero field is an unset flag, which is how "was this given" is told apart from "what was
// it given" without a second set of booleans to keep in step.
type ObjectStoreFlags struct {
	// Mode is --object-store: none, filesystem or s3. Empty means the provider's default.
	Mode string

	// Filesystem settings.
	Size         string
	StorageClass string
	AccessMode   string

	// S3 settings.
	S3Bucket      string
	S3Region      string
	S3Endpoint    string
	S3PathStyle   bool
	S3Credentials string // "<namespace>/<name>" of an existing Secret
}

func (f ObjectStoreFlags) givesFilesystemDetail() bool {
	return f.Size != "" || f.StorageClass != "" || f.AccessMode != ""
}

func (f ObjectStoreFlags) givesS3Detail() bool {
	return f.S3Bucket != "" || f.S3Region != "" || f.S3Endpoint != "" || f.S3PathStyle || f.S3Credentials != ""
}

// DefaultsToFilesystemObjectStore says whether an unset --object-store means a small
// filesystem store: on a cluster dcctl created itself (a kind cluster), where a store with
// nothing to configure is what makes branding logos and update artifacts work out of the
// box. On a cluster dcctl was only pointed at there is no default: whether a volume of the
// right class exists, or a bucket is wanted instead, is the operator's call, and an unset
// flag there means no store -- the same as before this flag existed.
func DefaultsToFilesystemObjectStore(provider string, binding ClusterBinding) bool {
	return provider == "local" && binding.Managed
}

// ResolveObjectStore turns the flags into the declaration's block, or nil for no store.
//
// 🔴 EVERY COMBINATION IT CANNOT HONOUR IS REFUSED, NEVER REINTERPRETED. A bucket given
// with --object-store filesystem, a volume size given with s3, any detail given with
// none: each is a flag that would be silently ignored, and a flag the instance does not
// obey is a declaration that is wrong about it.
func ResolveObjectStore(f ObjectStoreFlags, managedCluster bool) (*dcv1beta1.ObjectStoreSpec, error) {
	mode := f.Mode
	if mode == "" {
		switch {
		case managedCluster && f.givesS3Detail():
			return nil, fmt.Errorf("--object-store-s3-* flags need --object-store s3: without it this " +
				"cluster defaults to a filesystem store, which they do not configure")
		case managedCluster:
			mode = dcv1beta1.ObjectStoreFilesystem
		case f.givesFilesystemDetail() || f.givesS3Detail():
			return nil, fmt.Errorf("--object-store-* flags need --object-store filesystem or s3: on this " +
				"cluster the default is no object store, so there is nothing for them to configure")
		default:
			return nil, nil
		}
	}

	var spec *dcv1beta1.ObjectStoreSpec
	switch mode {
	case ObjectStoreNone:
		if f.givesFilesystemDetail() || f.givesS3Detail() {
			return nil, fmt.Errorf("--object-store none takes no other --object-store-* flag: they " +
				"would be silently ignored")
		}
		return nil, nil
	case dcv1beta1.ObjectStoreFilesystem:
		if f.givesS3Detail() {
			return nil, fmt.Errorf("--object-store-s3-* flags configure the s3 backend and would be " +
				"ignored by --object-store filesystem")
		}
		size := f.Size
		if size == "" {
			size = DefaultLocalObjectStoreSize
		}
		spec = &dcv1beta1.ObjectStoreSpec{
			Backend:      dcv1beta1.ObjectStoreFilesystem,
			Size:         size,
			StorageClass: f.StorageClass,
			AccessMode:   f.AccessMode,
		}
	case dcv1beta1.ObjectStoreS3:
		if f.givesFilesystemDetail() {
			return nil, fmt.Errorf("--object-store-size, --object-store-storage-class and " +
				"--object-store-access-mode describe a volume and would be ignored by --object-store s3")
		}
		s3 := &dcv1beta1.S3ObjectStoreSpec{
			Bucket: f.S3Bucket, Region: f.S3Region, Endpoint: f.S3Endpoint, UsePathStyle: f.S3PathStyle,
		}
		if f.S3Credentials != "" {
			ref, err := parseSecretReference(f.S3Credentials)
			if err != nil {
				return nil, fmt.Errorf("--object-store-s3-credentials-secret: %w", err)
			}
			s3.CredentialsSecret = ref
		}
		spec = &dcv1beta1.ObjectStoreSpec{Backend: dcv1beta1.ObjectStoreS3, S3: s3}
	default:
		return nil, fmt.Errorf("--object-store %q is not one of none, filesystem or s3", mode)
	}
	if err := ValidateObjectStore(spec); err != nil {
		return nil, err
	}
	// 🔴 A CLUSTER DCCTL CREATED HAS NO AWS IDENTITY TO FALL BACK ON. The pod-identity path
	// (an instance profile, a workload-identity binding on the ServiceAccount) is a cloud
	// cluster's; on a kind cluster the chain finds nothing and the store fails at its first
	// write, long after the bootstrap said it was ready. Refused here, where the fix is a flag.
	if managedCluster && spec.Backend == dcv1beta1.ObjectStoreS3 && spec.S3.CredentialsSecret == nil {
		return nil, fmt.Errorf("--object-store s3 on a cluster dcctl created needs " +
			"--object-store-s3-credentials-secret: there is no cloud identity for the pods to use here, " +
			"so without a Secret the store would fail at its first write")
	}
	return spec, nil
}

// parseSecretReference reads "<namespace>/<name>".
//
// 🔴 NO REFUSAL BELOW ECHOES THE VALUE IT REFUSES. A key pasted into this flag by mistake is
// exactly the input that fails the name rules, and an error that quotes it prints the
// credential into a terminal, a CI log and a shell history. The sentences say what the shape
// must be, never what it was.
func parseSecretReference(s string) (*dcv1beta1.SecretReference, error) {
	ns, name, ok := strings.Cut(s, "/")
	if !ok || ns == "" || name == "" {
		return nil, fmt.Errorf("must be <namespace>/<name> of an existing Secret (it names the Secret; " +
			"it is never the credential itself)")
	}
	return &dcv1beta1.SecretReference{Namespace: ns, Name: name}, nil
}

// ValidateObjectStore judges a block on its own. It is the one definition of "a usable
// store", run over the flags' result and over a declaration read back from the cluster, so
// a hand-edited declaration is refused by the same sentences.
func ValidateObjectStore(spec *dcv1beta1.ObjectStoreSpec) error {
	if spec == nil {
		return nil
	}
	switch spec.Backend {
	case dcv1beta1.ObjectStoreFilesystem:
		if spec.S3 != nil {
			return fmt.Errorf("the object store is a filesystem store and also carries s3 settings")
		}
		q, err := resource.ParseQuantity(spec.Size)
		if err != nil || spec.Size == "" {
			return fmt.Errorf("the object store size %q is not a Kubernetes quantity such as 1Gi", spec.Size)
		}
		if q.Sign() <= 0 {
			return fmt.Errorf("the object store size %q must be greater than zero", spec.Size)
		}
		if spec.StorageClass != "" && spec.StorageClass != "-" {
			if errs := validation.IsDNS1123Subdomain(spec.StorageClass); len(errs) > 0 {
				return fmt.Errorf("the object store storage class %q is not a valid name: %s",
					spec.StorageClass, strings.Join(errs, "; "))
			}
		}
		if spec.AccessMode != "" && !slices.Contains(objectStoreAccessModes, spec.AccessMode) {
			return fmt.Errorf("the object store access mode %q is not one of %s",
				spec.AccessMode, strings.Join(objectStoreAccessModes, ", "))
		}
	case dcv1beta1.ObjectStoreS3:
		if spec.Size != "" || spec.StorageClass != "" || spec.AccessMode != "" {
			return fmt.Errorf("the object store is an s3 store and also carries volume settings")
		}
		s3 := spec.S3
		if s3 == nil {
			return fmt.Errorf("the object store is s3 and names no bucket")
		}
		if !s3BucketPattern.MatchString(s3.Bucket) {
			return fmt.Errorf("the s3 bucket is not a valid bucket name (3 to 63 lowercase letters, " +
				"digits, dots and hyphens)")
		}
		if s3.Region == "" && s3.Endpoint == "" {
			return fmt.Errorf("an s3 store needs a region (AWS) or an endpoint (S3-compatible service)")
		}
		if s3.Region != "" && !s3RegionPattern.MatchString(s3.Region) {
			return fmt.Errorf("the s3 region is not a region name")
		}
		if s3.Endpoint != "" {
			if err := validateS3Endpoint(s3.Endpoint); err != nil {
				return err
			}
		}
		if ref := s3.CredentialsSecret; ref != nil {
			if len(validation.IsDNS1123Label(ref.Namespace)) > 0 {
				return fmt.Errorf("the s3 credentials Secret's namespace is not a valid Kubernetes name")
			}
			if len(validation.IsDNS1123Subdomain(ref.Name)) > 0 {
				return fmt.Errorf("the s3 credentials Secret's name is not a valid Kubernetes name")
			}
		}
	default:
		return fmt.Errorf("the object store backend %q is not one of %s or %s",
			spec.Backend, dcv1beta1.ObjectStoreFilesystem, dcv1beta1.ObjectStoreS3)
	}
	return nil
}

// validateS3Endpoint refuses what cannot be an endpoint, and in particular what carries a
// credential: the endpoint lands in the instance document and in the declaration, both of
// which are readable well beyond the Secret a key belongs in. It never prints the endpoint
// it was given, because the endpoint is where a credential would be.
func validateS3Endpoint(endpoint string) error {
	u, err := url.Parse(endpoint)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return fmt.Errorf("the s3 endpoint is not an http:// or https:// URL")
	}
	if u.User != nil {
		return fmt.Errorf("the s3 endpoint carries credentials in its URL; it is written into the instance " +
			"configuration and the declaration, so a credential belongs in the Secret --object-store-s3-" +
			"credentials-secret names")
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("the s3 endpoint must be a bare URL, without a query or fragment (a query is where " +
			"a signed credential would travel)")
	}
	return nil
}

// objectStoreChartValues are the two chart value blocks the store occupies: the
// instance document's infrastructure.blob (what the SERVICES read) and the top-level
// blobStorage (what the CHART does about it: the volume, the credentials projection).
//
// persistence.mountPath is not among them, and that is the point of installValuesFor
// restating it: the chart reads the mount path off the document this block authors, and
// stops being able to once the document lives in a Secret.
func objectStoreChartValues(spec *dcv1beta1.ObjectStoreSpec, instance string, colocate bool) (blob, storage map[string]interface{}) {
	if spec == nil {
		return nil, nil
	}
	switch spec.Backend {
	case dcv1beta1.ObjectStoreFilesystem:
		persistence := map[string]interface{}{
			"enabled": true,
			"size":    spec.Size,
		}
		if spec.StorageClass != "" {
			persistence["storageClass"] = spec.StorageClass
		}
		if spec.AccessMode != "" {
			persistence["accessModes"] = []interface{}{spec.AccessMode}
		}
		// Every pod that mounts the claim on one node, for a ReadWriteOnce claim that more
		// than one pod mounts. Only dcctl's own single-node cluster asks for it: on a
		// multi-node cluster pinning pods to one node is the wrong answer to a volume that
		// cannot be shared, and ReadWriteMany or s3 is the right one.
		if colocate && spec.AccessMode != "ReadWriteMany" {
			persistence["colocate"] = true
		}
		return map[string]interface{}{
				"backend":   dcv1beta1.ObjectStoreFilesystem,
				"directory": objectStoreDirectory,
			},
			map[string]interface{}{"persistence": persistence}
	case dcv1beta1.ObjectStoreS3:
		s3 := spec.S3
		blob = map[string]interface{}{
			"backend": dcv1beta1.ObjectStoreS3,
			"bucket":  s3.Bucket,
		}
		if s3.Region != "" {
			blob["region"] = s3.Region
		}
		if s3.Endpoint != "" {
			blob["endpoint"] = s3.Endpoint
		}
		if s3.UsePathStyle {
			blob["usePathStyle"] = true
		}
		storage = map[string]interface{}{}
		if s3.CredentialsSecret != nil {
			storage["s3"] = map[string]interface{}{"credentialsSecret": objectStoreCredentialsSecretName(instance)}
		}
		return blob, storage
	}
	return nil, nil
}

// describeObjectStore is the one-line resolution printed in the bootstrap report. It
// states what was chosen and nothing secret: a Secret is named, never read back.
func describeObjectStore(spec *dcv1beta1.ObjectStoreSpec) string {
	switch {
	case spec == nil:
		return "none (artifact upload and logo upload are refused until one is configured)"
	case spec.Backend == dcv1beta1.ObjectStoreFilesystem:
		class := spec.StorageClass
		if class == "" {
			class = "default storage class"
		}
		mode := spec.AccessMode
		if mode == "" {
			mode = "ReadWriteOnce"
		}
		return fmt.Sprintf("filesystem volume, %s, %s, %s", spec.Size, class, mode)
	default:
		s3 := spec.S3
		where := s3.Region
		if s3.Endpoint != "" {
			where = s3.Endpoint
		}
		creds := "pod identity (no credentials Secret)"
		if s3.CredentialsSecret != nil {
			creds = fmt.Sprintf("credentials from Secret %s/%s", s3.CredentialsSecret.Namespace, s3.CredentialsSecret.Name)
		}
		return fmt.Sprintf("s3 bucket %s (%s), %s", s3.Bucket, where, creds)
	}
}

// ObjectStoreSummary is describeObjectStore for the command layer.
func ObjectStoreSummary(spec *dcv1beta1.ObjectStoreSpec) string { return describeObjectStore(spec) }

// objectStoreCredentialsSecretName is the Secret dcctl copies the credentials into, in the
// instance's namespace. A pod can only reference a Secret in its own namespace, and the
// namespace does not exist before the bootstrap that builds it, so the operator's Secret
// cannot be it.
func objectStoreCredentialsSecretName(instance string) string {
	return fmt.Sprintf("dci-%s-blob-s3", instance)
}

// objectStoreCredentialKeys are the only keys copied. A source Secret that carries more
// (a session token, an unrelated password) does not have it forwarded to the pods.
var objectStoreCredentialKeys = []string{awsAccessKeyID, awsSecretAccessKey}

// readObjectStoreCredentials reads the operator's Secret and returns the two keys the pods
// need, refusing a Secret that cannot supply them. It never prints a value.
func readObjectStoreCredentials(ctx context.Context, typed kubernetes.Interface, instance string,
	ref *dcv1beta1.SecretReference) (map[string]string, error) {
	if ref.Namespace == InstanceNamespace(instance) {
		return nil, fmt.Errorf("the s3 credentials Secret %s/%s is in this instance's own namespace, "+
			"which dcctl creates during the bootstrap, so it cannot exist yet; keep it in another "+
			"namespace and dcctl copies it in", ref.Namespace, ref.Name)
	}
	sec, err := typed.CoreV1().Secrets(ref.Namespace).Get(ctx, ref.Name, metav1.GetOptions{})
	switch {
	case apierrors.IsNotFound(err):
		return nil, fmt.Errorf("the s3 credentials Secret %s/%s does not exist on the cluster; create it "+
			"with keys %s and %s, or leave --object-store-s3-credentials-secret out to use the pods' own "+
			"AWS identity", ref.Namespace, ref.Name, awsAccessKeyID, awsSecretAccessKey)
	case err != nil:
		return nil, fmt.Errorf("reading the s3 credentials Secret %s/%s: %w", ref.Namespace, ref.Name, err)
	}
	out := map[string]string{}
	for _, k := range objectStoreCredentialKeys {
		// ONE trailing newline is trimmed: `kubectl create secret --from-file` and an echo into
		// a file both leave one, and an access key with a newline on its end is a signature
		// that never verifies. Anything else non-printable is refused, not repaired. Neither
		// sentence prints the value.
		v := strings.TrimSuffix(strings.TrimSuffix(string(sec.Data[k]), "\n"), "\r")
		if strings.TrimSpace(v) == "" {
			return nil, fmt.Errorf("the s3 credentials Secret %s/%s has no non-empty key %s (it needs %s and %s)",
				ref.Namespace, ref.Name, k, awsAccessKeyID, awsSecretAccessKey)
		}
		if strings.IndexFunc(v, func(r rune) bool { return r <= ' ' || r == 0x7f }) >= 0 {
			return nil, fmt.Errorf("the s3 credentials Secret %s/%s key %s contains whitespace or a control "+
				"character; remove it from the Secret (a single trailing newline is the only one dcctl removes)",
				ref.Namespace, ref.Name, k)
		}
		out[k] = v
	}
	return out, nil
}

// objectStoreCredentialsDigest is the value the chart stamps on the pods that read the
// credentials, so a rotated key rolls them. A secretKeyRef is read once, at container start,
// and the copy keeps its name when its content changes: without a digest in the pod template a
// same-version upgrade applies cleanly and rolls nothing, leaving every pod on the old key.
// A digest, never the value; the separator keeps ("ab","c") apart from ("a","bc").
func objectStoreCredentialsDigest(creds map[string]string) string {
	h := sha256.New()
	for _, k := range objectStoreCredentialKeys {
		h.Write([]byte(k))
		h.Write([]byte{0})
		h.Write([]byte(creds[k]))
		h.Write([]byte{0})
	}
	return fmt.Sprintf("%x", h.Sum(nil))
}

// precheckObjectStore is the early, read-only half: it refuses a store the cluster cannot
// back before anything is written.
func precheckObjectStore(ctx context.Context, st *State) error {
	if err := checkObjectStoreMountable(ctx, st); err != nil {
		return err
	}
	if !usesCredentialsSecret(st.ObjectStore) {
		return nil
	}
	_, _, typed, err := kubeClients(st.KubeContext)
	if err != nil {
		return fmt.Errorf("connecting to the cluster to check the s3 credentials Secret: %w", err)
	}
	return precheckObjectStoreWith(ctx, typed, st)
}

// precheckObjectStoreWith is precheckObjectStore against a given client.
func precheckObjectStoreWith(ctx context.Context, typed kubernetes.Interface, st *State) error {
	if !usesCredentialsSecret(st.ObjectStore) {
		return nil
	}
	_, err := readObjectStoreCredentials(ctx, typed, st.Instance, st.ObjectStore.S3.CredentialsSecret)
	return err
}

// usesCredentialsSecret says whether the store is an S3 one that names a Secret to copy.
func usesCredentialsSecret(spec *dcv1beta1.ObjectStoreSpec) bool {
	return spec != nil && spec.Backend == dcv1beta1.ObjectStoreS3 && spec.S3 != nil && spec.S3.CredentialsSecret != nil
}

// syncObjectStoreCredentials copies the credentials into the instance namespace, as a
// Secret dcctl owns, and returns the digest the pods are rolled by. It runs on every install
// AND every upgrade, so rotating the source Secret and re-running `dcctl upgrade` is how the
// pods pick a new key up: the digest changes, the pod template with it, and the pods restart.
// The digest is empty when the store has no credentials Secret.
func syncObjectStoreCredentials(ctx context.Context, typed kubernetes.Interface, st *State) (string, error) {
	if !usesCredentialsSecret(st.ObjectStore) {
		return "", nil
	}
	data, err := readObjectStoreCredentials(ctx, typed, st.Instance, st.ObjectStore.S3.CredentialsSecret)
	if err != nil {
		return "", err
	}
	if err := writeOwnedSecret(ctx, typed, instanceOwner(st.Instance, st.InstanceUID), ownedSecret{
		Name:      objectStoreCredentialsSecretName(st.Instance),
		Namespace: InstanceNamespace(st.Instance),
		Type:      corev1.SecretTypeOpaque,
		Labels:    map[string]string{"devicechain.io/instance": st.Instance},
		Data:      data,
	}, time.Now); err != nil {
		return "", err
	}
	return objectStoreCredentialsDigest(data), nil
}

// withObjectStoreCredentialsDigest stamps the digest into the install values. It builds a
// copy of the blobStorage block rather than editing it: vals shares sub-maps with the
// authoring values.
func withObjectStoreCredentialsDigest(vals map[string]interface{}, digest string) {
	if digest == "" {
		return
	}
	bs := mergedStringKeyMap(vals["blobStorage"], map[string]interface{}{})
	bs["s3"] = mergedStringKeyMap(bs["s3"], map[string]interface{}{"credentialsChecksum": digest})
	vals["blobStorage"] = bs
}

// checkObjectStoreMountable refuses, before anything is created, an area set that would put
// more than one pod on a ReadWriteOnce store volume without a way to keep them on one node.
// A ReadWriteOnce volume attaches to one node; pods the scheduler places elsewhere sit
// ContainerCreating until Helm's wait times out, ten minutes into the bootstrap.
//
// It asks the CHART which pods mount the volume, by rendering it, rather than keeping a
// second list in dcctl of which areas use the store: that list is the chart's to keep.
// The render needs no cluster. The root key is a placeholder, and nothing rendered is applied.
func checkObjectStoreMountable(ctx context.Context, st *State) error {
	return checkObjectStoreMountableWith(ctx, st, nil)
}

// checkObjectStoreMountableWith is checkObjectStoreMountable with a hook over the authoring
// values it renders, so a test can ask about an area set the profile flags cannot produce.
func checkObjectStoreMountableWith(ctx context.Context, st *State, tweak func(map[string]interface{})) error {
	spec := st.ObjectStore
	if spec == nil || spec.Backend != dcv1beta1.ObjectStoreFilesystem || spec.AccessMode == "ReadWriteMany" ||
		DefaultsToFilesystemObjectStore(st.Provider, st.Binding) {
		return nil
	}
	ch, err := loadEmbeddedChart()
	if err != nil {
		return fmt.Errorf("loading the chart to check the object store: %w", err)
	}
	authoring := renderOnlyAuthoring(st)
	if tweak != nil {
		tweak(authoring)
	}
	manifest, err := renderChartClientSide(ctx, ch, authoring)
	if err != nil {
		return fmt.Errorf("the chart refuses this object store: %w", err)
	}
	return refuseSharedReadWriteOnce(manifest)
}

// renderOnlyAuthoring is the authoring values a pre-flight render uses: what a real run
// would render (dcctl's values with the operator's overlay over them), with the values the
// render step has not settled yet filled so the chart's schema accepts them. Nothing
// rendered from it is applied.
func renderOnlyAuthoring(st *State) map[string]interface{} {
	authoring := authoringValues(st)
	inst, _ := authoring["instance"].(map[string]interface{})
	cfg, _ := inst["config"].(map[string]interface{})
	if cfg == nil {
		cfg = map[string]interface{}{}
		inst["config"] = cfg
	}
	cfg["infrastructure"] = mergedStringKeyMap(cfg["infrastructure"], map[string]interface{}{
		"secrets": map[string]interface{}{"rootKey": base64.StdEncoding.EncodeToString(make([]byte, 32))},
	})
	// Values the render step has not settled yet, filled so the chart's schema accepts the
	// render. Nothing rendered here is applied.
	if ing, ok := authoring["ingress"].(map[string]interface{}); ok && ing["host"] == "" {
		ing["host"] = ingressHostFor(st)
	}
	if img, ok := authoring["image"].(map[string]interface{}); ok {
		if img["registry"] == "" {
			img["registry"] = DefaultImageRegistry
		}
		if img["tag"] == "" {
			img["tag"] = "render-only"
		}
	}
	return authoring
}

// refuseSharedReadWriteOnce counts the pods in a rendered manifest that mount the store volume
// and refuses more than one: the caller has already established the claim is ReadWriteOnce and
// not colocated.
func refuseSharedReadWriteOnce(manifest string) error {
	pods := 0
	var areas []string
	for _, doc := range releaseutil.SplitManifests(manifest) {
		var dep appsv1.Deployment
		if yaml.Unmarshal([]byte(doc), &dep) != nil || dep.Kind != "Deployment" {
			continue
		}
		for _, v := range dep.Spec.Template.Spec.Volumes {
			if v.Name == "blob-store" {
				n := 1
				if dep.Spec.Replicas != nil {
					n = int(*dep.Spec.Replicas)
				}
				pods += n
				areas = append(areas, fmt.Sprintf("%s x%d", dep.Labels["devicechain.io/functional-area"], n))
			}
		}
	}
	if pods > 1 {
		return fmt.Errorf("the filesystem object store is a ReadWriteOnce volume and %d pods would mount it (%s). "+
			"A ReadWriteOnce volume attaches to one node, so pods the scheduler places elsewhere never start. "+
			"Use --object-store-access-mode ReadWriteMany with a storage class that supports it, "+
			"--object-store s3, or deploy fewer areas that use the store", pods, strings.Join(areas, ", "))
	}
	return nil
}
