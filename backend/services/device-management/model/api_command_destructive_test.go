// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"fmt"
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
)

// A raw lwm2m.execute of the Factory Reset resource is refused at the enqueue gate on every entry
// path — single, batch (and REACT, which enqueues through the same gate) — whether or not the
// device's profile constrains its vocabulary, and under every spelling of the path. The code is
// written as a literal because it crosses a service boundary: command-delivery relays it and REACT
// decides from it not to retry.
func TestRawFactoryResetExecuteIsRefused(t *testing.T) {
	ctx := core.WithTenant(context.Background(), "A")

	paths := []string{"/3/0/5", "3/0/5", "/3/0/5/", "/3/00/5", "/3/1/5", "/3/0/5/0"}

	for _, vocab := range []string{"free-form", "declares lwm2m.execute"} {
		api := newEnqueueTestApi(t)
		var defs []*CommandDefinition
		if vocab != "free-form" {
			defs = []*CommandDefinition{defWithSchema(t, "lwm2m.execute", nil), defWithSchema(t, "lwm2m.read", nil)}
		}
		seedDeviceWithCommands(t, api, ctx, "dev", defs)

		for _, p := range paths {
			payload := []byte(fmt.Sprintf(`{"path":%q}`, p))
			t.Run(vocab+" single "+p, func(t *testing.T) {
				v, err := api.ValidateCommandEnqueue(ctx, "dev", "lwm2m.execute", payload)
				if err != nil {
					t.Fatalf("validate: %v", err)
				}
				if v.Allowed || string(v.Code) != "COMMAND_DESTRUCTIVE_REFUSED" {
					t.Fatalf("allowed=%v code=%q, want COMMAND_DESTRUCTIVE_REFUSED", v.Allowed, v.Code)
				}
			})
			t.Run(vocab+" batch "+p, func(t *testing.T) {
				refusals, err := api.ValidateCommandEnqueueBatch(ctx, []string{"dev"}, "lwm2m.execute", payload)
				if err != nil {
					t.Fatalf("batch: %v", err)
				}
				if len(refusals) != 1 || string(refusals[0].Code) != "COMMAND_DESTRUCTIVE_REFUSED" {
					t.Fatalf("refusals = %+v, want dev refused with COMMAND_DESTRUCTIVE_REFUSED", refusals)
				}
			})
		}

		for _, amb := range []string{`{"path":"/3/0/5","Path":"/3/0/9"}`, `{"path":"/3/0/9","PATH":"/3/0/5"}`} {
			v, err := api.ValidateCommandEnqueue(ctx, "dev", "lwm2m.execute", []byte(amb))
			if err != nil || v.Allowed || string(v.Code) != "COMMAND_DESTRUCTIVE_REFUSED" {
				t.Fatalf("%s single (%s): %+v, %v", amb, vocab, v, err)
			}
			refusals, err := api.ValidateCommandEnqueueBatch(ctx, []string{"dev"}, "lwm2m.execute", []byte(amb))
			if err != nil || len(refusals) != 1 || string(refusals[0].Code) != "COMMAND_DESTRUCTIVE_REFUSED" {
				t.Fatalf("%s batch (%s): %+v, %v", amb, vocab, refusals, err)
			}
		}

		// The counterweights: Reboot and other executes still pass, and so does a read of /3/0/5.
		for _, c := range []struct{ key, payload string }{
			{"lwm2m.execute", `{"path":"/3/0/4"}`},
			{"lwm2m.execute", `{"path":"/5/0/2"}`},
			{"lwm2m.execute", `{"path":"/3/0/50"}`},
			{"lwm2m.read", `{"path":"/3/0/5"}`},
		} {
			v, err := api.ValidateCommandEnqueue(ctx, "dev", c.key, []byte(c.payload))
			if err != nil || !v.Allowed {
				t.Fatalf("%s %s (%s) was refused: %+v, %v", c.key, c.payload, vocab, v, err)
			}
		}
	}
}
