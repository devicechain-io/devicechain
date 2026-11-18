// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
)

func relationalWALValues(vars []string) []string {
	var out []string
	for _, v := range vars {
		if strings.HasPrefix(v, "postgres_max_wal_size_mib=") {
			out = append(out, v)
		}
	}
	return out
}

func TestRelationalWALOptionsStateAndClusterEmission(t *testing.T) {
	for _, value := range []int{-1, 0, 63, 64, 4096} {
		t.Run(strconv.Itoa(value), func(t *testing.T) {
			err := ValidateRelationalStoreWAL(&value)
			if (err != nil) != (value != 0 && value < 64) {
				t.Fatalf("validation = %v", err)
			}
			if err != nil {
				return
			}
			st := installState(ClusterBinding{}, "local", InstallOptions{RelationalStoreMaxWALMiB: &value})
			want := []string{"postgres_max_wal_size_mib=" + strconv.Itoa(value)}
			cluster, err := installClusterVars(context.Background(), st, nil)
			if err != nil {
				t.Fatal(err)
			}
			if got := relationalWALValues(cluster); !reflect.DeepEqual(got, want) {
				t.Fatalf("cluster WAL = %v, want %v", got, want)
			}
			_, instance, err := splitVars(infraVars(st))
			if err != nil {
				t.Fatal(err)
			}
			if got := relationalWALValues(instance); len(got) != 0 {
				t.Fatalf("instance got cluster WAL: %v", got)
			}
			if got := installSettingsFor(st).RelationalStoreMaxWALMiB; got == nil || *got != value {
				t.Fatalf("settings dropped WAL: %v", got)
			}
		})
	}
	st := installState(ClusterBinding{}, "local", InstallOptions{})
	if got := relationalWALValues(infraVars(st)); len(got) != 0 {
		t.Fatalf("first install emitted WAL: %v", got)
	}
}

// A temporary cluster record exercises the same read/restore/emit path as a retry,
// with an unfinished apply and with a completed predecessor kept separately.
func TestRelationalWALRecordRetainsFailedApplyChoiceAndReset(t *testing.T) {
	ctx := context.Background()
	for _, completed := range []bool{false, true} {
		t.Run(strconv.FormatBool(completed), func(t *testing.T) {
			typed := fake.NewSimpleClientset()
			if completed {
				if err := writeInstalled(ctx, typed, aCompleteInstall(), installClock); err != nil {
					t.Fatal(err)
				}
			}
			orig := installClients
			t.Cleanup(func() { installClients = orig })
			installClients = func(string) (dynamic.Interface, kubernetes.Interface, error) { return nil, typed, nil }
			withClusterInstances(t, nil, nil)
			retry := func(want *int) *State {
				t.Helper()
				st := installState(ClusterBinding{}, "local", InstallOptions{})
				if _, _, err := connectAndRefuse(ctx, st, installSettingsFor(st), stateHere()); err != nil {
					t.Fatal(err)
				}
				got := st.RelationalStoreMaxWALMiB
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("restored WAL = %v, want %v", got, want)
				}
				cluster, err := installClusterVars(ctx, st, nil)
				if err != nil {
					t.Fatal(err)
				}
				var values []string
				if want != nil {
					values = []string{"postgres_max_wal_size_mib=" + strconv.Itoa(*want)}
				}
				if got := relationalWALValues(cluster); !reflect.DeepEqual(got, values) {
					t.Fatalf("retry emission = %v, want %v", got, values)
				}
				return st
			}
			// An older record without the field must keep today's behavior.
			retry(nil)
			for _, value := range []int{4096, 0} {
				st := installState(ClusterBinding{}, "local", InstallOptions{RelationalStoreMaxWALMiB: &value})
				if err := markInstallApplying(ctx, typed, testClusterUID, "test", installClock, st.RelationalStoreMaxWALMiB); err != nil {
					t.Fatal(err)
				}
				st = retry(&value)
				// Another failed flagless apply must keep the choice too.
				if err := markInstallApplying(ctx, typed, testClusterUID, "test", installClock, st.RelationalStoreMaxWALMiB); err != nil {
					t.Fatal(err)
				}
				retry(&value)
			}
		})
	}
}

// Installed records retain choices too; equal values from separate flag parses
// must not be mistaken for a settings change under running instances.
func TestRelationalWALCompletedRecordAndSettingsGuard(t *testing.T) {
	ctx := context.Background()
	value := 4096
	rec := aCompleteInstall()
	rec.Settings.RelationalStoreMaxWALMiB = &value
	typed := fake.NewSimpleClientset()
	if err := writeInstalled(ctx, typed, rec, installClock); err != nil {
		t.Fatal(err)
	}
	prev, err := previousInstall(ctx, typed)
	if err != nil {
		t.Fatal(err)
	}
	st, settings := reinstallState()
	if err := restoreRelationalStoreWAL(st, prev); err != nil {
		t.Fatal(err)
	}
	if st.RelationalStoreMaxWALMiB == nil || *st.RelationalStoreMaxWALMiB != value {
		t.Fatal("completed record lost WAL choice")
	}
	withClusterInstances(t, []string{"prod"}, nil)
	other := value
	settings.RelationalStoreMaxWALMiB = &other
	if err := refuseAReinstallThatWouldHurt(ctx, st, prev, settings, stateHere()); err != nil {
		t.Fatalf("equal WAL values refused: %v", err)
	}
	// A preset recommendation remains advisory even when it is present in state.
	if got := relationalWALValues(infraVars(perfState(false))); len(got) != 0 {
		t.Fatalf("preset applied relational WAL recommendation: %v", got)
	}
}

// Reload-only WAL changes are safe with running instances, but HA still is not.
func TestRelationalWALChangesWithRunningInstances(t *testing.T) {
	value := 4096
	for _, tc := range []struct {
		name     string
		previous *int
		next     int
		changeHA bool
	}{
		{name: "legacy to explicit", next: 4096},
		{name: "increase", previous: &value, next: 8192},
		{name: "reset", previous: &value, next: 0},
		{name: "HA control", previous: &value, next: 4096, changeHA: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			rec := aCompleteInstall()
			rec.Settings.RelationalStoreMaxWALMiB = tc.previous
			typed := fake.NewSimpleClientset()
			if err := writeInstalled(ctx, typed, rec, installClock); err != nil {
				t.Fatal(err)
			}
			orig := installClients
			t.Cleanup(func() { installClients = orig })
			installClients = func(string) (dynamic.Interface, kubernetes.Interface, error) { return nil, typed, nil }
			withClusterInstances(t, []string{"prod"}, nil)
			st, settings := reinstallState()
			st.RelationalStoreMaxWALMiB = &tc.next
			if tc.changeHA {
				settings.HA = !settings.HA
			}
			_, _, err := connectAndRefuse(ctx, st, settings, stateHere())
			if tc.changeHA {
				if err == nil || !strings.Contains(err.Error(), "refusing to change cluster") {
					t.Fatalf("HA change must be refused: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("reload-only WAL change refused: %v", err)
			}
			vars, err := installClusterVars(ctx, st, nil)
			if err != nil {
				t.Fatal(err)
			}
			want := []string{"postgres_max_wal_size_mib=" + strconv.Itoa(tc.next)}
			if got := relationalWALValues(vars); !reflect.DeepEqual(got, want) {
				t.Fatalf("WAL emission = %v, want %v", got, want)
			}
		})
	}
}
