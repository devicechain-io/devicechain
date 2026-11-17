// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"testing"
	"time"

	dcv1beta1 "github.com/devicechain-io/dc-k8s/api/v1beta1"
	"github.com/devicechain-io/dc-microservice/config"
	"github.com/hashicorp/terraform-exec/tfexec"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestShardGroupsUpgradeBrokerSafety(t *testing.T) {
	original := readLiveInfra
	t.Cleanup(func() { readLiveInfra = original })
	for _, servers := range []int32{1, 2, 3, 5} {
		t.Run(fmt.Sprint(servers), func(t *testing.T) {
			readLiveInfra = func(context.Context, string, string) (liveInfra, error) {
				return liveInfra{Broker: &appsv1.StatefulSet{Spec: appsv1.StatefulSetSpec{Replicas: &servers}}}, nil
			}
			st := &State{Instance: "prod", Profile: "default", HA: false, Evolving: true,
				Values: map[string]string{}, ValuesOverlay: shardGroupsValues(shardBlock())}
			err := precheckShardGroupsUpgrade(t.Context(), st)
			if servers < 3 {
				require.ErrorContains(t, err, "single-server (non-HA)")
			} else {
				require.NoError(t, err)
			}
		})
	}
	require.Error(t, refuseStandaloneShardGroups(0, false))
	readLiveInfra = func(context.Context, string, string) (liveInfra, error) {
		return liveInfra{}, fmt.Errorf("cannot read broker")
	}
	st := &State{Values: map[string]string{}, ValuesOverlay: shardGroupsValues(shardBlock())}
	require.ErrorContains(t, precheckShardGroupsUpgrade(t.Context(), st), "cannot read broker")
	st.ValuesOverlay = nil
	require.NoError(t, precheckShardGroupsUpgrade(t.Context(), st))
}

func TestShardGroupsOverlayCannotDropRecordedGroups(t *testing.T) {
	for _, typed := range []bool{false, true} {
		for _, reset := range []bool{false, true} {
			inst := &dcv1beta1.Instance{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{}}}
			if typed {
				inst.Spec.ShardGroups = &dcv1beta1.ShardGroupsSpec{Count: 3, Suffixes: []string{"inbound-events"}}
			} else {
				raw, err := encodeValuesOverlay(shardGroupsValues(shardBlock()))
				require.NoError(t, err)
				inst.Annotations[AnnotationValuesOverlay] = raw
			}
			st := &State{Instance: "prod", Profile: "default", Values: map[string]string{}, ShardGroups: inst.Spec.ShardGroups}
			opts := UpgradeOptions{ResetValues: reset}
			if !reset {
				opts.ValuesOverlay = map[string]interface{}{"replicaCount": 1}
			}
			require.ErrorContains(t, applyUpgradeValuesOverlay(st, inst, opts), "drop recorded shardGroups")
			opts = UpgradeOptions{ValuesOverlay: shardGroupsValues(shardBlock())}
			require.NoError(t, applyUpgradeValuesOverlay(st, inst, opts))
		}
	}
}

func TestShardGroupsReservationRefusesBeforeInfra(t *testing.T) {
	st := &State{Instance: "prod", Profile: "default", Values: map[string]string{},
		ValuesOverlay: shardGroupsValues(shardBlock())}
	mergeOverlay(st.ValuesOverlay, map[string]interface{}{"instance": map[string]interface{}{"config": map[string]interface{}{"infrastructure": map[string]interface{}{"nats": map[string]interface{}{"streamMaxBytes": int64(1 << 50)}}}}})
	require.ErrorContains(t, stepInfraApply(t.Context(), st), "would reserve")
}

func TestShardGroupsKeepsAnAlreadyClusteredBroker(t *testing.T) {
	st := &State{Instance: "prod", Profile: "default", Values: map[string]string{},
		ValuesOverlay: shardGroupsValues(shardBlock())}
	outputs := fakeOutputs{"nats_cluster_replicas": tfexec.OutputMeta{Value: json.RawMessage("3")}}
	require.NoError(t, checkHaNotTornDown(t.Context(), st, outputs))
	require.NoError(t, refuseStandaloneShardGroups(3, true))
	outputs["nats_cluster_replicas"] = tfexec.OutputMeta{Value: json.RawMessage("5")}
	require.Error(t, checkHaNotTornDown(t.Context(), st, outputs))
}

func TestShardGroupsSafetyChecksPrecedeChanges(t *testing.T) {
	calls, _ := overlayCalls(t, "upgrade.go", "Upgrade")
	require.NotEmpty(t, calls["precheckShardGroupsUpgrade"])
	require.Less(t, calls["precheckShardGroupsUpgrade"][0], calls["planUpgradeInfra"][0])
	require.Less(t, calls["precheckShardGroupsUpgrade"][0], calls["recordUpgradedVersion"][0])
	calls, _ = overlayCalls(t, "steps.go", "stepInfraApply")
	require.NotEmpty(t, calls["precheckJetStreamReservation"])
	require.Less(t, calls["precheckJetStreamReservation"][0], calls["applyInfra"][0])
}

// A non-HA instance with shard groups runs a three-server broker, so every
// consumer of the server count must see three: the certificate has to name each
// server's route host, or verified routes between them cannot form. The
// negative control is the same instance without the block, whose certificate
// names no per-server host.
func TestShardGroupsWidenTheBrokerEverywhere(t *testing.T) {
	route := "dc-nats-2.dc-nats-headless.dci-prod.svc.cluster.local"
	for _, on := range []bool{false, true} {
		st := &State{Instance: "prod", Profile: "default", Values: map[string]string{}}
		if on {
			st.ValuesOverlay = shardGroupsValues(shardBlock())
		}
		leaf, err := settleNATSTLS(t.Context(), nil, st, time.Now().UTC())
		require.NoError(t, err)
		cert, err := parseFirstCertificate(leaf.LeafCertPEM)
		require.NoError(t, err)
		require.Equal(t, on, slices.Contains(cert.DNSNames, route), "%v", cert.DNSNames)
		want := 1
		if on {
			want = 3
		}
		require.Equal(t, want, brokerTopology(st).ServerReplicas)
		require.Equal(t, 1, brokerTopology(st).StreamReplicas)
	}
}

// Narrowing a recorded block abandons data the way dropping it does; widening it
// is allowed. Both the recorded overlay and the typed spec are the record.
func TestShardGroupsOverlayCannotNarrowRecordedGroups(t *testing.T) {
	block := func(count int, suffixes ...interface{}) map[string]interface{} {
		return shardGroupsValues(map[string]interface{}{"count": count, "suffixes": suffixes})
	}
	for _, typed := range []bool{false, true} {
		inst := &dcv1beta1.Instance{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{}}}
		if typed {
			inst.Spec.ShardGroups = &dcv1beta1.ShardGroupsSpec{Count: 3, Suffixes: []string{"inbound-events"}}
		} else {
			raw, err := encodeValuesOverlay(block(3, "inbound-events"))
			require.NoError(t, err)
			inst.Annotations[AnnotationValuesOverlay] = raw
		}
		st := &State{Instance: "prod", Profile: "default", Values: map[string]string{}, ShardGroups: inst.Spec.ShardGroups}
		opts := UpgradeOptions{ValuesOverlay: block(2, "inbound-events")}
		require.ErrorContains(t, applyUpgradeValuesOverlay(st, inst, opts), "reduce shardGroups.count from 3 to 2")
		opts = UpgradeOptions{ValuesOverlay: block(4, "inbound-events")}
		require.NoError(t, applyUpgradeValuesOverlay(st, inst, opts))
	}
	// The default profiles refuse resolved-events outright, so the suffix half is
	// shown on the comparison itself.
	prev := &config.NatsShardGroupsConfiguration{Count: 3, Suffixes: []string{"inbound-events", "resolved-events"}}
	require.ErrorContains(t, refuseShardGroupsNarrowing(prev, &config.NatsShardGroupsConfiguration{Count: 3, Suffixes: []string{"inbound-events"}}), `stop sharding "resolved-events"`)
	require.NoError(t, refuseShardGroupsNarrowing(prev, prev))
	require.NoError(t, refuseShardGroupsNarrowing(nil, prev))
}

// The volume-growth recovery deletes the StatefulSet with --cascade=orphan; the
// cluster is then known by its claims, and the upgrade that recreates it must run.
func TestShardGroupsUpgradeCountsAnOrphanedBrokerByItsClaims(t *testing.T) {
	original := readLiveInfra
	t.Cleanup(func() { readLiveInfra = original })
	for _, claims := range []int{0, 1, 3} {
		readLiveInfra = func(context.Context, string, string) (liveInfra, error) {
			var infra liveInfra
			for i := range claims {
				infra.BrokerClaims = append(infra.BrokerClaims, corev1.PersistentVolumeClaim{
					ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("dc-nats-js-dc-nats-%d", i)}})
			}
			return infra, nil
		}
		st := &State{Instance: "prod", Profile: "default", Values: map[string]string{}, ValuesOverlay: shardGroupsValues(shardBlock())}
		err := precheckShardGroupsUpgrade(t.Context(), st)
		if claims < 3 {
			require.Error(t, err, "%d claims", claims)
		} else {
			require.NoError(t, err)
		}
	}
}
