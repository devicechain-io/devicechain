// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// withClusterInstances stubs the seam refuseAReinstallThatWouldHurt asks which instances
// run on the cluster through, and counts the asks.
func withClusterInstances(t *testing.T, ids []string, err error) *int {
	t.Helper()
	prev := readClusterInstances
	t.Cleanup(func() { readClusterInstances = prev })
	calls := 0
	readClusterInstances = func(context.Context, string) (clusterInstances, error) {
		calls++
		return clusterInstances{IDs: ids, Source: "test"}, err
	}
	return &calls
}

func stateHere() func(string) (bool, error) {
	return func(string) (bool, error) { return true, nil }
}

// reinstallState is a re-install asking for exactly what aCompleteInstall recorded.
func reinstallState() (*State, InstallSettings) {
	rec := aCompleteInstall()
	st := &State{ClusterUID: testClusterUID, KubeContext: "kind-devicechain",
		MaxConnections: rec.Outputs.Rdb.MaxConnections, Values: map[string]string{}}
	return st, rec.Settings
}

func installed() *InstallRecord {
	rec := aCompleteInstall()
	rec.Phase = installPhaseInstalled
	return &rec
}

// A first install, and a re-install of one that never finished, have nothing to hurt.
func TestAnInstallWithNoFinishedPredecessorIsNotRefused(t *testing.T) {
	calls := withClusterInstances(t, []string{"prod"}, nil)
	st, settings := reinstallState()
	settings.HA = true // a change, which would matter over a finished install
	noState := func(string) (bool, error) { return false, nil }

	if err := refuseAReinstallThatWouldHurt(context.Background(), st, nil, settings, noState); err != nil {
		t.Errorf("a first install was refused: %v", err)
	}
	applying := installed()
	applying.Phase = installPhaseApplying
	if err := refuseAReinstallThatWouldHurt(context.Background(), st, applying, settings, noState); err != nil {
		t.Errorf("re-running an install that did not finish was refused: %v", err)
	}
	if *calls != 0 {
		t.Errorf("the cluster's instances were read %d time(s) for an install with nothing to hurt", *calls)
	}
}

// 🔴 A RE-INSTALL FROM A MACHINE WITHOUT THE STATE is refused, whatever it asks for:
// empty state plans every prerequisite as new.
func TestAReinstallFromAnotherMachineIsRefused(t *testing.T) {
	withClusterInstances(t, nil, nil)
	st, settings := reinstallState()
	err := refuseAReinstallThatWouldHurt(context.Background(), st, installed(), settings,
		func(uid string) (bool, error) {
			if uid != testClusterUID {
				t.Errorf("the state was looked for under %q, not the cluster's identity", uid)
			}
			return false, nil
		})
	if err == nil || !strings.Contains(err.Error(), "another machine") {
		t.Errorf("a re-install with no local state was not refused as another machine's: %v", err)
	}

	// ...and "could not tell" is not "it is here".
	err = refuseAReinstallThatWouldHurt(context.Background(), st, installed(), settings,
		func(string) (bool, error) { return false, errors.New("permission denied") })
	if err == nil || !strings.Contains(err.Error(), "permission denied") {
		t.Errorf("an unreadable state directory was not refused with its cause: %v", err)
	}
}

// 🔴 NEW SETTINGS UNDER RUNNING INSTANCES are refused, naming them; the same change on a
// cluster with none is allowed, and so is asking for what is already there.
func TestAReinstallThatChangesTheClusterUnderItsInstancesIsRefused(t *testing.T) {
	for _, tc := range []struct {
		name      string
		change    func(*State, *InstallSettings)
		instances []string
		listErr   error
		refused   bool
		mentions  []string
	}{
		{name: "same settings, instances running",
			change: func(*State, *InstallSettings) {}, instances: []string{"prod"}},
		{name: "backups turned off, instances running",
			change:    func(_ *State, s *InstallSettings) { s.DatabaseBackups = false },
			instances: []string{"prod", "staging"}, refused: true, mentions: []string{"prod", "staging"}},
		{name: "backups turned off, no instances",
			change: func(_ *State, s *InstallSettings) { s.DatabaseBackups = false }},
		{name: "budget raised, instances running",
			change:    func(st *State, _ *InstallSettings) { st.MaxConnections = 1200 },
			instances: []string{"prod"}},
		{name: "budget lowered, instances running",
			change:    func(st *State, _ *InstallSettings) { st.MaxConnections = 300 },
			instances: []string{"prod"}, refused: true, mentions: []string{"prod", "600 → 300"}},
		{name: "settings changed, instances unreadable",
			change:  func(_ *State, s *InstallSettings) { s.HA = true },
			listErr: errors.New("the server is unavailable"), refused: true,
			mentions: []string{"the server is unavailable"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withClusterInstances(t, tc.instances, tc.listErr)
			st, settings := reinstallState()
			tc.change(st, &settings)
			err := refuseAReinstallThatWouldHurt(context.Background(), st, installed(), settings, stateHere())
			if (err != nil) != tc.refused {
				t.Fatalf("refused=%t, want %t: %v", err != nil, tc.refused, err)
			}
			for _, want := range tc.mentions {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("the refusal does not mention %q: %v", want, err)
				}
			}
		})
	}
}

// 🔴 EVERY FIELD IS OVERWRITTEN, NONE MERGED — in both directions, because a state that
// already says "HA" must follow a record that says "no HA".
func TestABootstrapFollowsEveryFieldOfTheInstall(t *testing.T) {
	for _, on := range []bool{false, true} {
		t.Run(fmt.Sprintf("record says %t", on), func(t *testing.T) {
			rec := aCompleteInstall()
			rec.Settings = InstallSettings{HA: on, Compact: on, Monitoring: on, CNPG: on}
			rec.Outputs.BackupSurvivesClusterLoss = on
			st := &State{
				HA: !on, Compact: !on, NoMonitoring: on, NoCNPG: on,
				Values: map[string]string{
					databaseNamespaceKey: "stale", cnpgNamespaceKey: "stale", "grafanaService": "stale",
					"grafanaNamespace": "stale", databaseBackupOffsiteKey: strconv.FormatBool(!on),
				},
			}
			FollowInstall(st, &rec)

			if st.Install != &rec {
				t.Error("the state does not carry the record it follows")
			}
			if st.HA != on || st.Compact != on || st.NoMonitoring == on || st.NoCNPG == on {
				t.Errorf("followed %+v into HA=%t Compact=%t NoMonitoring=%t NoCNPG=%t",
					rec.Settings, st.HA, st.Compact, st.NoMonitoring, st.NoCNPG)
			}
			for key, want := range map[string]string{
				databaseNamespaceKey:     rec.Outputs.Rdb.Namespace,
				cnpgNamespaceKey:         rec.Outputs.CNPGNamespace,
				"grafanaService":         rec.Outputs.GrafanaService,
				"grafanaNamespace":       rec.Outputs.GrafanaNamespace,
				databaseBackupOffsiteKey: strconv.FormatBool(on),
			} {
				if got := st.Values[key]; got != want {
					t.Errorf("Values[%q] = %q, want the record's %q", key, got, want)
				}
			}
		})
	}
}

// The command a refusal prints is the one that prepares the cluster the refused command
// was aimed at.
func TestTheInstallCommandNamesTheClusterItPrepares(t *testing.T) {
	for _, tc := range []struct{ cluster, kubeContext, want string }{
		{"", "", "dcctl install local"},
		{DefaultClusterName, "", "dcctl install local"},
		{"edge", "", "dcctl install local --cluster edge"},
		// A kube-context names the cluster exactly; a cluster name alongside it is not
		// what selected it.
		{"edge", "kind-other", "dcctl install local --kube-context kind-other"},
	} {
		if got := InstallCommand("local", tc.cluster, tc.kubeContext); got != tc.want {
			t.Errorf("InstallCommand(local, %q, %q) = %q, want %q", tc.cluster, tc.kubeContext, got, tc.want)
		}
	}
}

// Only "not installed" is answered with the install command; any other reason a record
// is unusable is passed through as itself, since installing is not its remedy.
func TestOnlyAnUninstalledClusterIsSentToInstall(t *testing.T) {
	const cmd = "dcctl install local --cluster edge"
	notInstalled := fmt.Errorf("%w: there is no install record", ErrNotInstalled)
	got := refuseUninstalled(notInstalled, cmd)
	if !errors.Is(got, ErrNotInstalled) {
		t.Errorf("the refusal lost its cause: %v", got)
	}
	if !strings.Contains(got.Error(), cmd) || !strings.Contains(got.Error(), "there is no install record") {
		t.Errorf("the refusal does not carry both the reason and the command: %v", got)
	}

	other := errors.New("the install record belongs to cluster x, not this one")
	if got := refuseUninstalled(other, cmd); got != other {
		t.Errorf("a record from elsewhere was rewritten as %v", got)
	}
}

// An instance's connection limit is every relational area it runs, at a full pool,
// mid-rollout — from its profile, or from the areas it names.
func TestAnInstancesConnectionLimitCountsItsRelationalAreas(t *testing.T) {
	per := servicePoolSize * rolloutSurge
	for _, tc := range []struct {
		name string
		st   *State
		want int
	}{
		// user, device, state, dashboards, commands, notifications, processing.
		{"no profile is the default", &State{}, 7 * per},
		{"default", &State{Profile: "default"}, 7 * per},
		// ...plus ai-inference and outbound-connectors; mcp and the ingests hold none.
		{"full", &State{Profile: "full"}, 9 * per},
		{"explicit areas win over the profile", &State{Profile: "full",
			EnabledAreas: []string{"user-management", "event-sources", "event-management", "mcp"}}, 1 * per},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := instanceConnectionLimit(tc.st)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Errorf("limit %d, want %d", got, tc.want)
			}
		})
	}
	if _, err := instanceConnectionLimit(&State{Profile: "no-such-profile"}); err == nil {
		t.Error("an unknown profile was sized rather than refused")
	}
}

// 🔴 THE BUDGET IS COUNTED FROM A LIST, AND THE LIST IS HELD AGAINST THE SERVICES. An area
// that starts opening the relational store without joining relationalAreas is an
// instance whose login refuses its connections at the limit; one that stops leaves every
// instance reserving connections nothing uses. The services' source is the witness: a
// service opens the store through its configuration's Persistence.Rdb.
func TestTheRelationalAreasAreTheServicesThatOpenTheRelationalStore(t *testing.T) {
	root := filepath.Join("..", "..", "services")
	dirs, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("reading the services: %v", err)
	}
	var opening []string
	for _, d := range dirs {
		if !d.IsDir() {
			continue
		}
		found := false
		err := filepath.WalkDir(filepath.Join(root, d.Name()), func(path string, e fs.DirEntry, err error) error {
			if err != nil || found || e.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return err
			}
			src, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			found = strings.Contains(string(src), "Persistence.Rdb")
			return nil
		})
		if err != nil {
			t.Fatalf("scanning %s: %v", d.Name(), err)
		}
		if found {
			opening = append(opening, d.Name())
		}
	}
	if len(opening) == 0 {
		t.Fatal("no service opens the relational store, so the scan is looking in the wrong place")
	}

	var listed []string
	for a := range relationalAreas {
		listed = append(listed, string(a))
	}
	sort.Strings(opening)
	sort.Strings(listed)
	if strings.Join(opening, ",") != strings.Join(listed, ",") {
		t.Errorf("the services that open the relational store are\n  %v\nbut relationalAreas counts\n  %v",
			opening, listed)
	}

	// ...and each is counted at the pool the shared library opens.
	src, err := os.ReadFile(filepath.Join("..", "..", "core", "rdb", "postgres.go"))
	if err != nil {
		t.Fatalf("reading the relational store library: %v", err)
	}
	m := regexp.MustCompile(`defaultMaxOpenConnections\s*=\s*(\d+)`).FindSubmatch(src)
	if m == nil {
		t.Fatal("backend/core/rdb/postgres.go no longer declares defaultMaxOpenConnections; " +
			"servicePoolSize mirrors a value that is not there")
	}
	if got, _ := strconv.Atoi(string(m[1])); got != servicePoolSize {
		t.Errorf("services open pools of %d connections and each is budgeted at %d", got, servicePoolSize)
	}
}
