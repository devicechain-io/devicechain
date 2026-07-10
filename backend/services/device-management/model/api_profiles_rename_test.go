// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/stretchr/testify/assert"
)

// A profile token is immutable once the profile has published versions (ADR-051 slice 4b-3):
// a rename would silently unscope every rule already published under the old token. A rename
// BEFORE any publish is allowed, and a non-rename update after publish is unaffected.
func TestUpdateDeviceProfile_RejectsRenameAfterPublish(t *testing.T) {
	api := newPublishEmitTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")
	seedProfileWithRule(t, api, ctx, "prof", "hot", true)

	// Before any publish, a rename is allowed.
	if _, err := api.UpdateDeviceProfile(ctx, "prof", &DeviceProfileCreateRequest{Token: "prof2"}); err != nil {
		t.Fatalf("rename before publish should be allowed: %v", err)
	}

	// Publish creates a version, freezing the token into the version key.
	if _, err := api.PublishDeviceProfile(ctx, "prof2", nil, nil, "tester"); err != nil {
		t.Fatalf("publish: %v", err)
	}

	// After publish, a rename is rejected.
	_, err := api.UpdateDeviceProfile(ctx, "prof2", &DeviceProfileCreateRequest{Token: "prof3"})
	assert.Error(t, err, "rename after publish must be rejected")

	// A non-rename update (same token) after publish is still allowed.
	_, err = api.UpdateDeviceProfile(ctx, "prof2", &DeviceProfileCreateRequest{Token: "prof2", Name: strptr("Renamed Display Only")})
	assert.NoError(t, err, "a same-token update after publish must still work")
}
