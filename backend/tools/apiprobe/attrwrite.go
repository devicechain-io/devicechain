// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// attrWriteMutation is the one write the attrlock drill phase drives. It is the platform's own
// setEntityAttribute, selected as thinly as the schema allows: the phase is about whether the write
// SUCCEEDS on a given pod, not about what comes back.
const attrWriteMutation = `mutation($req:EntityAttributeSetRequest!){setEntityAttribute(request:$req){attrKey}}`

// runAttrWrite writes the seeded device's facet attribute --count times, to the GraphQL endpoint
// --url (the instance ingress by default), signed in as the seed's tenant identity.
//
// 🔴 IT REWRITES THE VALUE THE SEED ALREADY WROTE, byte for byte. The write still takes the device
// attribute lock, marks the transaction and bumps the generation, which is what the phase exercises,
// and it changes nothing a later read could disagree about. --url is how a write is sent to ONE pod:
// the rig port-forwards to a baseline-image pod and points this at it, which the ingress (a
// load-balanced front door) cannot do.
func runAttrWrite(ctx context.Context, argv []string) error {
	fs := flagSetFor("attrwrite")
	var c connection
	c.bind(fs)
	var receipt, url, device string
	var count int
	var pause time.Duration
	fs.StringVar(&receipt, "receipt", "", "path to the receipt seed wrote (required)")
	fs.StringVar(&url, "url", "", "GraphQL endpoint to write to (default: device-management behind the ingress)")
	fs.StringVar(&device, "device", "apiprobe-device", "token of the device whose attribute is rewritten")
	fs.IntVar(&count, "count", 1, "how many writes to make")
	fs.DurationVar(&pause, "pause", 0, "wait between writes")
	if err := fs.Parse(argv); err != nil {
		return failWith(exitSetup, "%w", err)
	}
	if strings.TrimSpace(receipt) == "" {
		return failWith(exitSetup, "--receipt is required")
	}
	if count < 1 {
		return failWith(exitSetup, "--count must be at least 1")
	}
	rec, err := readReceipt(receipt)
	if err != nil {
		return err
	}
	c.tenant = rec.Tenant
	if url == "" {
		url = c.areaURL("device-management")
	}
	session := c.session(rec.Identity)
	vars := map[string]any{"req": map[string]any{
		"entityType": "device",
		"entity":     device,
		"scope":      probeAttrScope,
		"attrKey":    probeFacetKey,
		"valueType":  probeValueType,
		"value":      probeFacetValue,
	}}
	for i := 1; i <= count; i++ {
		var out struct {
			SetEntityAttribute struct {
				AttrKey string `json:"attrKey"`
			} `json:"setEntityAttribute"`
		}
		if err := session.Query(ctx, url, attrWriteMutation, vars, &out); err != nil {
			return failWith(exitMismatch, "write %d of %d to %s failed: %w", i, count, url, err)
		}
		if out.SetEntityAttribute.AttrKey != probeFacetKey {
			return failWith(exitShape, "write %d answered attrKey %q, want %q", i, out.SetEntityAttribute.AttrKey, probeFacetKey)
		}
		if pause > 0 && i < count {
			time.Sleep(pause)
		}
	}
	fmt.Printf("attrwrite: %d write(s) accepted by %s\n", count, url)
	return nil
}
