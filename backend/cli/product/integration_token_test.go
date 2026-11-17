// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package product

import (
	"context"
	"strings"
	"testing"
)

func TestRotateIntegrationToken(t *testing.T) {
	f := newFake(t)
	f.integrations["lorawan"] = map[string]any{"token": "lorawan"}
	got, err := RotateIntegrationToken(context.Background(), f, ep(), "lorawan")
	if err != nil || got != "issued-1" || f.tokensIssued != 1 {
		t.Fatalf("rotation: token=%q err=%v issued=%d", got, err, f.tokensIssued)
	}
	call := f.calls[0]
	if call.endpoint != ep().DeviceManagement || call.vars["token"] != "lorawan" || len(call.vars) != 1 {
		t.Fatalf("wrong endpoint or variables: %+v", call)
	}
	if errs := servedSchema(t, "device-management").ValidateWithVariables(call.query, call.vars); len(errs) != 0 {
		t.Fatal(errs)
	}
	got, err = RotateIntegrationToken(context.Background(), f, ep(), "missing")
	if err == nil || !strings.Contains(err.Error(), `no integration "missing"`) || got != "" {
		t.Fatalf("unknown integration: token=%q err=%v", got, err)
	}
}
