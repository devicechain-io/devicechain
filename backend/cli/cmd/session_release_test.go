// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"context"
	"fmt"
	"testing"

	dcv1beta1 "github.com/devicechain-io/dc-k8s/api/v1beta1"
	"github.com/devicechain-io/dcctl/bootstrap"
	"github.com/spf13/cobra"
)

func TestReleaseEvidenceMatchesSelectedEndpoint(t *testing.T) {
	oldBinding, oldRead, oldList := resolveTargetBinding, readTargetInstanceCR, listTargetInstances
	t.Cleanup(func() {
		resolveTargetBinding, readTargetInstanceCR, listTargetInstances = oldBinding, oldRead, oldList
	})
	usable := true
	resolveTargetBinding = func(bootstrap.Options) (bootstrap.ClusterBinding, bootstrap.BindingSource) {
		if !usable {
			return bootstrap.ClusterBinding{}, bootstrap.BindingUnreadable
		}
		return bootstrap.ClusterBinding{KubeContext: "recorded"}, bootstrap.BindingFromRecord
	}
	inst := &dcv1beta1.Instance{Spec: dcv1beta1.InstanceSpec{Host: "dc.example", TLS: true, ImageVersion: "v1.8.3"}}
	readErr := error(nil)
	readTargetInstanceCR = func(context.Context, string, string) (*dcv1beta1.Instance, error) {
		return inst, readErr
	}
	known := []bootstrap.KnownInstance{{Instance: "prod", HasRecord: true,
		Record: bootstrap.InstanceRecord{DcctlVersion: "v1.7.0"}}}
	listTargetInstances = func() ([]bootstrap.KnownInstance, error) { return known, nil }
	c := &cobra.Command{}
	c.SetContext(context.Background())
	c.Flags().String("instance", "prod", "")
	endpoint := "https://dc.example/api/device-management/graphql"
	check := func(want string, urls ...string) {
		t.Helper()
		got := recordedEndpointVersions(c, urls...)
		if want == "" {
			if len(got) != 0 {
				t.Fatalf("unverified metadata authorized fallback: %+v", got)
			}
		} else if len(got) != 1 || got[0].Endpoint != endpoint || got[0].Version != want {
			t.Fatalf("want exact endpoint at %q; got %+v", want, got)
		}
	}
	check("v1.8.3", endpoint, "https://elsewhere.example/api/device-management/graphql")
	c.SetContext(nil) // command helpers can also be called before Cobra.Execute
	check("v1.8.3", endpoint)
	c.SetContext(context.Background())
	check("", "http://dc.example/api/device-management/graphql")
	inst.Status.Version = &dcv1beta1.ObservedVersion{Running: "v1.7.4"}
	check("v1.7.4", endpoint)
	inst.Status.Version.Running = "v1.9.0"
	check("", endpoint) // observed modern release overrides old spec
	inst.Status.Version.Running = "mixed"
	check("", endpoint)
	inst.Status.Version.Running = ""
	check("", endpoint)
	inst.Status.Version.Running = "v1.8.3"
	inst.Generation = 2
	check("", endpoint) // stale observation never authorizes legacy
	inst.Status.Version = nil
	check("v1.8.3", endpoint) // old operators have only the spec record
	for _, version := range []string{"", "v1.8.0-rc.1", "v1.8.0+build", "dev", "v1.6.9", "v2.7.0", "v1.9.0"} {
		inst.Spec.ImageVersion = version
		check("", endpoint)
	}
	inst.Spec.ImageVersion = "v1.8.3"
	usable = false
	check("", endpoint)
	usable = true
	readErr = fmt.Errorf("cluster unavailable")
	check("", endpoint)
	readErr = nil
	_ = c.Flags().Set("instance", "")
	check("v1.8.3", endpoint) // --server alone locates the installed record
	inst.Spec.ImageVersion = ""
	check("", endpoint) // writer's DcctlVersion must never be the instance version
	inst.Spec.ImageVersion = "v1.8.3"
	known = append(known, bootstrap.KnownInstance{Instance: "other", HasRecord: true})
	check("", endpoint) // ambiguous hosts refuse even when versions agree
	known = known[:1]
	got := instanceEndpointVersions(c, "prod", "dc.example",
		"https://127.0.0.1/api/user-management/admin/graphql")
	if len(got) != 1 || got[0].Version != "v1.8.3" {
		t.Fatalf("sim Gateway host override lost recorded version: %+v", got)
	}
	if got := instanceEndpointVersions(c, "prod", "foreign.example", endpoint); len(got) != 0 {
		t.Fatalf("foreign Host inherited recorded release: %+v", got)
	}
}
