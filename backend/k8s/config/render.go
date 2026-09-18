// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"embed"
	"fmt"
	"io/fs"
	"strings"

	"sigs.k8s.io/kustomize/api/krusty"
	"sigs.k8s.io/kustomize/api/resmap"
	"sigs.k8s.io/kustomize/kyaml/filesys"
)

// overlay embeds the full operator kustomize tree so the rendered manifests can
// be produced in-process — no source checkout and no `kustomize`/`kubectl`
// binary at runtime. config/default references ../crd, ../rbac and ../manager,
// so all four sibling directories are embedded.
//
//go:embed default crd rbac manager
var overlay embed.FS

// RenderOperator renders the config/default overlay exactly as `make deploy`
// does — namespace (dc-k8s-system), name prefix (dc-k8s-), CRDs, RBAC and the
// controller Deployment — with the manager image set to the given reference.
// It returns a multi-document YAML stream ready to apply. An empty image leaves
// the placeholder (controller:latest) in place.
func RenderOperator(image string) ([]byte, error) {
	res, err := renderOverlay(image)
	if err != nil {
		return nil, err
	}
	// Stamped here rather than by each caller, so that everything dcctl applies
	// carries it without anyone having to remember.
	//
	// 🔴 `make deploy` DOES NOT GO THROUGH HERE — it runs the kustomize CLI over
	// config/default and pipes it to kubectl (see the Makefile), so an operator
	// installed that way carries NO identity at all. That is the maintainer path,
	// and the guards must read an absent annotation as "an operator I cannot
	// vouch for" rather than as a mismatch: the two deserve different messages,
	// because one means "run dcctl install" and the other means "you installed
	// this by hand and dcctl will not second-guess you".
	if err := stampIdentity(res); err != nil {
		return nil, err
	}
	return res.AsYaml()
}

// renderOverlay produces the unstamped resource map. It is separate from
// RenderOperator so the identity's own tests can render the overlay, change a
// CRD, and re-digest it — exercising the same path the stamp uses rather than a
// second copy of it that could drift.
func renderOverlay(image string) (resmap.ResMap, error) {
	fsys := filesys.MakeFsInMemory()
	if err := fs.WalkDir(overlay, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		b, rerr := overlay.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		return fsys.WriteFile(path, b)
	}); err != nil {
		return nil, fmt.Errorf("staging operator overlay: %w", err)
	}

	if image != "" {
		if err := setManagerImage(fsys, image); err != nil {
			return nil, err
		}
	}

	// Legacy reorder sorts Namespaces and CRDs ahead of the resources that
	// depend on them. kustomize-api defaults to no reordering (input order),
	// which would emit the dc-k8s-system ServiceAccount before the Namespace
	// that holds it; applying that stream directly (no kubectl kind-sorting)
	// then fails with "namespace not found".
	opts := krusty.MakeDefaultOptions()
	opts.Reorder = krusty.ReorderOptionLegacy
	res, err := krusty.MakeKustomizer(opts).Run(fsys, "default")
	if err != nil {
		return nil, fmt.Errorf("kustomize render: %w", err)
	}
	return res, nil
}

// setManagerImage injects an images override into the manager kustomization,
// mirroring `kustomize edit set image controller=<image>` in `make deploy`. The
// placeholder image name in config/manager/manager.yaml is "controller".
func setManagerImage(fsys filesys.FileSystem, image string) error {
	name, tag := splitImageRef(image)
	var b strings.Builder
	b.WriteString("resources:\n- manager.yaml\nimages:\n- name: controller\n")
	b.WriteString("  newName: " + name + "\n")
	if tag != "" {
		b.WriteString("  newTag: " + tag + "\n")
	}
	return fsys.WriteFile("manager/kustomization.yaml", []byte(b.String()))
}

// splitImageRef separates a reference into name and tag, honouring a registry
// port (localhost:5000/foo:tag) by only treating a colon after the last slash
// as the tag separator.
func splitImageRef(image string) (name, tag string) {
	if i := strings.LastIndex(image, ":"); i > strings.LastIndex(image, "/") {
		return image[:i], image[i+1:]
	}
	return image, ""
}
