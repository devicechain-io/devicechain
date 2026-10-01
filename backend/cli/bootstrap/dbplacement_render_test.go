// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"io/fs"
	"reflect"
	"testing"

	"helm.sh/helm/v3/pkg/releaseutil"
	"sigs.k8s.io/yaml"

	assets "github.com/devicechain-io/dc-deploy"
)

// renderedToleration is one toleration as the cnpg-cluster chart renders it. Value
// and TolerationSeconds are pointers so an absent key reads as nil, not as "" or 0:
// an Exists toleration must carry no value at all.
type renderedToleration struct {
	Key               string  `json:"key"`
	Operator          string  `json:"operator"`
	Value             *string `json:"value"`
	Effect            string  `json:"effect"`
	TolerationSeconds *int    `json:"tolerationSeconds"`
}

// renderedPlacement is the part of a Cluster's spec.affinity placement touches.
type renderedPlacement struct {
	NodeSelector              map[string]string    `json:"nodeSelector"`
	Tolerations               []renderedToleration `json:"tolerations"`
	AdditionalPodAntiAffinity struct {
		Preferred []struct {
			Weight int `json:"weight"`
		} `json:"preferredDuringSchedulingIgnoredDuringExecution"`
	} `json:"additionalPodAntiAffinity"`
}

// renderedClusterAffinity renders the cnpg-cluster chart, as embedded in dcctl, with
// the given values laid over a minimal valid set, and returns the Cluster's
// spec.affinity both decoded and as a raw map (to tell an absent key from an empty
// one).
func renderedClusterAffinity(t *testing.T, extra map[string]interface{}) (map[string]interface{}, renderedPlacement) {
	t.Helper()
	src, err := fs.Sub(assets.OpenTofu(), "modules/cnpg-cluster/chart")
	if err != nil {
		t.Fatalf("locating the cnpg-cluster chart: %v", err)
	}
	ch, err := loadChartFS(src)
	if err != nil {
		t.Fatalf("loading the cnpg-cluster chart: %v", err)
	}
	vals := map[string]interface{}{
		"name":             "dc-rdb",
		"imageName":        "example/operand:test",
		"instances":        3,
		"aliasServiceName": "dc-postgresql",
		"storage":          map[string]interface{}{"size": "8Gi"},
		"bootstrap":        map[string]interface{}{"database": "dc", "owner": "devicechain", "secretName": "dc-rdb-app"},
	}
	for k, v := range extra {
		vals[k] = v
	}
	manifest, err := renderChartClientSide(context.Background(), ch, vals)
	if err != nil {
		t.Fatalf("rendering the cnpg-cluster chart: %v", err)
	}
	var raws []map[string]interface{}
	var typed []renderedPlacement
	for _, doc := range releaseutil.SplitManifests(manifest) {
		var obj struct {
			Kind string `json:"kind"`
			Spec struct {
				Affinity renderedPlacement `json:"affinity"`
			} `json:"spec"`
		}
		if err := yaml.Unmarshal([]byte(doc), &obj); err != nil {
			t.Fatalf("decoding a rendered document: %v\n%s", err, doc)
		}
		if obj.Kind != "Cluster" {
			continue
		}
		var raw struct {
			Spec struct {
				Affinity map[string]interface{} `json:"affinity"`
			} `json:"spec"`
		}
		if err := yaml.Unmarshal([]byte(doc), &raw); err != nil {
			t.Fatalf("decoding the Cluster: %v", err)
		}
		raws = append(raws, raw.Spec.Affinity)
		typed = append(typed, obj.Spec.Affinity)
	}
	if len(typed) != 1 {
		t.Fatalf("the chart rendered %d Clusters, want exactly 1", len(typed))
	}
	return raws[0], typed[0]
}

func strp(s string) *string { return &s }
func intp(i int) *int       { return &i }

// The databases go where the install was told to put them: the node selector lands as
// the Cluster's affinity.nodeSelector, and a database toleration joins the node-loss
// pair in ONE list. Read by value off the rendered Cluster; the cross-store primary
// preference must survive beside them.
func TestTheChartPlacesTheDatabaseOnTheChosenNodes(t *testing.T) {
	notReady := renderedToleration{Key: "node.kubernetes.io/not-ready", Operator: "Exists", Effect: "NoExecute", TolerationSeconds: intp(30)}
	unreachable := renderedToleration{Key: "node.kubernetes.io/unreachable", Operator: "Exists", Effect: "NoExecute", TolerationSeconds: intp(30)}
	dedicated := renderedToleration{Key: "dedicated", Operator: "Equal", Value: strp("database"), Effect: "NoSchedule"}
	selector := map[string]interface{}{"devicechain.io/pool": "database"}
	dedicatedVal := map[string]interface{}{"key": "dedicated", "operator": "Equal", "value": "database", "effect": "NoSchedule"}

	for _, tc := range []struct {
		name         string
		values       map[string]interface{}
		wantSelector map[string]string // nil: the key must be absent
		wantTols     []renderedToleration
		// wantTolsKey: whether spec.affinity carries a tolerations key at all.
		wantTolsKey bool
	}{
		{
			name: "placed, with the node-loss fuse",
			values: map[string]interface{}{
				"nodeLossTolerationSeconds": 30,
				"nodeSelector":              selector,
				"tolerations":               []interface{}{dedicatedVal},
			},
			wantSelector: map[string]string{"devicechain.io/pool": "database"},
			wantTols:     []renderedToleration{notReady, unreachable, dedicated},
			wantTolsKey:  true,
		},
		{
			name: "placed, with the fuse left at Kubernetes' default",
			values: map[string]interface{}{
				"nodeSelector": selector,
				"tolerations":  []interface{}{dedicatedVal},
			},
			wantSelector: map[string]string{"devicechain.io/pool": "database"},
			wantTols:     []renderedToleration{dedicated},
			wantTolsKey:  true,
		},
		{
			name:        "not placed, with the fuse",
			values:      map[string]interface{}{"nodeLossTolerationSeconds": 30},
			wantTols:    []renderedToleration{notReady, unreachable},
			wantTolsKey: true,
		},
		{
			name: "an Exists toleration carries no value",
			values: map[string]interface{}{
				"nodeSelector": selector,
				"tolerations":  []interface{}{map[string]interface{}{"key": "dedicated", "operator": "Exists", "effect": "NoSchedule"}},
			},
			wantSelector: map[string]string{"devicechain.io/pool": "database"},
			wantTols:     []renderedToleration{{Key: "dedicated", Operator: "Exists", Effect: "NoSchedule"}},
			wantTolsKey:  true,
		},
		{
			name:   "not placed, no fuse: neither key",
			values: map[string]interface{}{},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw, got := renderedClusterAffinity(t, tc.values)
			if _, has := raw["nodeSelector"]; has != (tc.wantSelector != nil) {
				t.Errorf("spec.affinity has a nodeSelector key: %t, want %t (affinity: %v)", has, tc.wantSelector != nil, raw)
			}
			if !reflect.DeepEqual(got.NodeSelector, tc.wantSelector) {
				t.Errorf("spec.affinity.nodeSelector = %v, want %v", got.NodeSelector, tc.wantSelector)
			}
			if _, has := raw["tolerations"]; has != tc.wantTolsKey {
				t.Errorf("spec.affinity has a tolerations key: %t, want %t", has, tc.wantTolsKey)
			}
			if !reflect.DeepEqual(got.Tolerations, tc.wantTols) {
				t.Errorf("spec.affinity.tolerations = %s, want %s", describeTols(got.Tolerations), describeTols(tc.wantTols))
			}
			if p := got.AdditionalPodAntiAffinity.Preferred; len(p) != 1 || p[0].Weight != 100 {
				t.Errorf("the cross-store primary preference did not survive placement: %+v", p)
			}
		})
	}
}

func describeTols(ts []renderedToleration) string {
	b, _ := yaml.Marshal(ts)
	return "\n" + string(b)
}
