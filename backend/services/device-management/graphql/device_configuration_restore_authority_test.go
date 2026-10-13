// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"testing"

	"github.com/devicechain-io/dc-microservice/auth"
)

// Restore and copy write a device's SHARED attributes, so a caller who may only read
// devices is refused both, and nothing is minted.
func TestRestoreAndCopyConfigurationRequireDeviceWrite(t *testing.T) {
	ctx := deviceConfigurationCtx(t)
	execProfileGql(t, ctx, setSharedAttrMutation, map[string]any{"key": "interval", "type": "LONG", "value": "30"})
	execProfileGql(t, ctx, setSharedAttrMutation, map[string]any{"key": "interval", "type": "LONG", "value": "60"})
	reader := withAuthorities(ctx, viewerBaseline...)

	r := &SchemaResolver{}
	if _, err := r.RestoreDeviceConfiguration(reader, struct {
		DeviceToken string
		Revision    int32
	}{DeviceToken: "dev", Revision: 1}); err != auth.ErrForbidden {
		t.Errorf("restoreDeviceConfiguration answered a read-only caller with %v, want ErrForbidden", err)
	}
	if _, err := r.CopyDeviceConfiguration(reader, struct {
		FromDeviceToken string
		ToDeviceToken   string
		FromRevision    *int32
	}{FromDeviceToken: "dev", ToDeviceToken: "dev"}); err != auth.ErrForbidden {
		t.Errorf("copyDeviceConfiguration answered a read-only caller with %v, want ErrForbidden", err)
	}
	data := execProfileGql(t, ctx, `{ deviceConfiguration(deviceToken: "dev") { desired { revision } } }`, nil)
	if want := `{"deviceConfiguration":{"desired":{"revision":2}}}`; string(data) != want {
		t.Fatalf("a refused restore changed the device: %s, want %s", data, want)
	}
}
