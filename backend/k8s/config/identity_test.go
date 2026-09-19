// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package config

import "testing"

// TestTheIdentityIsStampedOnEveryRenderedObject pins that the annotation
// actually reaches the stream, since everything below compares values read back
// out of it rather than recomputed.
func TestTheIdentityIsStampedOnEveryRenderedObject(t *testing.T) {
	out, err := RenderOperator("localhost:5000/operator:dev")
	if err != nil {
		t.Fatal(err)
	}
	objs, err := decodeAnnotated(out)
	if err != nil {
		t.Fatal(err)
	}
	if len(objs) == 0 {
		t.Fatal("the overlay rendered no documents")
	}
	for _, o := range objs {
		if o.identity == "" {
			t.Errorf("%s/%s carries no identity annotation", o.kind, o.name)
		}
	}
}

// TestTheIdentityDoesNotMoveWithTheIMAGE is the counterweight that makes the
// guard usable. A cluster installed with one registry and a dcctl run naming
// another are carrying the SAME schema, and a digest that disagreed about that
// would refuse a bootstrap over a difference which cannot affect it.
func TestTheIdentityDoesNotMoveWithTheImage(t *testing.T) {
	a, err := RenderOperator("localhost:5000/operator:dev")
	if err != nil {
		t.Fatal(err)
	}
	b, err := RenderOperator("ghcr.io/devicechain-io/operator:v9.9.9")
	if err != nil {
		t.Fatal(err)
	}
	ida, err := IdentityOf(a)
	if err != nil {
		t.Fatal(err)
	}
	idb, err := IdentityOf(b)
	if err != nil {
		t.Fatal(err)
	}
	if ida != idb {
		t.Fatalf("the identity moved with the image reference: %s vs %s", ida, idb)
	}
}

// TestTheIdentityMovesWithTheSCHEMA is the other half, and without it the test
// above is satisfied by an identity that is constant — which would compare
// equal everywhere and refuse nothing.
func TestTheIdentityMovesWithTheSchema(t *testing.T) {
	base, err := RenderOperator("")
	if err != nil {
		t.Fatal(err)
	}
	id, err := IdentityOf(base)
	if err != nil {
		t.Fatal(err)
	}

	res, err := renderOverlay("")
	if err != nil {
		t.Fatal(err)
	}
	var touched bool
	for _, r := range res.Resources() {
		if r.GetKind() != "CustomResourceDefinition" {
			continue
		}
		labels := r.GetLabels()
		if labels == nil {
			labels = map[string]string{}
		}
		labels["pretend-the-schema-changed"] = "yes"
		if err := r.SetLabels(labels); err != nil {
			t.Fatal(err)
		}
		touched = true
		break
	}
	if !touched {
		t.Fatal("no CustomResourceDefinition in the rendered overlay to modify")
	}
	moved, err := identityOf(res)
	if err != nil {
		t.Fatal(err)
	}
	if moved == id {
		t.Fatal("the identity did not move when a CRD did, so it cannot tell two schemas apart")
	}
}

// TestIdentityOfRefusesAStreamWithNoCRD keeps the reader fail-closed: an empty
// answer here would be a value every cluster agrees on.
func TestIdentityOfRefusesAStreamWithNoCRD(t *testing.T) {
	if _, err := IdentityOf([]byte("apiVersion: v1\nkind: Namespace\nmetadata:\n  name: dc-k8s-system\n")); err == nil {
		t.Fatal("expected a refusal for a stream carrying no CustomResourceDefinition")
	}
}
