// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package controllers_test

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"

	dcv1 "github.com/devicechain-io/dc-k8s/api/v1"
	dcv1beta1 "github.com/devicechain-io/dc-k8s/api/v1beta1"
)

// 🔴 THE INSTANCE CRD SERVES v1 AND v1beta1 WITH NO CONVERSION WEBHOOK, SO THE
// API SERVER MOVES AN OBJECT BETWEEN THEM BY PRUNING IT TO THE SCHEMA OF THE
// VERSION IT IS READ OR WRITTEN THROUGH. Anything one schema lacks is silently
// dropped. These tests run against a real API server because that pruning, and
// the CEL rules the two versions each carry, are behaviours of it and not of any
// manifest a structural test can read.

const (
	versionV1beta1 = "v1beta1"
	versionV1      = "v1"
)

func gvkOf(version string) schema.GroupVersionKind {
	return schema.GroupVersionKind{Group: "core.devicechain.io", Version: version, Kind: "Instance"}
}

// asMap decodes any JSON-serialisable value to the shape the API server hands
// back, so a typed fixture and an unstructured read compare without caring which
// integer width either side used.
func asMap(t *testing.T, v interface{}) map[string]interface{} {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]interface{}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func norm(t *testing.T, v interface{}) interface{} {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var out interface{}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// createThrough creates an Instance whose spec is the given document, through the
// named version.
func createThrough(ctx context.Context, c client.Client, version, name string, spec map[string]interface{}) error {
	u := &unstructured.Unstructured{Object: map[string]interface{}{"spec": spec}}
	u.SetGroupVersionKind(gvkOf(version))
	u.SetName(name)
	return c.Create(ctx, u)
}

func getThrough(ctx context.Context, c client.Client, version, name string) (*unstructured.Unstructured, error) {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(gvkOf(version))
	return u, c.Get(ctx, client.ObjectKey{Name: name}, u)
}

// mutateThrough reads the object through the named version, lets fn edit its spec
// map, and writes it back through that same version.
func mutateThrough(ctx context.Context, c client.Client, version, name string, fn func(spec map[string]interface{})) error {
	u, err := getThrough(ctx, c, version, name)
	if err != nil {
		return err
	}
	spec, _ := u.Object["spec"].(map[string]interface{})
	fn(spec)
	return c.Update(ctx, u)
}

// instanceFixtures are valid specs that TOGETHER set every field. One fixture
// cannot: filesystem and s3 object stores are exclusive, and so are shardGroups
// and ha.
func instanceFixtures() map[string]dcv1beta1.InstanceSpec {
	yes := true
	no := false
	at := metav1.NewTime(time.Date(2026, 11, 22, 10, 30, 0, 0, time.UTC))
	return map[string]dcv1beta1.InstanceSpec{
		"minimal": {Provider: "local", Managed: false},
		"filesystem-ha": {
			Provider: "gke", Cluster: "prod-1", Managed: true,
			Profile: "full", ExtraFunctionalAreas: []string{"update-management", "mcp"},
			HA: true, Compact: true, Size: "compact", Sharding: "scale",
			Monitoring: true, CNPG: true, GrafanaSSO: true,
			Host: "dc.example.com", TLS: true,
			ImageRegistry: "registry.example.com/dc", ImageVersion: "v1.9.0",
			Restored: true, RestoredAt: &at,
			MQTTDeviceUplinks: &yes, MQTTFailClosed: &no,
			MQTTExposure: "gateway", MQTTHost: "mqtt.example.com",
			BrokerMaxConnections:   5000,
			TrustedProxies:         []string{"10.0.0.0/8", "192.168.0.0/16"},
			TrustedProxiesExplicit: true,
			ObjectStore: &dcv1beta1.ObjectStoreSpec{
				Backend: dcv1beta1.ObjectStoreFilesystem, Size: "20Gi",
				StorageClass: "fast", AccessMode: "ReadWriteMany",
			},
		},
		"s3-shardgroups": {
			Provider: "local", Cluster: "kind-dc", Managed: true,
			ShardGroups: &dcv1beta1.ShardGroupsSpec{
				Count:         2,
				Suffixes:      []string{"inbound-events", "resolved-events"},
				PlacementTags: map[string]dcv1beta1.ShardPlacementTags{"0": {"dc-nats-0"}, "1": {"dc-nats-1"}},
			},
			ObjectStore: &dcv1beta1.ObjectStoreSpec{
				Backend: dcv1beta1.ObjectStoreS3,
				S3: &dcv1beta1.S3ObjectStoreSpec{
					Bucket: "dc-objects", Region: "us-east-1", Endpoint: "https://s3.example.com",
					UsePathStyle:      true,
					CredentialsSecret: &dcv1beta1.SecretReference{Namespace: "dc-system", Name: "s3-creds"},
				},
			},
		},
	}
}

// fieldPaths lists the JSON path of every field of a type (struct fields of this
// module's API packages are followed, through pointers, slices and maps).
func fieldPaths(rt reflect.Type, prefix string, out map[string]bool) {
	for rt.Kind() == reflect.Ptr || rt.Kind() == reflect.Slice || rt.Kind() == reflect.Array {
		rt = rt.Elem()
	}
	if rt.Kind() != reflect.Struct || !strings.Contains(rt.PkgPath(), "dc-k8s/api") {
		return
	}
	for i := 0; i < rt.NumField(); i++ {
		name := strings.Split(rt.Field(i).Tag.Get("json"), ",")[0]
		if name == "" || name == "-" {
			continue
		}
		out[prefix+name] = true
		fieldPaths(rt.Field(i).Type, prefix+name+".", out)
	}
}

// setPaths lists the JSON path of every field a value actually sets.
func setPaths(rv reflect.Value, prefix string, out map[string]bool) {
	for rv.Kind() == reflect.Ptr {
		if rv.IsNil() {
			return
		}
		rv = rv.Elem()
	}
	switch rv.Kind() {
	case reflect.Slice:
		for i := 0; i < rv.Len(); i++ {
			setPaths(rv.Index(i), prefix, out)
		}
	case reflect.Struct:
		if !strings.Contains(rv.Type().PkgPath(), "dc-k8s/api") {
			return
		}
		for i := 0; i < rv.NumField(); i++ {
			name := strings.Split(rv.Type().Field(i).Tag.Get("json"), ",")[0]
			if name == "" || name == "-" || rv.Field(i).IsZero() {
				continue
			}
			out[prefix+name] = true
			setPaths(rv.Field(i), prefix+name+".", out)
		}
	}
}

func TestTheV1RoundTripFixturesSetEveryField(t *testing.T) {
	all := map[string]bool{}
	fieldPaths(reflect.TypeOf(dcv1beta1.InstanceSpec{}), "", all)
	set := map[string]bool{}
	for _, spec := range instanceFixtures() {
		setPaths(reflect.ValueOf(spec), "", set)
	}
	var missing []string
	for p := range all {
		if !set[p] {
			missing = append(missing, p)
		}
	}
	sort.Strings(missing)
	if len(missing) != 0 {
		t.Fatalf("the round-trip fixtures never set %v, so a field the schema dropped would go unseen. "+
			"Add it to a fixture.", missing)
	}
}

func statusFixture() dcv1beta1.InstanceStatus {
	at := metav1.NewTime(time.Date(2026, 11, 22, 11, 0, 0, 0, time.UTC))
	return dcv1beta1.InstanceStatus{
		ObservedGeneration: 3,
		Conditions: []metav1.Condition{
			{Type: dcv1beta1.ConditionReady, Status: metav1.ConditionTrue, Reason: "AllGood", Message: "ready", LastTransitionTime: at, ObservedGeneration: 3},
			{Type: dcv1beta1.ConditionWorkloadsAvailable, Status: metav1.ConditionFalse, Reason: "Starting", Message: "starting", LastTransitionTime: at},
		},
		Version: &dcv1beta1.ObservedVersion{Requested: "v1.9.0", Running: dcv1beta1.VersionMixed},
		Areas: []dcv1beta1.AreaStatus{
			{Name: "device-management", DesiredReplicas: 2, ReadyReplicas: 2, AvailableReplicas: 2, UpdatedReplicas: 1, Image: "reg/dm:v1.9.0"},
			{Name: "event-management", DesiredReplicas: 1},
		},
	}
}

func TestTheV1StatusFixtureSetsEveryField(t *testing.T) {
	all := map[string]bool{}
	fieldPaths(reflect.TypeOf(dcv1beta1.InstanceStatus{}), "", all)
	set := map[string]bool{}
	setPaths(reflect.ValueOf(statusFixture()), "", set)
	for p := range all {
		if !set[p] && !strings.HasPrefix(p, "conditions.") { // metav1.Condition is not this module's type
			t.Errorf("the status fixture never sets %s", p)
		}
	}
}

// Create through one version and read through the other, in both directions, for
// every fixture, and require the spec to survive byte for byte. Then write status
// through /status in each version and read it through the other.
func TestAnInstanceRoundTripsBetweenV1AndV1beta1(t *testing.T) {
	c := withAPIServer(t)
	ctx := context.Background()

	for _, dir := range []struct{ write, read string }{
		{versionV1beta1, versionV1},
		{versionV1, versionV1beta1},
	} {
		for fname, fixture := range instanceFixtures() {
			name := fmt.Sprintf("rt-%s-%s-%s", dir.write, dir.read, fname)
			t.Run(name, func(t *testing.T) {
				want := asMap(t, fixture)
				if err := createThrough(ctx, c, dir.write, name, want); err != nil {
					t.Fatalf("creating through %s: %v", dir.write, err)
				}
				got, err := getThrough(ctx, c, dir.read, name)
				if err != nil {
					t.Fatalf("reading through %s: %v", dir.read, err)
				}
				if got.GetAPIVersion() != "core.devicechain.io/"+dir.read {
					t.Errorf("read back as %s, want %s", got.GetAPIVersion(), dir.read)
				}
				if !reflect.DeepEqual(norm(t, got.Object["spec"]), norm(t, want)) {
					t.Errorf("the spec changed between %s and %s:\n  wrote: %v\n  read:  %v",
						dir.write, dir.read, norm(t, want), norm(t, got.Object["spec"]))
				}
			})
		}
	}

	t.Run("status written through /status is read through the other version", func(t *testing.T) {
		status := statusFixture()
		want := asMap(t, status)

		// v1beta1 writes, v1 reads.
		if err := createThrough(ctx, c, versionV1beta1, "st-b-to-1", asMap(t, instanceFixtures()["minimal"])); err != nil {
			t.Fatal(err)
		}
		b := &dcv1beta1.Instance{}
		if err := c.Get(ctx, client.ObjectKey{Name: "st-b-to-1"}, b); err != nil {
			t.Fatal(err)
		}
		b.Status = status
		if err := c.Status().Update(ctx, b); err != nil {
			t.Fatalf("status through v1beta1: %v", err)
		}
		got, err := getThrough(ctx, c, versionV1, "st-b-to-1")
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(norm(t, got.Object["status"]), norm(t, want)) {
			t.Errorf("status written through v1beta1 changed when read through v1:\n  wrote: %v\n  read:  %v",
				norm(t, want), norm(t, got.Object["status"]))
		}
		// ...and the spec was not touched by a status write.
		if !reflect.DeepEqual(norm(t, got.Object["spec"]), norm(t, asMap(t, instanceFixtures()["minimal"]))) {
			t.Errorf("a status write through v1beta1 changed the spec read through v1: %v", got.Object["spec"])
		}

		// v1 writes, v1beta1 reads, on an object with a rich spec so the status
		// write has plenty to wrongly prune.
		if err := createThrough(ctx, c, versionV1, "st-1-to-b", asMap(t, instanceFixtures()["filesystem-ha"])); err != nil {
			t.Fatal(err)
		}
		v1 := &dcv1.Instance{}
		if err := c.Get(ctx, client.ObjectKey{Name: "st-1-to-b"}, v1); err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(status)
		if err := json.Unmarshal(raw, &v1.Status); err != nil {
			t.Fatal(err)
		}
		if err := c.Status().Update(ctx, v1); err != nil {
			t.Fatalf("status through v1: %v", err)
		}
		got, err = getThrough(ctx, c, versionV1beta1, "st-1-to-b")
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(norm(t, got.Object["status"]), norm(t, want)) {
			t.Errorf("status written through v1 changed when read through v1beta1:\n  wrote: %v\n  read:  %v",
				norm(t, want), norm(t, got.Object["status"]))
		}
		if !reflect.DeepEqual(norm(t, got.Object["spec"]), norm(t, asMap(t, instanceFixtures()["filesystem-ha"]))) {
			t.Errorf("a status write through v1 changed the spec read through v1beta1: %v", got.Object["spec"])
		}
	})

	t.Run("a typed v1 client reads what v1beta1 wrote", func(t *testing.T) {
		fixture := instanceFixtures()["filesystem-ha"]
		if err := c.Create(ctx, &dcv1beta1.Instance{ObjectMeta: metav1.ObjectMeta{Name: "typed-v1"}, Spec: fixture}); err != nil {
			t.Fatal(err)
		}
		v1 := &dcv1.Instance{}
		if err := c.Get(ctx, client.ObjectKey{Name: "typed-v1"}, v1); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(norm(t, v1.Spec), norm(t, fixture)) {
			t.Errorf("the typed v1 spec differs from what v1beta1 wrote:\n  %v\n  %v", norm(t, v1.Spec), norm(t, fixture))
		}
		list := &dcv1.InstanceList{}
		if err := c.List(ctx, list); err != nil {
			t.Fatalf("listing through v1: %v", err)
		}
		if len(list.Items) == 0 {
			t.Error("listing through v1 returned nothing")
		}
	})
}

// celCase is one transition and whether the API server must refuse it.
type celCase struct {
	name    string
	drop    []string // keys removed from the base spec before creating
	mutate  func(spec map[string]interface{})
	refused bool
}

func celBase() map[string]interface{} {
	return map[string]interface{}{
		"provider": "local", "cluster": "kind-dc", "managed": true, "profile": "default",
		"sharding": "scale", "restored": true, "restoredAt": "2026-11-22T10:30:00Z",
		"objectStore": map[string]interface{}{"backend": "filesystem", "size": "10Gi"},
	}
}

func celCases() []celCase {
	return []celCase{
		// Controls: what must still be ACCEPTED, so a refusal below is the rule's and
		// not a malformed object's.
		{"an ordinary change is accepted", nil, func(s map[string]interface{}) { s["profile"] = "full"; s["imageVersion"] = "v1.9.1" }, false},
		{"the object store size may change", nil, func(s map[string]interface{}) { s["objectStore"].(map[string]interface{})["size"] = "20Gi" }, false},
		{"an unchanged update is accepted", nil, func(s map[string]interface{}) {}, false},

		{"cluster changed", nil, func(s map[string]interface{}) { s["cluster"] = "elsewhere" }, true},
		{"cluster removed", nil, func(s map[string]interface{}) { delete(s, "cluster") }, true},
		{"cluster added to an instance with none", []string{"cluster"}, func(s map[string]interface{}) { s["cluster"] = "kind-dc" }, true},

		{"sharding changed", nil, func(s map[string]interface{}) { s["sharding"] = "other" }, true},
		{"sharding removed", nil, func(s map[string]interface{}) { delete(s, "sharding") }, true},
		{"sharding added to an instance with none", []string{"sharding"}, func(s map[string]interface{}) { s["sharding"] = "scale" }, true},

		{"restored unset", nil, func(s map[string]interface{}) { s["restored"] = false }, true},
		{"restored removed", nil, func(s map[string]interface{}) { delete(s, "restored"); delete(s, "restoredAt") }, true},
		{"restoredAt kept while restored is removed", nil, func(s map[string]interface{}) { delete(s, "restored") }, true},

		{"objectStore removed", nil, func(s map[string]interface{}) { delete(s, "objectStore") }, true},
		{"objectStore backend changed", nil, func(s map[string]interface{}) {
			s["objectStore"] = map[string]interface{}{"backend": "s3", "s3": map[string]interface{}{"bucket": "b"}}
		}, true},

		{"required provider deleted", nil, func(s map[string]interface{}) { delete(s, "provider") }, true},
		{"provider changed", nil, func(s map[string]interface{}) { s["provider"] = "gke" }, true},
		{"required managed deleted", nil, func(s map[string]interface{}) { delete(s, "managed") }, true},
		{"managed flipped", nil, func(s map[string]interface{}) { s["managed"] = false }, true},
	}
}

// Every transition the single-version tests refuse is refused across versions
// too: created through one, mutated through the other. The same-version pairs are
// the control that the refusal is the rule's.
func TestTheCELRulesHoldAcrossV1AndV1beta1(t *testing.T) {
	c := withAPIServer(t)
	ctx := context.Background()

	n := 0
	for _, pair := range []struct{ create, mutate string }{
		{versionV1beta1, versionV1},
		{versionV1, versionV1beta1},
		{versionV1beta1, versionV1beta1},
		{versionV1, versionV1},
	} {
		for _, tc := range celCases() {
			n++
			name := fmt.Sprintf("cel-%d", n)
			t.Run(fmt.Sprintf("create %s, mutate %s: %s", pair.create, pair.mutate, tc.name), func(t *testing.T) {
				base := celBase()
				for _, k := range tc.drop {
					delete(base, k)
				}
				if err := createThrough(ctx, c, pair.create, name, base); err != nil {
					t.Fatalf("creating the base object through %s: %v", pair.create, err)
				}
				err := mutateThrough(ctx, c, pair.mutate, name, tc.mutate)
				switch {
				case tc.refused && err == nil:
					t.Fatalf("a transition the rules refuse was ACCEPTED through %s on an object created through %s", pair.mutate, pair.create)
				case tc.refused && !apierrors.IsInvalid(err):
					t.Fatalf("refused, but not as a validation error: %v", err)
				case !tc.refused && err != nil:
					t.Fatalf("a legitimate change was refused through %s on an object created through %s: %v", pair.mutate, pair.create, err)
				}
			})
		}
	}
}

// The create-time rules too: an invalid object is refused through either version.
func TestTheCELRulesRefuseAnInvalidCreateThroughEitherVersion(t *testing.T) {
	c := withAPIServer(t)
	ctx := context.Background()
	bad := map[string]func(map[string]interface{}){
		"restoredAt without restored": func(s map[string]interface{}) { delete(s, "restored") },
		"mqttExposure without a host": func(s map[string]interface{}) { s["mqttExposure"] = "gateway" },
		"shardGroups with ha": func(s map[string]interface{}) {
			s["ha"] = true
			s["shardGroups"] = map[string]interface{}{"count": 2, "suffixes": []interface{}{"inbound-events"}}
		},
		"no provider": func(s map[string]interface{}) { delete(s, "provider") },
	}
	n := 0
	for _, v := range []string{versionV1beta1, versionV1} {
		for what, plant := range bad {
			n++
			spec := celBase()
			plant(spec)
			err := createThrough(ctx, c, v, fmt.Sprintf("bad-%d", n), spec)
			if err == nil {
				t.Errorf("%s was accepted through %s", what, v)
			} else if !apierrors.IsInvalid(err) {
				t.Errorf("%s through %s was refused, but not as a validation error: %v", what, v, err)
			}
		}
	}
}
