// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"
	"testing"

	dcv1beta1 "github.com/devicechain-io/dc-k8s/api/v1beta1"
	"github.com/devicechain-io/dc-microservice/config"
	"github.com/stretchr/testify/require"
	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/chartutil"
	"helm.sh/helm/v3/pkg/engine"
	"helm.sh/helm/v3/pkg/releaseutil"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"sigs.k8s.io/yaml"
)

func resolvedBlock(count int, suffixes ...interface{}) map[string]interface{} {
	return map[string]interface{}{"count": count, "suffixes": suffixes}
}

// A fresh install shards resolved-events with event-processing deployed: dcctl, the
// declaration and the chart all accept it. The guard that refused it is gone.
func TestShardGroupsAcceptResolvedEventsOnAFreshInstall(t *testing.T) {
	st := &State{Instance: "prod", Profile: "default", Values: map[string]string{},
		ValuesOverlay: shardGroupsValues(resolvedBlock(2, "inbound-events", "resolved-events"))}
	cfg, err := shardGroupsFromValues(authoringValues(st))
	require.NoError(t, err)
	require.Equal(t, []string{"inbound-events", "resolved-events"}, cfg.Suffixes)
	areas, err := shardGroupValuesAreas(authoringValues(st))
	require.NoError(t, err)
	require.Contains(t, areas, "event-processing")
	require.Contains(t, areas, "device-state")
	require.NoError(t, ValidateInstanceSpec(InstanceSpecFrom(st, ClusterBinding{}, "local")))
	ch, err := loadEmbeddedChart()
	require.NoError(t, err)
	_, err = renderInstanceConfigDocument(t.Context(), ch, renderOnlyAuthoring(st))
	require.NoError(t, err)
}

// On upgrade, with event-processing deployed, resolved-events cannot start being
// sharded and a sharded one cannot change its count. Without event-processing it can.
func TestShardGroupsUpgradeRefusesReshapingResolvedEvents(t *testing.T) {
	for _, typed := range []bool{false, true} {
		record := func(block map[string]interface{}) (*State, *dcv1beta1.Instance) {
			inst := &dcv1beta1.Instance{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{}}}
			if typed {
				var suffixes []string
				for _, s := range block["suffixes"].([]interface{}) {
					suffixes = append(suffixes, s.(string))
				}
				inst.Spec.ShardGroups = &dcv1beta1.ShardGroupsSpec{Count: block["count"].(int), Suffixes: suffixes}
			} else {
				raw, err := encodeValuesOverlay(shardGroupsValues(block))
				require.NoError(t, err)
				inst.Annotations[AnnotationValuesOverlay] = raw
			}
			return &State{Instance: "prod", Profile: "default", Values: map[string]string{}, ShardGroups: inst.Spec.ShardGroups}, inst
		}
		st, inst := record(resolvedBlock(3, "inbound-events"))
		err := applyUpgradeValuesOverlay(st, inst, UpgradeOptions{ValuesOverlay: shardGroupsValues(resolvedBlock(3, "inbound-events", "resolved-events"))})
		require.ErrorContains(t, err, "start sharding resolved-events on an existing instance", "typed=%v", typed)

		st, inst = record(resolvedBlock(3, "inbound-events", "resolved-events"))
		err = applyUpgradeValuesOverlay(st, inst, UpgradeOptions{ValuesOverlay: shardGroupsValues(resolvedBlock(4, "inbound-events", "resolved-events"))})
		require.ErrorContains(t, err, "change shardGroups.count from 3 to 4", "typed=%v", typed)

		st, inst = record(resolvedBlock(3, "inbound-events", "resolved-events"))
		require.NoError(t, applyUpgradeValuesOverlay(st, inst, UpgradeOptions{ValuesOverlay: shardGroupsValues(resolvedBlock(3, "inbound-events", "resolved-events"))}), "typed=%v", typed)

		// inbound-events alone may still grow.
		st, inst = record(resolvedBlock(3, "inbound-events"))
		require.NoError(t, applyUpgradeValuesOverlay(st, inst, UpgradeOptions{ValuesOverlay: shardGroupsValues(resolvedBlock(4, "inbound-events"))}), "typed=%v", typed)
	}
	// Nothing recorded: a block typed for the first time on upgrade is new to the instance.
	st := &State{Instance: "prod", Profile: "default", Values: map[string]string{}}
	inst := &dcv1beta1.Instance{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{}}}
	err := applyUpgradeValuesOverlay(st, inst, UpgradeOptions{ValuesOverlay: shardGroupsValues(resolvedBlock(3, "resolved-events"))})
	require.ErrorContains(t, err, "start sharding resolved-events on an existing instance")

	// Without event-processing the stream carries no detection state to split.
	prev := &config.NatsShardGroupsConfiguration{Count: 3, Suffixes: []string{"inbound-events"}}
	next := &config.NatsShardGroupsConfiguration{Count: 4, Suffixes: []string{"inbound-events", "resolved-events"}}
	require.NoError(t, refuseResolvedEventsReshape(prev, next, []string{"device-management", "device-state"}))
	require.Error(t, refuseResolvedEventsReshape(prev, next, []string{"device-management", "event-processing"}))
	require.NoError(t, refuseResolvedEventsReshape(nil, nil, []string{"event-processing"}))
}

// secretLookup answers the chart's lookup of its own instance-config Secret.
type secretLookup struct{ client dynamic.Interface }

func (s secretLookup) GetClientFor(apiVersion, kind string) (dynamic.NamespaceableResourceInterface, bool, error) {
	return s.client.Resource(schema.GroupVersionResource{Version: "v1", Resource: "secrets"}), true, nil
}

// renderWithPrior renders the chart as a real `helm upgrade` would, with the instance's
// current Secret readable through lookup (or no Secret, when prior is nil).
func renderWithPrior(t *testing.T, ch *chart.Chart, vals map[string]interface{}, prior []byte) error {
	t.Helper()
	// Name and namespace come from the chart's own client-side render.
	manifest, err := renderChartClientSide(t.Context(), ch, vals)
	require.NoError(t, err)
	want := renderedInstanceConfigSecretName(ch, vals)
	var name, namespace string
	for _, doc := range releaseutil.SplitManifests(manifest) {
		var obj struct {
			Kind     string `json:"kind"`
			Metadata struct {
				Name      string `json:"name"`
				Namespace string `json:"namespace"`
			} `json:"metadata"`
		}
		if yaml.Unmarshal([]byte(doc), &obj) == nil && obj.Kind == "Secret" && obj.Metadata.Name == want {
			name, namespace = obj.Metadata.Name, obj.Metadata.Namespace
		}
	}
	require.NotEmpty(t, name)
	var objects []runtime.Object
	if prior != nil {
		objects = append(objects, &unstructured.Unstructured{Object: map[string]interface{}{
			"apiVersion": "v1", "kind": "Secret",
			"metadata": map[string]interface{}{"name": name, "namespace": namespace},
			"data":     map[string]interface{}{"instance": base64.StdEncoding.EncodeToString(prior)},
		}})
	}
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{{Version: "v1", Resource: "secrets"}: "SecretList"}, objects...)
	caps := chartutil.DefaultCapabilities.Copy()
	caps.APIVersions = append(caps.APIVersions, "monitoring.coreos.com/v1")
	top, err := chartutil.ToRenderValues(ch, vals, chartutil.ReleaseOptions{Name: "render", Namespace: "default", IsUpgrade: true}, caps)
	require.NoError(t, err)
	_, err = engine.RenderWithClientProvider(ch, top, secretLookup{client: client})
	return err
}

// A raw `helm upgrade` reads the instance's current document back and refuses the same
// two reshapes dcctl refuses.
func TestChartUpgradeRefusesReshapingResolvedEvents(t *testing.T) {
	ch, err := loadEmbeddedChart()
	require.NoError(t, err)
	doc := func(block map[string]interface{}) []byte {
		st := &State{Instance: "prod", Profile: "default", Values: map[string]string{}}
		if block != nil {
			st.ValuesOverlay = shardGroupsValues(block)
		}
		raw, err := renderInstanceConfigDocument(t.Context(), ch, renderOnlyAuthoring(st))
		require.NoError(t, err)
		return raw
	}
	next := func(block map[string]interface{}) map[string]interface{} {
		return renderOnlyAuthoring(&State{Instance: "prod", Profile: "default", Values: map[string]string{}, ValuesOverlay: shardGroupsValues(block)})
	}
	inboundOnly := doc(resolvedBlock(3, "inbound-events"))
	resolved3 := doc(resolvedBlock(3, "inbound-events", "resolved-events"))

	err = renderWithPrior(t, ch, next(resolvedBlock(3, "inbound-events", "resolved-events")), inboundOnly)
	require.ErrorContains(t, err, "cannot be enabled on an existing instance")
	err = renderWithPrior(t, ch, next(resolvedBlock(3, "inbound-events", "resolved-events")), doc(nil))
	require.ErrorContains(t, err, "cannot be enabled on an existing instance")
	err = renderWithPrior(t, ch, next(resolvedBlock(4, "inbound-events", "resolved-events")), resolved3)
	require.ErrorContains(t, err, "count cannot change")
	// The controls: the same shape again, and a first install with nothing to read back.
	require.NoError(t, renderWithPrior(t, ch, next(resolvedBlock(3, "inbound-events", "resolved-events")), resolved3))
	require.NoError(t, renderWithPrior(t, ch, next(resolvedBlock(3, "inbound-events", "resolved-events")), nil))
	require.True(t, strings.Contains(string(resolved3), "resolved-events"))
}

// Every upgrade, flagless included, is judged against what the cluster RUNS: a recorded
// overlay edited to add resolved-events is carried without any --values, and must still
// be refused before anything is written.
func TestShardGroupsUpgradeJudgesTheDeployedConfiguration(t *testing.T) {
	originalInfra, originalDeployed := readLiveInfra, readDeployedShardGroups
	t.Cleanup(func() { readLiveInfra, readDeployedShardGroups = originalInfra, originalDeployed })
	three := int32(3)
	readLiveInfra = func(context.Context, string, string) (liveInfra, error) {
		return liveInfra{Broker: &appsv1.StatefulSet{Spec: appsv1.StatefulSetSpec{Replicas: &three}}}, nil
	}
	for _, tc := range []struct {
		name     string
		deployed *config.NatsShardGroupsConfiguration
		want     string
	}{
		{"inbound-only", &config.NatsShardGroupsConfiguration{Count: 3, Suffixes: []string{"inbound-events"}}, "start sharding resolved-events"},
		{"unsharded", nil, "start sharding resolved-events"},
		{"other-count", &config.NatsShardGroupsConfiguration{Count: 2, Suffixes: []string{"inbound-events", "resolved-events"}}, "change shardGroups.count from 2 to 3"},
		{"same", &config.NatsShardGroupsConfiguration{Count: 3, Suffixes: []string{"inbound-events", "resolved-events"}}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			readDeployedShardGroups = func(context.Context, string, string) (*config.NatsShardGroupsConfiguration, error) {
				return tc.deployed, nil
			}
			// No overlay intent: the block arrives the way a carried record does.
			st := &State{Instance: "prod", Profile: "default", Values: map[string]string{},
				ValuesOverlay: shardGroupsValues(resolvedBlock(3, "inbound-events", "resolved-events"))}
			err := precheckShardGroupsUpgrade(t.Context(), st)
			if tc.want == "" {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, tc.want)
			}
		})
	}
	// A deployed configuration that cannot be read is not a fresh instance.
	readDeployedShardGroups = func(context.Context, string, string) (*config.NatsShardGroupsConfiguration, error) {
		return nil, fmt.Errorf("forbidden")
	}
	st := &State{Instance: "prod", Profile: "default", Values: map[string]string{},
		ValuesOverlay: shardGroupsValues(resolvedBlock(3, "resolved-events"))}
	require.ErrorContains(t, precheckShardGroupsUpgrade(t.Context(), st), "forbidden")
	// Without event-processing the deployed shape is not consulted at all.
	st = &State{Instance: "prod", Profile: "ingest-only", EnableAreas: []string{"event-management"}, Values: map[string]string{},
		ValuesOverlay: shardGroupsValues(resolvedBlock(3, "resolved-events"))}
	var err error
	st.EnabledAreas, err = ResolveEnabledAreas(st.Profile, st.EnableAreas)
	require.NoError(t, err)
	require.NoError(t, precheckShardGroupsUpgrade(t.Context(), st))
}
