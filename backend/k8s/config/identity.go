// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"

	"sigs.k8s.io/kustomize/api/resmap"
	"sigs.k8s.io/kustomize/kyaml/kio"
)

// IdentityAnnotation names the operator install a cluster is carrying.
//
// WHY AN ANNOTATION AND NOT A VERSION. `dcctl install` puts the operator on a
// cluster and `dcctl bootstrap`/`dcctl upgrade` have to answer "is the operator
// here the one I need?" before they write anything. The obvious source is the
// controller's image tag, and it cannot answer: the developer path stamps the
// literal `dev` on every build ever made, so two clusters a month apart carry
// the same tag and compare equal. A released tag orders, a dev tag does not, and
// a guard that can only fire on one of the two paths is a guard that is never
// exercised where it is being written.
//
// So the question asked is EQUALITY, not order, and the thing compared is a
// digest of what would be applied. Two dcctl binaries built from the same source
// agree by construction; any change to the operator's schema disagrees. Nothing
// has to parse a version, and the developer path is covered exactly as well as
// the released one.
//
// 🔴 THE DIGEST COVERS THE CRDs ONLY, AND THAT IS A DELIBERATE NARROWING. What
// a bootstrap depends on is the CustomResourceDefinition: it writes an Instance
// and the API server validates it against whatever schema is installed. It does
// NOT depend on the controller, which today fetches the Instance, logs that it
// saw it, and returns — the operator ships, runs, and does nothing. Including
// the controller Deployment would make the digest move whenever the image
// reference moves, so a cluster installed with `--registry` set and a bootstrap
// run without it would disagree about a schema that is in fact identical, and
// the refusal would fire on a difference that cannot hurt anyone.
//
// ⚠️ THE DAY THE CONTROLLER DOES SOMETHING, THIS IS WHERE TO TIGHTEN IT, and
// the narrowing above becomes wrong rather than merely conservative: a
// reconciler that acts on an Instance is a second reader whose version matters,
// and the digest then has to cover the workload too.
const IdentityAnnotation = "core.devicechain.io/operator-identity"

// stampIdentity computes the operator identity over the rendered CRDs and writes
// it onto every object in the stream.
//
// The digest is taken BEFORE anything is stamped, so it is a function of the
// schema alone and not of itself. Every object carries the result — the CRDs
// because they are what the digest describes, and the rest so that a reader
// looking at any part of the install can say which install it is looking at
// without having to know which object to ask.
func stampIdentity(res resmap.ResMap) error {
	id, err := identityOf(res)
	if err != nil {
		return err
	}
	for _, r := range res.Resources() {
		ann := r.GetAnnotations()
		if ann == nil {
			ann = map[string]string{}
		}
		ann[IdentityAnnotation] = id
		if err := r.SetAnnotations(ann); err != nil {
			return fmt.Errorf("stamping the operator identity on %s/%s: %w", r.GetKind(), r.GetName(), err)
		}
	}
	return nil
}

// identityOf digests the CustomResourceDefinitions in a rendered overlay.
//
// Resources are digested in name order rather than stream order. kustomize's
// ordering is stable today, but an identity that moved when the renderer
// reordered its output would refuse every bootstrap on a cluster nobody had
// touched — a guard whose failure mode is "everything is broken" teaches people
// to switch it off.
func identityOf(res resmap.ResMap) (string, error) {
	var crds []string
	for _, r := range res.Resources() {
		if r.GetKind() != "CustomResourceDefinition" {
			continue
		}
		y, err := r.AsYAML()
		if err != nil {
			return "", fmt.Errorf("reading rendered CRD %q: %w", r.GetName(), err)
		}
		crds = append(crds, r.GetName()+"\n"+string(y))
	}
	if len(crds) == 0 {
		// Fail rather than digest nothing. An empty digest is a perfectly good
		// hex string that every cluster would agree on, so a renderer that
		// stopped emitting CRDs would produce an identity that matches
		// everywhere and a guard that passes everything.
		return "", fmt.Errorf("the rendered operator overlay contains no CustomResourceDefinition, so it has no identity to stamp")
	}
	sort.Strings(crds)
	h := sha256.New()
	for _, c := range crds {
		_, _ = h.Write([]byte(c))
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// IdentityOf reads the operator identity back out of a rendered manifest stream.
//
// Callers compare the value this returns for the overlay they carry against the
// same annotation read off the CRD a cluster actually has. Both sides READ the
// annotation; neither recomputes the digest, so the two can never disagree about
// how it is derived.
func IdentityOf(manifests []byte) (string, error) {
	objs, err := decodeAnnotated(manifests)
	if err != nil {
		return "", err
	}
	for _, o := range objs {
		if o.kind != "CustomResourceDefinition" {
			continue
		}
		if o.identity == "" {
			return "", fmt.Errorf("the rendered CRD %q carries no %s annotation", o.name, IdentityAnnotation)
		}
		return o.identity, nil
	}
	return "", fmt.Errorf("the rendered operator overlay contains no CustomResourceDefinition")
}

// annotatedObject is the slice of a rendered document IdentityOf needs: enough
// to find the CRDs and read the stamp off one.
type annotatedObject struct {
	kind     string
	name     string
	identity string
}

// decodeAnnotated splits a multi-document YAML stream into the fields above.
//
// It reads the stream with the same library that rendered it rather than with a
// typed Kubernetes decoder, because the stream is CRDs and RBAC as well as a
// Deployment and a typed decode would need every scheme in it.
func decodeAnnotated(manifests []byte) ([]annotatedObject, error) {
	nodes, err := (&kio.ByteReader{Reader: bytes.NewReader(manifests)}).Read()
	if err != nil {
		return nil, fmt.Errorf("reading the rendered operator manifests: %w", err)
	}
	out := make([]annotatedObject, 0, len(nodes))
	for _, n := range nodes {
		meta, merr := n.GetMeta()
		if merr != nil {
			return nil, fmt.Errorf("reading a rendered document's metadata: %w", merr)
		}
		out = append(out, annotatedObject{
			kind:     meta.Kind,
			name:     meta.Name,
			identity: meta.Annotations[IdentityAnnotation],
		})
	}
	return out, nil
}
