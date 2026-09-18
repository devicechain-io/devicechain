// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package operator describes the DeviceChain operator as an artifact: what it is
// called, where its source lives, what manifests it renders to, which namespace
// it occupies, and how to put it on a cluster.
//
// 🔴 IT EXISTS BECAUSE THREE VERBS NOW SHARE IT, AND THEY ARE NOT PEERS. The
// operator is CLUSTER-SCOPED — one copy per cluster, shared by every instance on
// it — so `dcctl install` owns it. `dcctl bootstrap` and `dcctl upgrade` still
// touch it today only because the move away from them is being done in slices,
// and each of those verbs has to agree with install about what "the operator" IS.
//
// Before this package they agreed by having written the same lines three times.
// The overlay was rendered in four places, the image reference assembled in three,
// and the namespace read in three more — every one of them correct, and every one
// of them a separate thing to remember when the overlay changes. The specific
// failure that shape produces is not a crash: it is two dcctl verbs installing
// what they each believe is the same operator, differing, and neither noticing.
//
// 🔑 SO THE RULE FOR THIS PACKAGE IS NARROW: it answers questions ABOUT the
// operator, and it does not know what a dcctl run is. Nothing here takes the
// bootstrap State, prints progress, or decides when an apply should happen —
// those belong to the verb, which is the thing that differs between callers. What
// must not differ lives here.
package operator

import (
	"context"
	"fmt"
	"path/filepath"

	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"

	dcapply "github.com/devicechain-io/dc-k8s/apply"
	dck8s "github.com/devicechain-io/dc-k8s/config"
)

// ImageName is what the controller image is called in a registry, under whatever
// registry and tag a run resolves — the operator overlay's Deployment is patched
// to this name, and the developer path's ko build pushes to it.
//
// 🔴 IT MUST MATCH THE NAME THE RELEASE PIPELINE PUBLISHES THE OPERATOR UNDER:
// ghcr.io/devicechain-io/operator. See .github/workflows/release.yml, which
// special-cases backend/k8s to ".../operator" rather than deriving the name from
// the module directory the way it does for every service. A mismatch makes the
// published-image path pull an image that was never published.
//
// One constant, because a build that pushes to one name and an overlay that pulls
// from another produce an ImagePullBackOff on a controller nobody is watching,
// several minutes after a run that reported every earlier step green.
const ImageName = "operator"

// SourceDir is the Go module the controller is built from, under a source
// checkout's root. Only the developer (--build) path needs it.
func SourceDir(repoRoot string) string {
	return filepath.Join(repoRoot, "backend", "k8s")
}

// ImageRef is the controller image a settled image source names.
//
// 🔴 NEITHER ARGUMENT MAY BE EMPTY, and callers are expected to have settled them
// already: "/operator:" is a syntactically valid reference that pulls nothing, so
// a run built on one proceeds and dies minutes later naming an image nobody asked
// for. This function cannot refuse usefully — it has no idea which run it is
// serving — so the refusal belongs at the point the source is resolved, and
// bootstrap's requireResolvedImages is where it is written down.
func ImageRef(registry, version string) string {
	return fmt.Sprintf("%s/%s:%s", registry, ImageName, version)
}

// Render produces the manifest stream for an operator at the given image: the
// config/default overlay exactly as `make deploy` renders it — namespace, name
// prefix, CRDs, RBAC and the controller Deployment.
//
// Rendered in-process from manifests embedded in the dcctl binary, so no source
// checkout and no kustomize/kubectl binary is needed at runtime. An empty image
// leaves the overlay's placeholder in place, which is what a caller that wants
// only the namespace passes.
func Render(image string) ([]byte, error) {
	return dck8s.RenderOperator(image)
}

// Namespace reports the namespace the operator occupies.
//
// 🔴 READ FROM THE RENDERED OVERLAY, NEVER WRITTEN DOWN AS A CONSTANT, and the
// reason is bigger than the operator: the cluster lock is a Lease in this same
// namespace. The name is a kustomize setting, so a copy of it in Go is a second
// place to remember — and a rename would leave dcctl taking its lock in a
// namespace nothing else uses, which is a lock that silently protects nothing
// while every run reports success.
//
// The overlay renders with no image because nothing here is going to apply it.
func Namespace() (string, error) {
	manifests, err := Render("")
	if err != nil {
		return "", fmt.Errorf("rendering the operator overlay to find its namespace: %w", err)
	}
	return NamespaceOf(manifests)
}

// NamespaceOf reads the namespace out of a stream a caller has already rendered,
// for the callers that need the manifests anyway and must not render twice.
func NamespaceOf(manifests []byte) (string, error) {
	objs, err := dcapply.Decode(manifests)
	if err != nil {
		return "", err
	}
	for _, o := range objs {
		if o.GetKind() == "Namespace" {
			if n := o.GetName(); n != "" {
				return n, nil
			}
		}
	}
	// Fail rather than default. A caller that got "" here would take the cluster
	// lock in whatever namespace its kubeconfig happens to name, and two runs
	// doing that in two contexts would both succeed.
	return "", fmt.Errorf("the rendered operator overlay declares no Namespace, so there is " +
		"nowhere to put the operator or to take the cluster lock")
}

// Apply server-side applies a rendered stream to a cluster.
//
// The WHOLE stream is applied, not just the controller Deployment, and the CRDs
// are the half with the trap: the API server prunes fields a structural schema
// does not declare, so a cluster left on the CRDs it was first installed with
// would silently discard anything a later release added to them.
func Apply(ctx context.Context, dyn dynamic.Interface, disco discovery.DiscoveryInterface, manifests []byte) error {
	return dcapply.NewApplyOptions(dyn, disco).WithServerSide(true).Apply(ctx, manifests)
}

// Identity is the value a cluster's operator install can be compared against —
// a digest over the rendered CRDs, stamped onto every rendered object.
//
// Re-exported through this package rather than reached for directly in dc-k8s, so
// that the verbs which must AGREE about the operator ask one package about all of
// it. The digest's derivation, and why it covers the CRDs only, are documented
// where it is computed.
func Identity(manifests []byte) (string, error) {
	return dck8s.IdentityOf(manifests)
}
