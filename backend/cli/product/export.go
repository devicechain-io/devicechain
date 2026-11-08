// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package product

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"sigs.k8s.io/yaml"
)

// Export reads the tenant's product into dir. It refuses, before writing anything, if any
// value in it is shaped like a secret. A kind whose area the instance does not serve fails
// the export unless tolerant, in which case it is skipped and returned in skipped.
//
// What is exported is the DRAFT of each profile, connector and dashboard -- the thing an
// author edits -- and apply publishes it again. A draft that was never published is exported
// all the same.
func Export(ctx context.Context, c Client, ep Endpoints, kinds KindSet, tolerant bool, dir, name string) (skipped []string, err error) {
	l, err := Load(ctx, c, ep, kinds, tolerant, nil)
	if err != nil {
		return nil, err
	}
	b, err := l.Bundle()
	if err != nil {
		return nil, err
	}
	b.Product.Name = name
	if name == "" {
		b.Product.Name = existingName(dir)
	}
	if err := b.Write(dir); err != nil {
		return nil, err
	}
	return l.Skipped, nil
}

// existingName keeps the label of a bundle that is being re-exported over.
func existingName(dir string) string {
	data, err := os.ReadFile(filepath.Join(dir, ManifestFile))
	if err != nil {
		return ""
	}
	var p Product
	if yaml.Unmarshal(data, &p) != nil {
		return ""
	}
	return p.Name
}

// PlanBundle reads the tenant for the kinds the bundle carries and returns what applying it would do.
func PlanBundle(ctx context.Context, c Client, ep Endpoints, b *Bundle, o Options) (*Plan, error) {
	kinds := KindSet{}
	if len(b.DeviceTypes) > 0 {
		kinds["device-types"] = true
	}
	// Profile children and the integrations' device-type scope are resolved by the server;
	// a profile read is needed whenever the bundle carries profiles.
	if len(b.Profiles) > 0 {
		kinds["profiles"] = true
	}
	if len(b.Integrations) > 0 {
		kinds["integrations"] = true
	}
	if len(b.Connectors) > 0 {
		kinds["connectors"] = true
	}
	var only map[string]bool
	if len(b.Dashboards) > 0 {
		kinds["dashboards"] = true
		only = map[string]bool{}
		for _, d := range b.Dashboards {
			only[d.Token] = true
		}
	}
	l, err := Load(ctx, c, ep, kinds, false, only)
	if err != nil {
		return nil, fmt.Errorf("reading the tenant: %w", err)
	}
	return BuildPlan(b, l, c, ep, o), nil
}
