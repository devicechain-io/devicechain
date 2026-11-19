// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	dmcontract "github.com/devicechain-io/dc-device-management/contract"
	dmproto "github.com/devicechain-io/dc-device-management/proto"
	gql "github.com/graph-gophers/graphql-go"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
	gproto "google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// v1.7.0 ends at field 20. Remove the later position_derived field and its
// synthetic oneof as well as the three additive suppression fields.
func suppressionV17Descriptor(t *testing.T) protoreflect.MessageDescriptor {
	t.Helper()
	fd := protodesc.ToFileDescriptorProto((&dmproto.PAlarmStateChangeEvent{}).ProtoReflect().Descriptor().ParentFile())
	var msg *descriptorpb.DescriptorProto
	for _, m := range fd.MessageType {
		if m.GetName() == "PAlarmStateChangeEvent" {
			msg = m
		}
	}
	require.NotNil(t, msg)
	kept := msg.Field[:0]
	removed := 0
	for _, f := range msg.Field {
		if f.GetNumber() >= 21 && f.GetNumber() <= 24 {
			removed++
			continue
		}
		kept = append(kept, f)
	}
	require.Equal(t, 4, removed)
	msg.Field = kept
	oneofs := msg.OneofDecl[:0]
	for _, o := range msg.OneofDecl {
		if o.GetName() != "_position_derived" {
			oneofs = append(oneofs, o)
		}
	}
	msg.OneofDecl = oneofs
	file, err := protodesc.NewFile(fd, nil)
	require.NoError(t, err)
	return file.Messages().ByName("PAlarmStateChangeEvent")
}

func suppressionV17Wire(t *testing.T, wire []byte) []byte {
	t.Helper()
	old := dynamicpb.NewMessage(suppressionV17Descriptor(t))
	require.NoError(t, gproto.Unmarshal(wire, old))
	// An OLD producer cannot emit unknown position/suppression fields. A decode/re-encode
	// without this step would retain them and would not model an old producer.
	old.SetUnknown(nil)
	out, err := gproto.Marshal(old)
	require.NoError(t, err)
	return out
}

func suppressionOldValidation(t *testing.T) any {
	t.Helper()
	raw, err := os.ReadFile("../../device-management/graphql/schema.graphql")
	require.NoError(t, err)
	sdl := string(raw)
	start := strings.Index(sdl, "    alarmSuppressionStatus(")
	require.GreaterOrEqual(t, start, 0, "must actually find Query.alarmSuppressionStatus")
	description := strings.LastIndex(sdl[:start], "    \"\"\"")
	require.GreaterOrEqual(t, description, 0)
	const resultType = "): [AlarmSuppressionStatus!]!"
	end := strings.Index(sdl[start:], resultType)
	require.GreaterOrEqual(t, end, 0)
	sdl = sdl[:description] + sdl[start+end+len(resultType):]
	require.NotEqual(t, string(raw), sdl)
	require.NotContains(t, sdl, "    alarmSuppressionStatus(")
	schema, err := gql.ParseSchema(sdl, nil, gql.UseFieldResolvers())
	require.NoError(t, err)
	errs := schema.ValidateWithVariables(alarmSuppressionStatusDocument, map[string]any{"tokens": []string{"a1"}})
	require.NotEmpty(t, errs)
	return errs
}

func TestSuppressionRollingMatrix(t *testing.T) {
	validation := suppressionOldValidation(t)
	for _, tc := range []struct {
		name         string
		newNM, newDM bool
	}{
		{"new-NM_new-DM", true, true}, {"new-NM_old-DM", true, false},
		{"old-NM_new-DM", false, true}, {"old-NM_old-DM", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clock := outsideQuiet
			until := clock.Add(time.Hour)
			n, api, fa, ctx := quietFixture(t, "", "", "", &clock)
			suppressionMetrics(n)
			client, url := suppressionWire(t, func(w http.ResponseWriter, r *http.Request) {
				var req struct {
					Query     string
					Variables struct{ Tokens []string }
				}
				require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
				require.Equal(t, alarmSuppressionStatusDocument, req.Query)
				if !tc.newDM {
					_ = json.NewEncoder(w).Encode(map[string]any{"errors": validation})
					return
				}
				rows := []map[string]any{}
				for _, token := range req.Variables.Tokens {
					row := map[string]any{"alarmToken": token, "severity": "MAJOR", "state": "ACTIVE", "suppression": nil}
					if clock.Before(until) {
						row["suppression"] = map[string]string{"windowToken": "window-1", "until": until.Format(time.RFC3339Nano)}
					}
					rows = append(rows, row)
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"alarmSuppressionStatus": rows}})
			})
			lookup := NewSuppressionLookup(client, url).(*suppressionLookup)
			lookup.now = func() time.Time { return clock }
			if tc.newNM {
				n.SuppressionLookup = lookup
			}
			plain := eventAt(dmcontract.AlarmEventRaised, "MAJOR", clock)
			plain.DeviceToken, plain.DeviceTypeToken, plain.DeviceName = "pump-1", "pump", "Pump"
			plain.ScopeAnchors = []string{"customer:A"}
			no := false
			plain.PositionDerived = &no
			event := *plain
			if tc.newDM {
				event.SuppressedUntil = &until
				event.SuppressionWindowToken = "window-1"
				event.RaisedDuringSuppressionToken = "window-1"
			}
			wire, err := dmproto.MarshalAlarmStateChangeEvent(&event)
			require.NoError(t, err)
			if !tc.newDM {
				wire = suppressionV17Wire(t, wire)
			}
			if !tc.newNM {
				descriptor := suppressionV17Descriptor(t)
				old := dynamicpb.NewMessage(descriptor)
				require.NoError(t, gproto.Unmarshal(wire, old))
				plainWire, err := dmproto.MarshalAlarmStateChangeEvent(plain)
				require.NoError(t, err)
				expected := dynamicpb.NewMessage(descriptor)
				require.NoError(t, gproto.Unmarshal(plainWire, expected))
				old.SetUnknown(nil)
				expected.SetUnknown(nil)
				require.True(t, gproto.Equal(expected, old), "EVERY pre-existing field must decode exactly as the plain v1.7.0 event")
				wire, err = gproto.Marshal(old)
				require.NoError(t, err)
				round := dynamicpb.NewMessage(descriptor)
				require.NoError(t, gproto.Unmarshal(wire, round))
				require.True(t, gproto.Equal(old, round))
			}
			got, err := dmproto.UnmarshalAlarmStateChangeEvent(wire)
			require.NoError(t, err)
			if !tc.newDM || !tc.newNM {
				require.Nil(t, got.PositionDerived, "v1.7.0 producer bytes have no position verdict and must still page")
			}
			require.NoError(t, n.Notify(ctx, got))
			if tc.newNM && tc.newDM {
				require.Zero(t, fa.calls)
				require.Len(t, heldRows(t, api, ctx), 1)
				clock = clock.Add(time.Minute)
				require.NoError(t, n.ReleaseHeld(ctx, enabledPolicies(t, api, ctx), clock))
				require.Zero(t, fa.calls)
				require.Len(t, heldRows(t, api, ctx), 1)
				clock = until.Add(time.Nanosecond)
				require.NoError(t, n.ReleaseHeld(ctx, enabledPolicies(t, api, ctx), clock))
				require.Equal(t, 1, fa.calls)
				require.Empty(t, heldRows(t, api, ctx))
				require.NoError(t, n.ReleaseHeld(ctx, enabledPolicies(t, api, ctx), clock.Add(time.Hour)))
				require.Equal(t, 1, fa.calls)
			} else {
				require.Equal(t, 1, fa.calls)
				require.Empty(t, heldRows(t, api, ctx))
			}
			if tc.newNM && !tc.newDM {
				// A later real escalation must count the real old-SDL validation error,
				// treat it as unsupported, and page; it must never turn it into a hold.
				esc, rec, escAPI, escCtx := pagedThenDue(t, namedAlarm())
				suppressionMetrics(esc)
				esc.SuppressionLookup = lookup
				states, err := escAPI.OpenNotificationStates(escCtx)
				require.NoError(t, err)
				require.Len(t, states, 1)
				require.NoError(t, esc.Escalate(escCtx, states[0], enabledPolicies(t, escAPI, escCtx), esc.clock(), 5))
				require.Len(t, rec.sent, 2)
				require.Empty(t, heldRows(t, escAPI, escCtx))
				require.Equal(t, float64(1), testutil.ToFloat64(esc.suppression.WithLabelValues("lookup_failed")))
				_, err = lookup.LookupSuppressions(escCtx, []string{"a1"})
				require.ErrorIs(t, err, ErrSuppressionUnsupported)
			}
		})
	}
	t.Run("old-pod-leases-new-hold-early", func(t *testing.T) {
		clock := outsideQuiet
		until := clock.Add(time.Hour)
		n, api, fa, ctx := quietFixture(t, "", "", "", &clock)
		require.NoError(t, n.Notify(ctx, suppressedEvent(clock, until)))
		require.Zero(t, fa.calls)
		clock = clock.Add(time.Minute)
		due, err := v17DueDeferrals(api.RDB.DB(ctx), clock, releaseBatch)
		require.NoError(t, err)
		require.Len(t, due, 1)
		require.True(t, clock.Before(until))
		require.True(t, heldRows(t, api, ctx)[0].HeldByWindow.Valid)
		won, err := v17LeaseDeferral(api.RDB.DB(ctx), due[0], clock.Add(time.Minute))
		require.NoError(t, err)
		require.True(t, won)
		won, err = v17LeaseDeferral(api.RDB.DB(ctx), due[0], clock.Add(time.Minute))
		require.NoError(t, err)
		require.False(t, won)
		var event dmcontract.AlarmStateChangeEvent
		require.NoError(t, json.Unmarshal(due[0].Event, &event))
		wire, err := dmproto.MarshalAlarmStateChangeEvent(&event)
		require.NoError(t, err)
		old, err := dmproto.UnmarshalAlarmStateChangeEvent(suppressionV17Wire(t, wire))
		require.NoError(t, err)
		require.NoError(t, n.Notify(ctx, old))
		require.Equal(t, 1, fa.calls, "v1.7.0 has no quiet hold here and sends early")
	})
}

// SNAPSHOT v1.7.0:backend/services/notification-management/model/model_deferrals.go NotificationDeferral
// Keep this independent of the live model: old pods do not know held_by_window.
type v17Deferral struct {
	ID                      uint `gorm:"primarykey"`
	CreatedAt, UpdatedAt    time.Time
	TenantId                string
	AlarmToken              string
	PolicyId                uint
	AlarmKey, Severity      string
	OccurredTime, ReleaseAt time.Time
	Attempts                int
	Event                   datatypes.JSON
}

func (v17Deferral) TableName() string { return "notification_deferrals" }

// SNAPSHOT v1.7.0:backend/services/notification-management/model/api_deferrals.go DueNotificationDeferrals
func v17DueDeferrals(db *gorm.DB, now time.Time, limit int) ([]*v17Deferral, error) {
	found := make([]*v17Deferral, 0)
	err := db.Where("release_at <= ?", now).Order("release_at ASC, id ASC").Limit(limit).Find(&found).Error
	return found, err
}

// SNAPSHOT v1.7.0:backend/services/notification-management/model/api_deferrals.go LeaseNotificationDeferral
func v17LeaseDeferral(db *gorm.DB, d *v17Deferral, until time.Time) (bool, error) {
	result := db.Model(&v17Deferral{ID: d.ID}).Where("updated_at = ? AND release_at = ?", d.UpdatedAt, d.ReleaseAt).Update("release_at", until)
	return result.RowsAffected == 1, result.Error
}
