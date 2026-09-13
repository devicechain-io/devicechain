// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"fmt"
	"sort"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"

	"helm.sh/helm/v3/pkg/action"
)

// clusterInstances is one cluster's answer to "which instances do you already hold?".
//
// 🔴 ONE READER, CONSULTED BY EVERYTHING THAT ASKS (ADR-080). Two readers that can
// disagree about whose cluster this is would be the defect the boundary exists to
// remove: the refusal would name one instance while the check that let the run past
// was looking at another.
//
// Three answers rather than two, the same shape releaseInstance and
// reuseMintedCredential are written to: "this cluster holds nothing", "it holds these",
// and "something is here and I cannot attribute it". The third is an ERROR, never an
// empty set — reading "could not tell" as "nothing there" is the direction that writes
// a second instance over the first.
type clusterInstances struct {
	// IDs are the instance ids this cluster holds, sorted and de-duplicated. Empty
	// means the cluster holds no DeviceChain instance.
	IDs []string
	// Source names the artifact that answered, so a refusal can say what it READ
	// rather than only asserting its conclusion. Empty when nothing answered.
	Source string
}

// holds reports whether this cluster's answer names the given instance.
//
// The complement of othersThan, and separate from it because the two questions have
// different callers and different consequences: othersThan asks "is anything ELSE here"
// so a bootstrap can refuse, and this asks "is THIS one here" so an upgrade can tell an
// instance that predates declarations from a name nobody ever installed.
func (c clusterInstances) holds(instance string) bool {
	for _, id := range c.IDs {
		if id == instance {
			return true
		}
	}
	return false
}

// othersThan returns the held ids that are not the instance this run names.
func (c clusterInstances) othersThan(instance string) []string {
	var out []string
	for _, id := range c.IDs {
		if id != instance {
			out = append(out, id)
		}
	}
	return out
}

// instanceSource is one durable artifact a bootstrap leaves behind that can name the
// instance it belongs to.
type instanceSource struct {
	// what names the artifact in an operator-facing message.
	what string
	read func() ([]string, error)
}

// firstAnsweringSource walks the sources in order and returns the first that names
// anything, or the first that cannot answer.
//
// 🔴 THE DECLARATION LEADS BECAUSE IT IS THE EARLIEST ARTIFACT, AND THAT IS THE WHOLE
// POINT OF COMPOSING THEM RATHER THAN READING ONE. A bootstrap writes its declaration in
// the declare step, its credentials in the infrastructure apply and its Helm release in
// the Helm install — in that order — so a run that dies between the first two leaves a
// cluster holding a declaration and no release. Keyed on the release alone this reader
// would call that cluster empty and let a differently-named instance walk into it,
// leaving the half-built case riding the foreign-Secret refusal, which is the guard this
// check exists to replace.
//
// The two behind it are in the order an operator would look, not a strict write order;
// either of them being present disqualifies the cluster on its own, so nothing turns on
// which is asked second.
//
// 🔑 AN EMPTY ANSWER IS NOT AN ANSWER. A source that is simply not there yet — no CRD on
// a virgin cluster, no release until the Helm step — must not stop the walk, or every
// first bootstrap would read the first source's silence as the whole cluster's.
func firstAnsweringSource(sources []instanceSource) (clusterInstances, error) {
	for _, s := range sources {
		ids, err := s.read()
		if err != nil {
			return clusterInstances{}, err
		}
		if len(ids) > 0 {
			return clusterInstances{IDs: ids, Source: s.what}, nil
		}
	}
	return clusterInstances{}, nil
}

// readClusterInstances is the seam the step reads through. Indirected for the same
// reason lookupDeployedInstance is: the decision behind it is whether a run may write
// to a cluster that is already running somebody else's instance, and that decision has
// to be exercisable without standing one up.
var readClusterInstances = clusterInstancesFor

// clusterInstancesFor assembles the three sources against a real cluster.
func clusterInstancesFor(ctx context.Context, kubeContext string) (clusterInstances, error) {
	dyn, _, typed, err := kubeClients(kubeContext)
	if err != nil {
		return clusterInstances{}, fmt.Errorf("connecting to the cluster to ask which instances "+
			"it already holds: %w", err)
	}
	helmCfg, err := helmActionConfig(kubeContext)
	if err != nil {
		return clusterInstances{}, fmt.Errorf("reaching the Helm release records in this cluster, "+
			"to ask which instance they belong to: %w", err)
	}
	// 🔴 THE ORDER IS THE ORDER A BOOTSTRAP WRITES THESE, EARLIEST FIRST, AND THAT IS THE
	// WHOLE POINT OF HAVING THREE. A run that dies part-way leaves a prefix of them: the
	// declaration lands in the declare step, the minted credentials in the infrastructure
	// step after it, the release in the Helm step after that. Asking in write order means
	// the half-built cluster is answered by whatever it actually got to, and the earlier
	// the source the more runs it covers.
	//
	// An earlier draft asked the release before the credentials. It had no correctness
	// hole — each source disqualifies the cluster on its own — but it answered a cluster
	// holding credentials and an unattributable release with "I cannot tell", where the
	// credentials could have named the holder. Failing closed is safe; naming the instance
	// is useful, and there is no reason to give up the second to keep the first.
	return firstAnsweringSource([]instanceSource{
		{"the instance declarations in this cluster", func() ([]string, error) {
			return declaredInstances(ctx, dyn)
		}},
		{fmt.Sprintf("the credentials dcctl minted in %s", infraNamespace), func() ([]string, error) {
			return ownedSecretInstances(ctx, typed)
		}},
		{"the DeviceChain Helm releases in this cluster", func() ([]string, error) {
			return releasedInstances(helmCfg)
		}},
	})
}

// declaredInstances lists the Instance declarations in a cluster.
//
// 🔴 IT MUST TOLERATE THE CRD BEING ABSENT, because this runs BEFORE stepInstallCore
// installs it. A virgin cluster has to read as "no instances" rather than as an error,
// or the boundary would refuse the first bootstrap of every cluster.
//
// 🔑 THE DISAMBIGUATION isInstanceNotFound MAKES DOES NOT APPLY TO A LIST, and reusing
// it verbatim would be wrong rather than merely redundant. That function exists because
// a GET names an object, so its 404 is ambiguous — "no such object" and "no such
// resource type" are reported identically — and it separates them on the Details the
// API server attaches to the name it was asked for. A LIST names no object, so the
// ambiguity cannot arise: a 404 to a collection request can only mean the collection is
// not served. Both ways client-go can report that are covered — a plain NotFound when
// the API server answers with a Status, a NoMatch when a RESTMapper is in the way —
// which is the same pair clusterArchivePath uses for the CNPG CRD, for the same reason.
//
// Every OTHER error fails: a cluster that will not say what it holds is not an empty
// one, and treating it as empty is how a second instance gets written over the first.
func declaredInstances(ctx context.Context, dyn dynamic.Interface) ([]string, error) {
	list, err := dyn.Resource(instanceGVR).List(ctx, metav1.ListOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) || meta.IsNoMatchError(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("listing the instance declarations in this cluster: %w. Refusing "+
			"to continue: if a declaration IS there, treating this cluster as empty would "+
			"bootstrap a second instance over a live one", err)
	}
	var ids []string
	for _, item := range list.Items {
		// A declaration part-way through being destroyed still counts. Some of that
		// instance is still in the cluster — that is what the Destroying phase MEANS —
		// and bootstrapping a different one into the remains is exactly this case.
		if name := item.GetName(); name != "" {
			ids = append(ids, name)
		}
	}
	sort.Strings(ids)
	return ids, nil
}

// releasedInstances asks the DeviceChain releases in this cluster whose they are.
//
// 🔴 IT ASKS ALL OF THEM NOW, WHERE IT USED TO ASK ONE BY NAME. Release names became
// instance-derived, so the name can no longer be guessed by a run that does not already
// know the answer — see deviceChainReleases. The three answers are unchanged: no release
// is an empty list, attributable ones are their instance ids, and one that does not say
// which instance it belongs to is an ERROR rather than an absence.
func releasedInstances(cfg *action.Configuration) ([]string, error) {
	return deviceChainReleases(cfg)
}

// ownedSecretInstances reads the ownership stamps off the Secrets dcctl minted into the
// infrastructure namespace — the last of the three sources, and the one that still
// answers for a cluster whose declaration was released by hand and whose release was
// never reached.
//
// 🔴 ONLY dcctl's OWN SECRETS ARE READ. dc-system fills up with Secrets nobody here
// wrote — CloudNativePG's generated certificates, Helm's own release records, whatever
// an operator put there — and none of them says anything about an instance. The
// managed-by stamp is what separates the ones a conclusion may be drawn from, and it is
// the same stamp writeOwnedSecret refuses on.
//
// A Secret carrying dcctl's stamp and no instance name is unattributable and fails
// closed. writeOwnedSecret writes both or neither, so the only way to reach it is a
// hand-edited annotation — which is precisely when guessing is worst.
func ownedSecretInstances(ctx context.Context, typed kubernetes.Interface) ([]string, error) {
	list, err := typed.CoreV1().Secrets(infraNamespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		// The namespace does not exist until the infrastructure apply creates it, so on
		// a cluster this reader is asked about before that step it is simply absent.
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("listing the Secrets in %s, to ask which instance minted them: "+
			"%w. Refusing to continue rather than read a namespace that will not answer as an "+
			"empty one", infraNamespace, err)
	}
	seen := map[string]bool{}
	var ids []string
	for i := range list.Items {
		s := &list.Items[i]
		own := readOwnership(s)
		if !own.managed {
			continue
		}
		if own.instance == "" {
			return nil, fmt.Errorf("Secret %s/%s carries dcctl's %s=%s stamp and does not say "+
				"which instance it was minted for, so this cluster cannot be told from an empty "+
				"one. Refusing to bootstrap into it rather than guess — inspect it with "+
				"`kubectl get secret %s -n %s -o yaml`",
				infraNamespace, s.Name, annotationManagedBy, managedByDcctl, s.Name, infraNamespace)
		}
		if !seen[own.instance] {
			seen[own.instance] = true
			ids = append(ids, own.instance)
		}
	}
	sort.Strings(ids)
	return ids, nil
}
