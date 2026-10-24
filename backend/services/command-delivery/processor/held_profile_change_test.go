// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-command-delivery/model"
	"github.com/devicechain-io/dc-command-delivery/verify"
	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/svcclient"
	"github.com/stretchr/testify/require"
)

// A command waiting for a maintenance window, and a PROFILE change under it.
//
// These tests run the re-stamp pass through the REAL device-management client
// (verify.WindowScheduler) against a stub device-management that decides the way the real one
// does: per device type, from the profile version the type is on now (stubOwner.impact) and the
// window the type is on now (stubOwner.window). Asked with alreadyHeld, it answers by the window
// alone; asked without it (enqueue), a NORMAL command is ungated. A key missing from impact is
// one the profile no longer defines.
//
// The decision they pin: only an operator changing the WINDOW releases a waiting command. A new
// profile version that marks it NORMAL leaves it waiting; one that drops it fails it, unsent.

type stubOwner struct {
	impact   map[string]string // command key -> DISRUPTIVE | NORMAL; absent = not defined
	window   string            // the type's window now; "" = none
	openings map[string]model.WindowOpening
	// blip, when set, is how the schedule question fails: "500", "graphql" (an errors envelope
	// beside data that names every device undefined), "drop" (the connection is cut).
	blip string
}

func (o *stubOwner) serve(t *testing.T) string {
	t.Helper()
	dm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		var data map[string]any
		switch {
		case strings.Contains(req.Query, "maintenanceWindowOpening"):
			token, _ := req.Variables["token"].(string)
			op, found := o.openings[token]
			ans := map[string]any{"found": found, "openNow": op.OpenNow}
			if found {
				ans["dispatchAfter"] = op.DispatchAfter.UTC().Format(time.RFC3339)
				ans["closes"] = op.Closes.UTC().Format(time.RFC3339)
			}
			data = map[string]any{"maintenanceWindowOpening": ans}
		case strings.Contains(req.Query, "commandDispatchSchedule") && o.blip != "":
			switch o.blip {
			case "500":
				http.Error(w, "upstream unavailable", http.StatusInternalServerError)
			case "graphql":
				devices, _ := req.Variables["deviceTokens"].([]any)
				_ = json.NewEncoder(w).Encode(map[string]any{
					"data": map[string]any{"commandDispatchSchedule": map[string]any{
						"holds": []any{}, "refusals": []any{}, "ungated": []any{}, "undefined": devices}},
					"errors": []any{map[string]any{"message": "context deadline exceeded"}}})
			case "drop":
				conn, _, err := w.(http.Hijacker).Hijack()
				if err == nil {
					_ = conn.Close()
				}
			}
			return
		case strings.Contains(req.Query, "commandDispatchSchedule"):
			alreadyHeld := strings.Contains(req.Query, "alreadyHeld: true")
			key, _ := req.Variables["commandKey"].(string)
			holds, ungated, undefined := []any{}, []string{}, []string{}
			devices, _ := req.Variables["deviceTokens"].([]any)
			for _, d := range devices {
				device := d.(string)
				impact, defined := o.impact[key]
				switch {
				case !defined:
					undefined = append(undefined, device)
				case o.window == "":
					ungated = append(ungated, device)
				case !alreadyHeld && impact == "NORMAL":
					ungated = append(ungated, device)
				default:
					op := o.openings[o.window]
					holds = append(holds, map[string]any{"deviceToken": device, "windowToken": o.window,
						"dispatchAfter": op.DispatchAfter.UTC().Format(time.RFC3339),
						"dispatchUntil": op.Closes.UTC().Format(time.RFC3339)})
				}
			}
			data = map[string]any{"commandDispatchSchedule": map[string]any{
				"holds": holds, "refusals": []any{}, "ungated": ungated, "undefined": undefined}}
		default:
			http.Error(w, "unexpected query", http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
	}))
	t.Cleanup(dm.Close)
	return dm.URL
}

func stubScheduler(t *testing.T, owner *stubOwner) *verify.WindowScheduler {
	t.Helper()
	mint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(auth.ServiceTokenResponse{Token: "svc-token", ExpiresAt: 1 << 40})
	}))
	t.Cleanup(mint.Close)
	host, portStr, _ := net.SplitHostPort(mint.Listener.Addr().String())
	port, _ := strconv.Atoi(portStr)
	client := svcclient.New(config.UserManagementConfiguration{Hostname: host, Port: uint32(port), InternalPort: uint32(port)},
		"shh", "command-delivery", []string{string(auth.DeviceRead)})
	return verify.NewWindowScheduler(client, owner.serve(t))
}

// twoWindows is plant-a (the window the commands were stamped from) and plant-b, both next
// opening days away, so a row bound to either is not dispatchable now.
func twoWindows() map[string]model.WindowOpening {
	a := time.Now().Add(6 * 24 * time.Hour).Truncate(time.Second)
	b := time.Now().Add(5 * 24 * time.Hour).Truncate(time.Second)
	return map[string]model.WindowOpening{
		"plant-a": {Found: true, DispatchAfter: a, Closes: a.Add(4 * time.Hour)},
		"plant-b": {Found: true, DispatchAfter: b, Closes: b.Add(4 * time.Hour)},
	}
}

// seedWaitingInEveryState queues one reboot, stamped from plant-a, in each undelivered status a
// window-stamped command can be in.
func seedWaitingInEveryState(t *testing.T, api *model.Api) map[string]model.CommandStatus {
	t.Helper()
	rows := map[string]model.CommandStatus{
		"w-queued": model.CommandQueued, "w-held": model.CommandHeld, "w-parked": model.CommandParked,
	}
	i := 0
	for token, status := range rows {
		seedHeldRow(t, api, "acme", token, "dev-"+strconv.Itoa(i), status, time.Now().Add(time.Hour))
		i++
	}
	return rows
}

// The profile version the type is on now marks `reboot` NORMAL. Every waiting reboot is still
// held on plant-a after the pass (and bound to plant-a's current opening), and the sweep sends
// none of them.
func TestAWaitingCommandMarkedNormalByANewProfileVersionStaysHeld(t *testing.T) {
	api := realApi(t)
	rows := seedWaitingInEveryState(t, api)
	owner := &stubOwner{impact: map[string]string{"reboot": "NORMAL"}, window: "plant-a", openings: twoWindows()}
	writer := &recordingWriter{}
	proc := procWith(api, writer)
	proc.WindowOpenings = stubScheduler(t, owner)

	require.NoError(t, proc.restampWindows(context.Background()))
	require.NoError(t, proc.deliverPendingCommands(context.Background()))

	a := owner.openings["plant-a"]
	for token, status := range rows {
		got := rowOf(t, api, "acme", token)
		require.Equal(t, "plant-a", got.WindowToken.String,
			"%s (%s) was released by a profile version that marks it NORMAL", token, status)
		require.True(t, got.DispatchAfter.Valid && got.DispatchAfter.Time.Equal(a.DispatchAfter),
			"%s holds from %v, want plant-a's opening %v", token, got.DispatchAfter, a.DispatchAfter)
		require.True(t, got.DispatchUntil.Valid && got.DispatchUntil.Time.Equal(a.Closes),
			"%s holds until %v, want plant-a's close %v", token, got.DispatchUntil, a.Closes)
		require.Equal(t, status.String(), got.Status, "%s changed status", token)
	}
	require.Zero(t, writer.count(), "the sweep sent a command that is still waiting for its window")
}

// The profile version the type is on now no longer defines `reboot`. Every waiting reboot ends
// FAILED, NOT_DELIVERED, with the reason that says so, and the sweep sends none of them.
func TestAWaitingCommandDroppedByANewProfileVersionFailsUnsent(t *testing.T) {
	api := realApi(t)
	rows := seedWaitingInEveryState(t, api)
	owner := &stubOwner{impact: map[string]string{"other": "DISRUPTIVE"}, window: "plant-a", openings: twoWindows()}
	writer := &recordingWriter{}
	proc := procWith(api, writer)
	proc.WindowOpenings = stubScheduler(t, owner)

	require.NoError(t, proc.restampWindows(context.Background()))
	require.NoError(t, proc.deliverPendingCommands(context.Background()))

	for token := range rows {
		got := rowOf(t, api, "acme", token)
		require.Equal(t, model.CommandFailed.String(), got.Status, "%s was not failed", token)
		require.Equal(t, model.OutcomeNotDelivered.String(), got.Outcome.String, "%s outcome", token)
		require.Contains(t, got.Error.String, "no longer defined by this device's profile", "%s reason", token)
	}
	require.Zero(t, writer.count(), "the sweep sent a command its device's profile no longer defines")

	// Dropped AND the window unassigned: still failed, never released.
	api2 := realApi(t)
	seedHeldRow(t, api2, "acme", "w-solo", "dev-9", model.CommandQueued, time.Now().Add(time.Hour))
	owner2 := &stubOwner{impact: map[string]string{}, window: "", openings: twoWindows()}
	proc2 := procWith(api2, &recordingWriter{})
	proc2.WindowOpenings = stubScheduler(t, owner2)
	require.NoError(t, proc2.restampWindows(context.Background()))
	require.Equal(t, model.CommandFailed.String(), rowOf(t, api2, "acme", "w-solo").Status,
		"an undefined command whose type also lost its window was released")
}

// The operator's levers are unchanged: re-pointing the type moves the waiting commands to the new
// window, and unassigning its window releases them — whatever the profile now says of the command.
func TestRepointAndUnassignStillMoveAndReleaseWaitingCommands(t *testing.T) {
	for _, impact := range []string{"DISRUPTIVE", "NORMAL"} {
		t.Run(impact, func(t *testing.T) {
			api := realApi(t)
			rows := seedWaitingInEveryState(t, api)
			owner := &stubOwner{impact: map[string]string{"reboot": impact}, window: "plant-b", openings: twoWindows()}
			proc := &CommandDeliveryProcessor{Api: api, WindowOpenings: stubScheduler(t, owner)}
			require.NoError(t, proc.restampWindows(context.Background()))
			b := owner.openings["plant-b"]
			for token := range rows {
				got := rowOf(t, api, "acme", token)
				require.Equal(t, "plant-b", got.WindowToken.String, "%s did not follow its type to plant-b", token)
				require.True(t, got.DispatchAfter.Time.Equal(b.DispatchAfter), "%s is not on plant-b's opening", token)
			}

			owner.window = ""
			proc.lastWindowRestamp = time.Time{}
			require.NoError(t, proc.restampWindows(context.Background()))
			for token, status := range rows {
				got := rowOf(t, api, "acme", token)
				require.False(t, got.WindowToken.Valid || got.DispatchAfter.Valid || got.DispatchUntil.Valid,
					"%s is still held after its type's window was unassigned", token)
				require.Equal(t, status.String(), got.Status, "%s changed status on release", token)
			}
		})
	}
}

// A command enqueued AFTER the profile change follows the new profile: NORMAL is not held at all,
// and DISRUPTIVE is stamped from the type's window.
func TestACommandEnqueuedAfterTheProfileChangeFollowsTheNewProfile(t *testing.T) {
	api := realApi(t)
	owner := &stubOwner{impact: map[string]string{"reboot": "NORMAL", "wipe": "DISRUPTIVE"}, window: "plant-a",
		openings: twoWindows()}
	api.DispatchScheduler = stubScheduler(t, owner)
	ctx := core.WithTenant(context.Background(), "acme")

	_, err := api.CreateCommand(ctx, &model.CommandCreateRequest{Token: "after-normal", DeviceToken: "dev-1", Name: "reboot"})
	require.NoError(t, err)
	got := rowOf(t, api, "acme", "after-normal")
	require.False(t, got.WindowToken.Valid || got.DispatchAfter.Valid,
		"a command the current profile marks NORMAL was held at enqueue: %v", got.WindowToken)

	_, err = api.CreateCommand(ctx, &model.CommandCreateRequest{Token: "after-disruptive", DeviceToken: "dev-1", Name: "wipe"})
	require.NoError(t, err)
	got = rowOf(t, api, "acme", "after-disruptive")
	require.Equal(t, "plant-a", got.WindowToken.String, "a DISRUPTIVE command was not held at enqueue")
}

// 🔴 A BLIP IS NOT A VERDICT. When device-management cannot answer the schedule question (a 5xx,
// a GraphQL error envelope even beside data that names every device undefined, a cut
// connection), nothing is decided: every waiting command stays exactly as stamped, none is
// failed and none is released. Reading a failed answer as "undefined" would FAIL a fleet's
// waiting commands over a network blip.
func TestADeviceManagementBlipFailsNothing(t *testing.T) {
	for _, blip := range []string{"500", "graphql", "drop"} {
		t.Run(blip, func(t *testing.T) {
			api := realApi(t)
			rows := seedWaitingInEveryState(t, api)
			owner := &stubOwner{impact: map[string]string{}, window: "plant-a", openings: twoWindows(), blip: blip}
			writer := &recordingWriter{}
			proc := procWith(api, writer)
			proc.WindowOpenings = stubScheduler(t, owner)

			require.Error(t, proc.restampWindows(context.Background()), "a failed schedule question was not reported")
			for token, status := range rows {
				got := rowOf(t, api, "acme", token)
				require.Equal(t, status.String(), got.Status, "%s changed status on a blip", token)
				require.Equal(t, "plant-a", got.WindowToken.String, "%s left its window on a blip", token)
				require.False(t, got.Error.Valid, "%s carries an error after a blip: %q", token, got.Error.String)
			}
			require.NoError(t, proc.deliverPendingCommands(context.Background()))
			require.Zero(t, writer.count(), "a waiting command was sent after a blip")
		})
	}
}
