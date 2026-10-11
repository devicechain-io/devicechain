// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-device-management/config"
	"github.com/devicechain-io/dc-device-management/model"
	esmodel "github.com/devicechain-io/dc-event-sources/model"
	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
)

// fullDeviceApi authenticates through the real (narrow) lookup, then hands the resolver the
// device as a FULL row read, every column. Resolving through it is the reference a narrow
// read is compared to: a resolved event must not depend on a device column the credential
// lookup leaves unread.
type fullDeviceApi struct {
	model.DeviceManagementApi
	rig *resolveRig
}

func (a fullDeviceApi) AuthenticateDevice(ctx context.Context, p *model.PresentedCredential, now time.Time) (*model.Device, error) {
	narrow, err := a.rig.capi.AuthenticateDevice(ctx, p, now)
	if err != nil {
		return nil, err
	}
	var full model.Device
	if err := a.rig.api.RDB.DB(ctx).First(&full, narrow.ID).Error; err != nil {
		return nil, err
	}
	return &full, nil
}

// A resolved event built from the narrow credential read equals one built from a full
// device read, for a device that stores an external id, a name, a description and large
// metadata. It guards a column the resolver starts reading later: that read would see a zero
// value on a cache miss, and this test would fail.
func TestAResolvedEventDoesNotDependOnUnreadDeviceColumns(t *testing.T) {
	rig := newCountingResolveRig(t, false, nil)
	secret := "s3cret"
	if _, err := rig.api.CreateDeviceCredential(rig.ctx, &model.DeviceCredentialCreateRequest{
		Token: "c-1", DeviceToken: "dev", CredentialType: string(model.CredentialMqttBasic),
		CredentialId: "cred-1", CredentialValue: &secret, Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	ext, name, desc := "ext-1", "a named device", "a described device"
	meta := `{"blob":"` + strings.Repeat("x", 4096) + `"}`
	if _, err := rig.api.UpdateDevice(rig.ctx, "dev", &model.DeviceUpdateRequest{
		ExternalId: dcgraphql.OptionalStringOf(ext), Name: dcgraphql.OptionalStringOf(name),
		Description: dcgraphql.OptionalStringOf(desc), Metadata: dcgraphql.OptionalStringOf(meta),
	}); err != nil {
		t.Fatal(err)
	}
	rig.caches.Credentials = nil // every event reads the credential: the miss path

	narrow := NewEventResolver(1, rig.capi, config.AuthModeRequired, EventTimePolicy{}, nil, nil, nil, nil, nil, nil)
	full := NewEventResolver(1, fullDeviceApi{DeviceManagementApi: rig.capi, rig: rig}, config.AuthModeRequired,
		EventTimePolicy{}, nil, nil, nil, nil, nil, nil)

	events := map[string]func() *esmodel.UnresolvedEvent{
		"measurement": func() *esmodel.UnresolvedEvent { return tempEvent("21") },
		"location":    devLocationEvent,
	}
	for label, mk := range events {
		t.Run(label, func(t *testing.T) {
			var got [2]*model.ResolvedEvent
			for i, rez := range []*EventResolver{narrow, full} {
				ev := mk()
				ctype, cid := string(model.CredentialMqttBasic), "cred-1"
				ev.CredentialType, ev.CredentialId, ev.CredentialSecret = &ctype, &cid, &secret
				results, reason, err := rez.ResolveEvent(rig.ctx, ev)
				if err != nil || reason != 0 || len(results) != 1 {
					t.Fatalf("resolver %d: %d results, reason %d, err %v", i, len(results), reason, err)
				}
				got[i] = results[0].Resolved
			}
			if got[0].SourceDeviceToken != "dev" || got[0].ExternalId != ext {
				t.Fatalf("control: the narrow resolve lost the device token or external id: %+v", got[0])
			}
			if !reflect.DeepEqual(got[0], got[1]) {
				t.Errorf("the resolved event differs between the narrow and the full device read:\nnarrow %+v\nfull   %+v", got[0], got[1])
			}
		})
	}
}
