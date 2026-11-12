// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package actiongate

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestKillEventCodec(t *testing.T) {
	e := KillEvent{EventID: "abc", Tenant: "acme", Cause: KillCauseTenantDisabled, ActuationGeneration: 3, At: time.Now().UTC()}
	for _, cause := range []KillCause{KillCauseTenantDisabled, KillCauseOperatorSuspended} {
		e.Cause = cause
		b, err := EncodeKillEvent(e)
		killNoError(t, err)
		got, err := DecodeKillEvent(b)
		killNoError(t, err)
		killEqual(t, e, got)
	}
	for name, mutate := range map[string]func(*KillEvent){
		"id": func(e *KillEvent) { e.EventID = "" }, "tenant": func(e *KillEvent) { e.Tenant = " " },
		"blank id": func(e *KillEvent) { e.EventID = " " }, "empty tenant": func(e *KillEvent) { e.Tenant = "" },
		"cause": func(e *KillEvent) { e.Cause = "other" }, "generation": func(e *KillEvent) { e.ActuationGeneration = -1 },
		"time": func(e *KillEvent) { e.At = time.Time{} },
	} {
		t.Run(name, func(t *testing.T) {
			bad := e
			mutate(&bad)
			_, err := EncodeKillEvent(bad)
			killError(t, err)
			b, err := json.Marshal(bad)
			killNoError(t, err)
			_, err = DecodeKillEvent(b)
			killError(t, err)
		})
	}
	b, err := EncodeKillEvent(e)
	killNoError(t, err)
	for name, raw := range map[string][]byte{
		"unknown":  []byte(strings.TrimSuffix(string(b), "}") + `,"unknown":true}`),
		"second":   append(append([]byte{}, b...), []byte(` {}`)...),
		"garbage":  append(append([]byte{}, b...), 'x'),
		"oversize": []byte(strings.Repeat(" ", 4097)), "empty": nil, "malformed": []byte(`{`), "null": []byte(`null`),
	} {
		t.Run(name, func(t *testing.T) { _, err := DecodeKillEvent(raw); killError(t, err) })
	}
	exact := append(append([]byte{}, b...), []byte(strings.Repeat(" ", 4096-len(b)))...)
	_, err = DecodeKillEvent(exact)
	killNoError(t, err)
	e.EventID = strings.Repeat("a", 4096)
	_, err = EncodeKillEvent(e)
	killError(t, err)
}
func TestCheckFence(t *testing.T) {
	for _, tc := range []struct {
		name string
		s    FenceState
		v    int
		want FenceVerdict
	}{
		{"absent", FenceState{}, 0, FenceOpen},
		{"absent ignores fields", FenceState{Enabled: true, KilledAtGeneration: 9}, 0, FenceOpen},
		{"enabled newer", FenceState{true, true, 4}, 5, FenceOpen},
		{"disabled newer", FenceState{true, false, 4}, 5, FenceReenable},
		{"enabled equal", FenceState{true, true, 4}, 4, FenceClosed},
		{"disabled equal", FenceState{true, false, 4}, 4, FenceClosed},
		{"enabled older", FenceState{true, true, 4}, 3, FenceClosed},
		{"disabled older", FenceState{true, false, 4}, 3, FenceClosed},
	} {
		t.Run(tc.name, func(t *testing.T) { killEqual(t, tc.want, CheckFence(tc.s, tc.v)) })
	}
}

func killNoError(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
func killError(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("expected rejection")
	}
}
func killEqual(t *testing.T, want, got any) {
	t.Helper()
	if !reflect.DeepEqual(want, got) {
		t.Fatalf("want %#v, got %#v", want, got)
	}
}
