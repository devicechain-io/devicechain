// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"os"
	"strings"
	"testing"
)

// reservationState is a State on the volume it names, with fail-closed as given.
func reservationState(compactOn bool, volume, maxFileStore string, failClosed *bool) *State {
	st := compactState(compactOn)
	st.Evolving = true
	st.LiveVolumes.JetStream = volume
	st.LiveMaxFileStore = maxFileStore
	st.MqttFailClosed = failClosed
	return st
}

func TestTheFloorRuleIsTheBrokers(t *testing.T) {
	for volume, want := range map[string]int64{"16Gi": 14 << 30, "12Gi": 10 << 30, "3Gi": 2 << 30, "14745Mi": 13270 << 20} {
		if got, err := derivedMaxFileStore(volume); err != nil || got != want {
			t.Errorf("derivedMaxFileStore(%s) = %d, %v; want %d", volume, got, err, want)
		}
	}
}

// 🔴 THE SHIPPED SIZES PASS, WITH THE SETTING OFF, ON AND UNSET, so the precheck refuses only
// what the services would.
func TestShippedAndCompactVolumesPassTheReservationCheck(t *testing.T) {
	t.Parallel()
	shipped := jetStreamStorageFor(&State{})
	for _, tc := range []struct {
		name    string
		compact bool
		volume  string
	}{{"shipped", false, shipped}, {"compact", true, compact.JetStreamStorage}} {
		for _, fc := range []*bool{nil, boolPtr(false), boolPtr(true)} {
			if err := precheckJetStreamReservation(t.Context(), reservationState(tc.compact, tc.volume, "", fc)); err != nil {
				t.Errorf("%s volume %s, failClosed=%v: %v", tc.name, tc.volume, fc, err)
			}
		}
	}
}

// An instance whose LIVE volume is smaller than shipped is refused when the setting would
// raise the reservation past what it holds, naming the numbers and the way back.
func TestAShrunkLiveVolumeIsRefusedBeforeAnythingIsWritten(t *testing.T) {
	t.Parallel()
	err := precheckJetStreamReservation(t.Context(), reservationState(false, "14Gi", "", boolPtr(true)))
	if err == nil {
		t.Fatal("a 14Gi JetStream volume (a 12Gi ceiling) was let through with fail-closed on")
	}
	for _, want := range []string{"MiB of JetStream storage", "90% of its 14Gi", "--mqtt-fail-closed=false", "Nothing has been changed"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not say %q: %v", want, err)
		}
	}
	// The same volume holds everything it held before the setting: the refusal is the
	// setting's own doing, not the volume's.
	for _, off := range []*bool{nil, boolPtr(false)} {
		if err := precheckJetStreamReservation(t.Context(), reservationState(false, "14Gi", "", off)); err != nil {
			t.Errorf("a 14Gi volume was refused with the setting %v: %v", off, err)
		}
	}
}

// A hand-set ceiling is the broker's real one, whatever the volume.
func TestAHandSetMaxFileStoreIsTheCeiling(t *testing.T) {
	t.Parallel()
	err := precheckJetStreamReservation(t.Context(), reservationState(false, "16Gi", "1Gi", nil))
	if err == nil || !strings.Contains(err.Error(), "max_file_store, 1Gi") {
		t.Fatalf("a 1Gi max_file_store over a 16Gi volume was not refused on its own number: %v", err)
	}
	if err := precheckJetStreamReservation(t.Context(), reservationState(false, "16Gi", "14Gi", boolPtr(true))); err != nil {
		t.Errorf("the derived ceiling spelled out was refused: %v", err)
	}
}

// The refusal has to come before the declaration is written, or every flagless retry would
// read the setting back and fail again.
func TestTheReservationIsCheckedBeforeTheDeclarationIsWritten(t *testing.T) {
	raw, err := os.ReadFile("upgrade.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(raw)
	check := strings.Index(src, "precheckJetStreamReservation(ctx, st)")
	write := strings.Index(src, "recordUpgradedVersion(ctx, dyn, opts.Instance, st)")
	if check < 0 || write < 0 || check > write {
		t.Fatalf("Upgrade must call precheckJetStreamReservation (at %d) before recordUpgradedVersion (at %d)", check, write)
	}
}
