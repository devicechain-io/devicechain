// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"fmt"
	"io/fs"
	"regexp"
	"strings"

	assets "github.com/devicechain-io/dc-deploy"

	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// natsStatefulSetName is the StatefulSet the nats Helm release creates. It
// matches the release name the OpenTofu module uses (modules/nats release_name).
const natsStatefulSetName = "dc-nats"

// checkJetStreamVolumeIsUpgradable refuses an infrastructure apply that would
// change the size of an EXISTING NATS JetStream volume.
//
// Kubernetes forbids updating a StatefulSet's volumeClaimTemplates — the field is
// immutable, and the API server rejects the whole object rather than ignoring the
// change. So raising the JetStream PV size, which ADR-020 A0 does (12Gi -> 16Gi,
// to stop the reservation sitting exactly on its headroom floor), turns every
// `tofu apply` against an instance installed from an earlier release into a failed
// Helm upgrade. The release is left in a `failed` state and nothing after it runs.
//
// Left alone the operator meets that as a Kubernetes schema error in the middle of
// a tofu apply — a message about "updates to statefulset spec for fields other
// than 'replicas', 'ordinals', 'template'..." that says nothing about JetStream,
// nothing about which value changed, and nothing about how to proceed. This turns
// it into a sentence with a recipe, before anything is touched.
//
// It is NOT an error in the change: the PV had to move, and pre-GA with a handful
// of instances is the cheapest this will ever be. It is an error to make someone
// discover it the hard way.
func checkJetStreamVolumeIsUpgradable(ctx context.Context, st *State) error {
	if st.DryRun {
		return nil
	}
	want, err := resource.ParseQuantity(jetStreamStorageFor(st))
	if err != nil {
		// The size comes from a shipped constant or the embedded tofu default; an
		// unparseable one is a bug elsewhere, and failing the bootstrap over an
		// unreadable guard input would be worse than not checking.
		return nil
	}

	_, _, typed, err := kubeClients(st.KubeContext)
	if err != nil {
		// A cluster we cannot reach has no existing volume to conflict with, and
		// the apply itself will report the connection problem far better.
		return nil
	}
	sts, err := typed.AppsV1().StatefulSets(natsInfraNamespace).Get(ctx, natsStatefulSetName, metav1.GetOptions{})
	if err != nil {
		// Not found is the overwhelmingly likely case: a fresh install. Any other
		// error (RBAC, transient) is not worth blocking a bootstrap over.
		return nil
	}

	for _, tmpl := range sts.Spec.VolumeClaimTemplates {
		have, ok := tmpl.Spec.Resources.Requests["storage"]
		if !ok || have.Cmp(want) == 0 {
			continue
		}
		return fmt.Errorf(
			"the existing NATS JetStream volume is %s and this release provisions %s. A "+
				"StatefulSet's volumeClaimTemplates cannot be changed in place — Kubernetes "+
				"rejects the whole object — so applying this would leave the nats Helm release "+
				"in a failed state and stop the bootstrap partway.\n\n"+
				"To upgrade (the recipe below keeps JetStream data; the PVCs are not deleted "+
				"with the StatefulSet):\n"+
				"  1. kubectl delete statefulset %s -n %s --cascade=orphan\n"+
				"  2. if your StorageClass allows expansion, patch each PVC's "+
				"spec.resources.requests.storage to %s; otherwise delete the PVCs to start "+
				"with fresh JetStream state\n"+
				"  3. re-run this command\n\n"+
				"To stay on the current size instead, pass -var nats_jetstream_storage=%s to "+
				"OpenTofu directly. Note the shipped stream and KV ceilings are sized against "+
				"%s: at %s the reservation sits exactly on its headroom floor, so the platform "+
				"cannot add a stream or bucket without moving this volume",
			have.String(), want.String(),
			natsStatefulSetName, natsInfraNamespace, want.String(),
			have.String(), want.String(), have.String())
	}
	return nil
}

// jetStreamStorageFor is the volume size this run would provision: the compact
// preset's when compact is on, otherwise the shipped OpenTofu default.
//
// Read from the embedded infrastructure config rather than restated, for the same
// reason TestRenderedReservationFitsTheJetStreamStore reads it: a hand-copied
// size keeps comparing against the old volume after the real one moves, which is
// precisely the change this guard exists to notice.
func jetStreamStorageFor(st *State) string {
	if st.Compact {
		return compact.JetStreamStorage
	}
	if size := embeddedJetStreamStorageDefault(); size != "" {
		return size
	}
	return ""
}

// natsInfraNamespace is where the OpenTofu root installs the broker. It mirrors
// the root's `namespace` default; dcctl does not override it.
const natsInfraNamespace = "dc-system"

// embeddedJetStreamStorageDefault extracts the nats_jetstream_storage default from
// the embedded OpenTofu variables.tf, or "" if it cannot be found.
func embeddedJetStreamStorageDefault() string {
	raw, err := readEmbeddedTofuVariables()
	if err != nil {
		return ""
	}
	m := jetStreamStorageDefaultRe.FindSubmatch(raw)
	if m == nil {
		return ""
	}
	return strings.TrimSpace(string(m[1]))
}

// readEmbeddedTofuVariables reads the shipped root variables.tf out of the binary.
func readEmbeddedTofuVariables() ([]byte, error) {
	return fs.ReadFile(assets.OpenTofu(), "variables.tf")
}

// jetStreamStorageDefaultRe finds the `default` immediately following the
// nats_jetstream_storage declaration.
var jetStreamStorageDefaultRe = regexp.MustCompile(`(?s)variable\s+"nats_jetstream_storage"\s*\{.*?default\s*=\s*"([^"]+)"`)
