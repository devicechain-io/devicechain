// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"context"
	"net/url"
	"regexp"
	"time"

	dcv1beta1 "github.com/devicechain-io/dc-k8s/api/v1beta1"
	"github.com/devicechain-io/dcctl/apiclient"
	"github.com/devicechain-io/dcctl/bootstrap"
	"github.com/spf13/cobra"
)

var listTargetInstances = bootstrap.ListInstances

var legacyRelease = regexp.MustCompile(`^v?1\.(7|8)\.(0|[1-9][0-9]*)$`)

// recordedEndpointVersions is best effort: metadata failures leave discovery
// mandatory. No HTTP probe, client release or writer's DcctlVersion is consulted.
func recordedEndpointVersions(cmd *cobra.Command, endpoints ...string) []apiclient.EndpointVersion {
	instance, _ := cmd.Flags().GetString("instance")
	return instanceEndpointVersions(cmd, instance, "", endpoints...)
}

func instanceEndpointVersions(cmd *cobra.Command, instance, hostHeader string, endpoints ...string) []apiclient.EndpointVersion {
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	kubeContext, _ := cmd.Flags().GetString("kube-context")
	ids := []string{instance}
	if instance == "" {
		// --server commands can identify a previously installed instance by its
		// recorded host. Local records supply cluster bindings, never versions.
		known, err := listTargetInstances()
		if err != nil {
			return nil
		}
		ids = nil
		for _, k := range known {
			if k.HasRecord && k.Err == nil && !k.Destroying && k.DestroyingErr == nil {
				ids = append(ids, k.Instance)
			}
		}
	}
	var instances []*dcv1beta1.Instance
	for _, id := range ids {
		binding, source := resolveTargetBinding(bootstrap.Options{Instance: id, KubeContext: kubeContext})
		if source == bootstrap.BindingUnreadable || source == bootstrap.BindingConflict {
			continue
		}
		inst, err := readTargetInstanceCR(ctx, binding.KubeContext, id)
		if err == nil && inst != nil {
			instances = append(instances, inst)
		}
	}
	var versions []apiclient.EndpointVersion
	for _, endpoint := range endpoints {
		u, err := url.Parse(endpoint)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
			continue
		}
		host := u.Host
		if hostHeader != "" {
			host = hostHeader
		}
		matches, version := 0, ""
		for _, inst := range instances {
			if inst.Spec.Host == "" || inst.Spec.Host != host || inst.Spec.TLS != (u.Scheme == "https") {
				continue
			}
			matches++
			version = recordedRelease(inst)
		}
		// Ambiguous hosts cannot establish an endpoint's release, even if one
		// matching instance has a convenient legacy version.
		if matches == 1 && legacyRelease.MatchString(version) {
			versions = append(versions, apiclient.EndpointVersion{Endpoint: endpoint, Version: version})
		}
	}
	return versions
}

func recordedRelease(inst *dcv1beta1.Instance) string {
	if inst.Status.Version != nil {
		// A current observation takes precedence over intent, including an
		// empty or mixed observation. Never use a stale status as evidence.
		if inst.Status.ObservedGeneration != inst.Generation {
			return ""
		}
		return inst.Status.Version.Running
	}
	// Older operators did not report a version status. Their declaration is
	// the release recorded by bootstrap/upgrade, independent of discovery.
	return inst.Spec.ImageVersion
}
