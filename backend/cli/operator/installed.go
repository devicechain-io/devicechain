// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package operator

import (
	"context"
	"fmt"
	"sort"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"

	dcapply "github.com/devicechain-io/dc-k8s/apply"
	dck8s "github.com/devicechain-io/dc-k8s/config"
)

// crdGVR is where a cluster keeps the definitions this operator installs.
var crdGVR = schema.GroupVersionResource{
	Group: "apiextensions.k8s.io", Version: "v1", Resource: "customresourcedefinitions",
}

// Installed is what a cluster answers when asked which operator it is carrying.
//
// 🔴 IT HAS THREE STATES, NOT TWO, AND CONFLATING ANY PAIR OF THEM IS THE WHOLE
// RISK HERE:
//
//   - ABSENT. No CRD. The cluster was never prepared, or was prepared by a dcctl
//     from before the operator moved into `dcctl install`. The remedy is to run
//     install.
//
//   - UNSTAMPED. The CRDs are there and carry no identity.
//
//     🔴 THIS STATE HAS THREE CAUSES AND ONLY ONE OF THEM IS BENIGN, which is the
//     thing to know before reading the policy as safe. (a) `make deploy` pipes the
//     kustomize CLI to kubectl and never goes through this package, so a
//     maintainer's deliberate hand-install looks exactly like this. (b) Somebody
//     edited the annotation off. (c) — the one that is easy to miss — a dcctl from
//     BEFORE the stamp existed ran `bootstrap` or `upgrade` against a stamped
//     cluster: those verbs server-side-applied this same overlay under the same
//     field manager with Force, so the apply REMOVED the annotation the newer dcctl
//     had set, and moved the schema to whatever that older binary carried. A team on
//     mixed dcctl releases reaches (c) far more often than anyone runs `make deploy`.
//
//     It is still not treated as a mismatch, because dcctl cannot tell the three
//     apart and refusing (a) would overrule a choice it has no better information
//     about. What it must not do is stay quiet: the note the caller prints names the
//     benign cause AND says to run install if that is not what happened, so (c) is
//     recoverable by somebody who reads it. Pre-GA that trade stands; it is written
//     down so the next person weighing it is weighing the real thing.
//
//   - STAMPED. Compare it and say whether it is the one this dcctl needs.
//
// An absence read as agreement would let a bootstrap write an Instance against a
// schema that is not there; an absence read as a MISMATCH would tell a maintainer
// their correct operator is the wrong one. Different sentences, different remedies.
type Installed struct {
	// Present reports that every CRD the overlay declares exists on the cluster.
	Present bool
	// Identity is the stamp those CRDs carry, empty when they carry none.
	Identity string
	// Missing names the CRDs the overlay declares that the cluster does not have.
	// Non-empty only when Present is false, and it is what the refusal quotes.
	Missing []string
}

// Stamped reports whether the cluster's operator can be compared at all.
func (i Installed) Stamped() bool { return i.Present && i.Identity != "" }

// Matches reports whether the cluster carries exactly the operator described by
// want. An unstamped install matches NOTHING — callers must ask Stamped first and
// decide what an unvouchable operator means to them.
func (i Installed) Matches(want string) bool {
	return i.Stamped() && want != "" && i.Identity == want
}

// DeclaredCRDs lists, by name, the CustomResourceDefinitions a rendered overlay
// installs.
//
// Read from the manifests rather than written down, for the reason the namespace
// is: these are the overlay's to name. There are two of them today and there was
// one not long ago, so a constant here would be a second place to remember that
// nothing would force anyone to update.
func DeclaredCRDs(manifests []byte) ([]string, error) {
	objs, err := dcapply.Decode(manifests)
	if err != nil {
		return nil, fmt.Errorf("reading the rendered operator manifests: %w", err)
	}
	var names []string
	for _, o := range objs {
		if o.GetKind() == "CustomResourceDefinition" {
			names = append(names, o.GetName())
		}
	}
	if len(names) == 0 {
		// The same refusal the identity digest makes, for the same reason: an
		// overlay with no CRDs would make every cluster agree with every other.
		return nil, fmt.Errorf("the rendered operator overlay declares no CustomResourceDefinition")
	}
	sort.Strings(names)
	return names, nil
}

// ReadInstalled asks a cluster which operator it has, by reading the CRDs the given
// overlay declares.
//
// 🔴 IT ASKS THE CLUSTER, NEVER THE INSTALL RECORD, and that is the lesson this
// whole guard is built on. A record written by a dcctl from before the operator
// moved says "installed" perfectly truthfully while no operator is present at all —
// so a guard reading it would wave through exactly the clusters it exists to catch.
// Ask for the artifact that travels WITH the thing.
//
// 🔴 EVERY DECLARED CRD MUST BE PRESENT AND THEY MUST AGREE. Checking one would
// pass a half-applied operator — an apply interrupted between two CRDs is an
// ordinary way to end up in that state — and reporting the first stamp found would
// then describe an install that does not exist as a whole.
func ReadInstalled(ctx context.Context, dyn dynamic.Interface, manifests []byte) (Installed, error) {
	names, err := DeclaredCRDs(manifests)
	if err != nil {
		return Installed{}, err
	}

	var (
		found     int
		missing   []string
		stamps    = map[string]string{}
		anyStamps []string
	)
	for _, name := range names {
		obj, err := dyn.Resource(crdGVR).Get(ctx, name, metav1.GetOptions{})
		switch {
		case err == nil:
		case apierrors.IsNotFound(err), meta.IsNoMatchError(err):
			// A cluster with no such CRD is the ABSENT case. Nothing else is:
			// every other error means the cluster would not say what it holds,
			// and answering "not installed" to that is how a bootstrap gets run
			// against an operator that is in fact there.
			missing = append(missing, name)
			continue
		default:
			return Installed{}, fmt.Errorf("reading the operator definition %q from this cluster: %w. "+
				"Refusing to continue: a cluster that will not say which operator it has is not a "+
				"cluster without one", name, err)
		}
		found++
		id := obj.GetAnnotations()[dck8s.IdentityAnnotation]
		stamps[name] = id
		if id != "" {
			anyStamps = append(anyStamps, id)
		}
	}

	if found == 0 || len(missing) > 0 {
		return Installed{Present: false, Missing: missing}, nil
	}

	// 🔴 A DISAGREEMENT IS ITS OWN ANSWER AND MUST NOT BE AVERAGED AWAY. Two CRDs
	// carrying two stamps is a half-applied operator — an apply that died between
	// them, or one hand-edited since. Picking either would describe a cluster that
	// is not in the state the answer claims.
	if len(anyStamps) > 0 && len(anyStamps) != found {
		return Installed{}, fmt.Errorf(
			"this cluster's operator is only partly stamped (%v): some of its definitions carry an "+
				"identity and some do not, which means an install did not finish. Run `dcctl install` "+
				"again to complete it", stamps)
	}
	for _, id := range anyStamps {
		if id != anyStamps[0] {
			return Installed{}, fmt.Errorf(
				"this cluster's definitions disagree about which operator installed them (%v), which "+
					"means an install did not finish. Run `dcctl install` again to complete it", stamps)
		}
	}

	out := Installed{Present: true}
	if len(anyStamps) > 0 {
		out.Identity = anyStamps[0]
	}
	return out, nil
}
