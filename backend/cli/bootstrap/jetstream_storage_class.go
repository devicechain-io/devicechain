// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

const jetStreamStorageClassVar = "nats_jetstream_storage_class"

// appendJetStreamStorageClass selects a default only for a new broker. A surviving
// claim also counts as existing (for example after an interrupted bootstrap).
// Upgrades never call this: the module retains its first choice through ignore_changes.
func appendJetStreamStorageClass(ctx context.Context, typed kubernetes.Interface, st *State, rootdir string, vars []string) ([]string, error) {
	if statedByOperator(rootdir, jetStreamStorageClassVar) {
		return vars, nil
	}
	ns := InstanceNamespace(st.Instance)
	_, err := typed.AppsV1().StatefulSets(ns).Get(ctx, natsStatefulSetName, metav1.GetOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return nil, fmt.Errorf("checking existing broker before selecting JetStream storage class: %w", err)
	}
	if err == nil {
		return vars, nil
	}
	claims, err := typed.CoreV1().PersistentVolumeClaims(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("checking existing JetStream PVCs before selecting storage class: %w", err)
	}
	for _, claim := range claims.Items {
		if brokerClaimNameRe.MatchString(claim.Name) {
			return vars, nil
		}
	}
	// Detect the actual GKE class, including on a cluster with an aliased context.
	// Other clouds and kind retain the module's empty default.
	sc, err := typed.StorageV1().StorageClasses().Get(ctx, "premium-rwo", metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return vars, nil
	}
	if err != nil {
		return nil, fmt.Errorf("checking GKE premium-rwo JetStream storage class: %w", err)
	}
	if sc.Provisioner != "pd.csi.storage.gke.io" {
		return vars, nil
	}
	if sc.Parameters["type"] != "pd-ssd" {
		return nil, fmt.Errorf("GKE JetStream storage class premium-rwo must use type pd-ssd; found %q", sc.Parameters["type"])
	}
	return append(vars, jetStreamStorageClassVar+"=premium-rwo"), nil
}
