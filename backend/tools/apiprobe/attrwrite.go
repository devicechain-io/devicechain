// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"
	"io"
	"math/rand"
	"os"
	"strings"
	"time"

	"github.com/devicechain-io/dc-microservice/userclient"
)

// codeAttributeWriteBusy is the extensions.code device-management answers when the device attribute
// writer gave up after its own bounded attempts under contention. It is retryable BY DESIGN.
const codeAttributeWriteBusy = "ATTRIBUTE_WRITE_BUSY"

// The retry bound for a BUSY write. The attrlock phase deliberately writes one device from two pod
// versions at once, so BUSY is an expected outcome there, not a failure. Variables, not constants, so
// the tests can shrink the wait.
var (
	attrBusyMaxTries   = 10
	attrBusyBackoffMin = 50 * time.Millisecond
	attrBusyBackoffMax = 500 * time.Millisecond
)

// attrBusyBackoff is a jittered wait in [min, max].
func attrBusyBackoff() time.Duration {
	if attrBusyBackoffMax <= attrBusyBackoffMin {
		return attrBusyBackoffMin
	}
	return attrBusyBackoffMin + time.Duration(rand.Int63n(int64(attrBusyBackoffMax-attrBusyBackoffMin)+1))
}

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
//
// 🔴 ONLY ATTRIBUTE_WRITE_BUSY IS RETRIED (up to attrBusyMaxTries attempts per write, jittered backoff),
// and only when EVERY error in the response carries that code. "All writes accepted" therefore means
// accepted, possibly after BUSY retries; the retries are counted and printed. Any other error fails at once.
func runAttrWrite(ctx context.Context, argv []string) error {
	return attrWriteTo(ctx, argv, os.Stdout)
}

func attrWriteTo(ctx context.Context, argv []string, stdout io.Writer) error {
	fs := flagSetFor("attrwrite")
	var c connection
	c.bind(fs)
	var receipt, url, device, key string
	var count int
	var pause time.Duration
	var del bool
	fs.StringVar(&receipt, "receipt", "", "path to the receipt seed wrote (required)")
	fs.StringVar(&url, "url", "", "GraphQL endpoint to write to (default: device-management behind the ingress)")
	fs.StringVar(&device, "device", "apiprobe-device", "token of the device whose attribute is rewritten")
	fs.StringVar(&key, "key", probeFacetKey, "the SHARED attribute key written (default: the seeded facet)")
	fs.BoolVar(&del, "delete", false, "DELETE the attribute instead of writing it; each delete must find the row")
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
	if strings.TrimSpace(key) == "" {
		return failWith(exitSetup, "--key must not be empty")
	}
	doc, vars := attrWriteMutation, map[string]any{"req": map[string]any{
		"entityType": "device",
		"entity":     device,
		"scope":      probeAttrScope,
		"attrKey":    key,
		"valueType":  probeValueType,
		"value":      probeFacetValue,
	}}
	if del {
		doc, vars = bcUnsetDoc, map[string]any{"entityType": "device", "entity": device, "scope": probeAttrScope, "attrKey": key}
	}
	retries := 0
	for i := 1; i <= count; i++ {
		var out struct {
			SetEntityAttribute struct {
				AttrKey string `json:"attrKey"`
			} `json:"setEntityAttribute"`
			DeleteEntityAttribute *bool `json:"deleteEntityAttribute"`
		}
		for try := 1; ; try++ {
			err := session.Query(ctx, url, doc, vars, &out)
			if err == nil {
				break
			}
			if !userclient.AllHaveCode(err, codeAttributeWriteBusy) {
				return failWith(exitMismatch, "write %d of %d to %s failed: %w", i, count, url, err)
			}
			if try >= attrBusyMaxTries {
				return failWith(exitMismatch, "write %d of %d to %s still %s after %d tries: %w",
					i, count, url, codeAttributeWriteBusy, try, err)
			}
			retries++
			select {
			case <-ctx.Done():
				return failWith(exitSetup, "write %d of %d to %s: %w", i, count, url, ctx.Err())
			case <-time.After(attrBusyBackoff()):
			}
		}
		switch {
		case del && out.DeleteEntityAttribute == nil:
			return failWith(exitShape, "delete %d of %q answered no deleteEntityAttribute", i, key)
		case del && !*out.DeleteEntityAttribute:
			return failWith(exitMissing, "delete %d of %q answered false: there was no such attribute to delete", i, key)
		case !del && out.SetEntityAttribute.AttrKey != key:
			return failWith(exitShape, "write %d answered attrKey %q, want %q", i, out.SetEntityAttribute.AttrKey, key)
		}
		if pause > 0 && i < count {
			time.Sleep(pause)
		}
	}
	what := "write"
	if del {
		what = "delete"
	}
	fmt.Fprintf(stdout, "attrwrite: %d %s(s) of %q accepted by %s (%d %s retries)\n", count, what, key, url, retries, codeAttributeWriteBusy)
	return nil
}
