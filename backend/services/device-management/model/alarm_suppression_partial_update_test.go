// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/rdb"
	putest "github.com/devicechain-io/dc-microservice/rdb/partialupdatetest"
	"gorm.io/datatypes"
)

func suppressionWindowFamily(recurring bool) putest.Family[*Api] {
	const slots = "[{\"day\":\"MONDAY\",\"start\":\"10:00\",\"durationMinutes\":60}]"
	const slotsReplace = "[{\"day\":\"TUESDAY\",\"start\":\"11:00\",\"durationMinutes\":120}]"
	const from = "2030-01-01T01:00:00Z"
	const until = "2030-01-01T05:00:00Z"
	name := "alarmSuppressionWindowOneShot"
	limitations := map[string]string{
		"kind":       "Switching kind must clear the old schedule and set the new one atomically; TestSuppressionPartialNullableFieldsAndScheduleSwitch and TestAlarmSuppressionWireThreeStatesAndOwnedFields drive it.",
		"timezone":   "Inapplicable to ONE_SHOT; the recurring family drives all three states.",
		"slots":      "Inapplicable to ONE_SHOT; the recurring family drives all three states.",
		"validFrom":  "Inapplicable to ONE_SHOT; the recurring family drives all three states.",
		"validUntil": "Inapplicable to ONE_SHOT; the recurring family drives all three states.",
	}
	if recurring {
		name = "alarmSuppressionWindowRecurring"
		limitations = map[string]string{
			"kind":      "Switching kind requires a whole schedule edit; dedicated model and wire switch tests drive it.",
			"startTime": "Inapplicable to RECURRING; the one-shot family drives value and null refusal.",
			"endTime":   "Inapplicable to RECURRING; the one-shot family drives value and null refusal.",
		}
	}
	fields := []putest.Field{
		putest.OptionalStringField("name", "Original", "Renamed", func(r *AlarmSuppressionUpdateRequest) *dcgraphql.OptionalString { return &r.Name }),
		putest.OptionalStringField("description", "Description", "Rewritten", func(r *AlarmSuppressionUpdateRequest) *dcgraphql.OptionalString { return &r.Description }),
		putest.OptionalStringField("metadata", "{\"work\":\"old\"}", "{\"work\":\"new\"}", func(r *AlarmSuppressionUpdateRequest) *dcgraphql.OptionalString { return &r.Metadata }),
		putest.RequiredStringField("reason", "Service work", "Extended work", func(r *AlarmSuppressionUpdateRequest) *dcgraphql.OptionalString { return &r.Reason }),
		putest.OptionalStringListField("alarmKeys", []string{"hot"}, []string{"cold"}, func(r *AlarmSuppressionUpdateRequest) *dcgraphql.OptionalStringList { return &r.AlarmKeys }),
		putest.OptionalStringField("bypassSeverity", "CRITICAL", "MAJOR", func(r *AlarmSuppressionUpdateRequest) *dcgraphql.OptionalString { return &r.BypassSeverity }),
		{Name: "targets", Seeded: "[{\"type\":\"TENANT\",\"token\":\"\"}]", Replace: "[{\"type\":\"CUSTOMER\",\"token\":\"acme\"}]", Kind: putest.RequiredValue,
			Set: func(req any, value string) {
				var targets []AlarmSuppressionTarget
				if err := json.Unmarshal([]byte(value), &targets); err != nil {
					panic(err)
				}
				req.(*AlarmSuppressionUpdateRequest).Targets = OptionalAlarmSuppressionTargets{Set: true, Value: targets}
			},
			SetNull: func(req any) {
				req.(*AlarmSuppressionUpdateRequest).Targets = OptionalAlarmSuppressionTargets{Set: true}
			},
			SetEmpty: func(req any) {
				req.(*AlarmSuppressionUpdateRequest).Targets = OptionalAlarmSuppressionTargets{Set: true, Value: []AlarmSuppressionTarget{}}
			},
		},
	}
	if recurring {
		fields = append(fields,
			putest.RequiredStringField("timezone", "Europe/Berlin", "UTC", func(r *AlarmSuppressionUpdateRequest) *dcgraphql.OptionalString { return &r.Timezone }),
			putest.RequiredStringField("slots", slots, slotsReplace, func(r *AlarmSuppressionUpdateRequest) *dcgraphql.OptionalString { return &r.Slots }),
			putest.OptionalStringField("validFrom", from, "2029-12-01T01:00:00Z", func(r *AlarmSuppressionUpdateRequest) *dcgraphql.OptionalString { return &r.ValidFrom }),
			putest.OptionalStringField("validUntil", until, "2030-02-01T05:00:00Z", func(r *AlarmSuppressionUpdateRequest) *dcgraphql.OptionalString { return &r.ValidUntil }),
		)
	} else {
		fields = append(fields,
			putest.RequiredStringField("startTime", from, "2030-01-01T02:00:00Z", func(r *AlarmSuppressionUpdateRequest) *dcgraphql.OptionalString { return &r.StartTime }),
			putest.RequiredStringField("endTime", until, "2030-01-01T06:00:00Z", func(r *AlarmSuppressionUpdateRequest) *dcgraphql.OptionalString { return &r.EndTime }),
		)
	}
	return putest.Family[*Api]{
		Name: name, Token: "suppression", Migrate: []any{&AlarmSuppressionWindow{}}, Fields: fields, FieldLimitations: limitations,
		NewRequest: func() any { return new(AlarmSuppressionUpdateRequest) },
		Seed: func(t *testing.T, api *Api, ctx context.Context) {
			t.Helper()
			start, _ := time.Parse(time.RFC3339, from)
			end, _ := time.Parse(time.RFC3339, until)
			metadata := datatypes.JSON("{\"work\":\"old\"}")
			w := AlarmSuppressionWindow{TokenReference: rdb.TokenReference{Token: "suppression"}, NamedEntity: rdb.NamedEntity{Name: rdb.NullStrOf(strp("Original")), Description: rdb.NullStrOf(strp("Description"))},
				MetadataEntity: rdb.MetadataEntity{Metadata: &metadata}, Reason: "Service work", Targets: []AlarmSuppressionTarget{{Type: "TENANT"}}, AlarmKeys: []string{"hot"}, BypassSeverity: rdb.NullStrOf(strp("CRITICAL")), Kind: SuppressionOneShot, StartTime: &start, EndTime: &end}
			if recurring {
				w.Kind = SuppressionRecurring
				w.StartTime = nil
				w.EndTime = nil
				w.Timezone = rdb.NullStrOf(strp("Europe/Berlin"))
				w.Slots = datatypes.JSON(slots)
				w.ValidFrom = &start
				w.ValidUntil = &end
			}
			if _, err := api.CreateAlarmSuppressionWindow(suppressionCaller(ctx, true), &w); err != nil {
				t.Fatal(err)
			}
		},
		Update: func(api *Api, ctx context.Context, token string, req any) error {
			_, err := api.UpdateAlarmSuppressionWindow(suppressionCaller(ctx, true), token, req.(*AlarmSuppressionUpdateRequest))
			return err
		},
		Read: func(t *testing.T, api *Api, ctx context.Context) map[string]string {
			w, err := api.AlarmSuppressionWindowByToken(suppressionCaller(ctx, true), "suppression")
			if err != nil {
				t.Fatal(err)
			}
			targets, _ := json.Marshal(w.Targets)
			out := map[string]string{"name": nullStr(w.Name), "description": nullStr(w.Description), "metadata": jsonStr(w.Metadata), "reason": w.Reason, "targets": string(targets), "alarmKeys": putest.RenderStringList(w.AlarmKeys), "bypassSeverity": nullStr(w.BypassSeverity)}
			if recurring {
				render := func(t *time.Time) string {
					if t == nil {
						return putest.NullMarker
					}
					return t.UTC().Format(time.RFC3339Nano)
				}
				out["timezone"] = nullStr(w.Timezone)
				out["slots"] = string(w.Slots)
				out["validFrom"] = render(w.ValidFrom)
				out["validUntil"] = render(w.ValidUntil)
			} else {
				out["startTime"] = w.StartTime.UTC().Format(time.RFC3339Nano)
				out["endTime"] = w.EndTime.UTC().Format(time.RFC3339Nano)
			}
			return out
		},
	}
}
