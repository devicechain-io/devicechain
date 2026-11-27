// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/devicechain-io/dcctl/bootstrap"
	"github.com/devicechain-io/dcctl/internal/installflow"
	"github.com/devicechain-io/dcctl/internal/installplain"
)

func TestInstallDockerCapacity(t *testing.T) {
	for _, tc := range []struct {
		name, cpus, memory string
		argv               []string
		fails, warns       int
	}{
		{"small CPU", "2", "17179869184", nil, 1, 0},
		{"three CPUs", "3", "17179869184", nil, 1, 0},
		{"small memory", "8", "4294967296", nil, 1, 0},
		{"compact", "2", "17179869184", []string{"--compact"}, 0, 1},
		{"size compact", "2", "17179869184", []string{"--size=compact"}, 0, 1},
		{"compact small memory", "8", "4294967296", []string{"--compact"}, 0, 1},
		{"ample", "8", "17179869184", nil, 0, 0},
		{"exact thresholds", "4", "8589934592", nil, 0, 0},
		// Memory refuses below 7.5GiB on the exact byte value and only warns up to 8Gi,
		// so a Docker Desktop given "8 GB" (about 7.7GiB) is advised, never refused.
		{"7.4GiB refused", "8", "7945689498", nil, 1, 0},
		{"one byte under 7.5GiB refused", "8", "8053063679", nil, 1, 0},
		{"exactly 7.5GiB warns", "8", "8053063680", nil, 0, 1},
		{"7.6GiB warns", "8", "8160437862", nil, 0, 1},
		{"Docker Desktop 8 GB warns", "8", "8267812045", nil, 0, 1},
		{"8GiB passes", "8", "8589934592", nil, 0, 0},
		{"unreadable", "", "not-a-number", nil, 0, 1},
		{"unreadable CPU", "not-a-number", "", nil, 0, 1},
		{"zero CPU", "0", "17179869184", nil, 0, 1},
		{"compact unknown CPU", "", "17179869184", []string{"--compact"}, 0, 1},
		{"existing cluster", "2", "4294967296", []string{"--kube-context=prod", "--build=false"}, 0, 0},
		{"build for existing cluster", "2", "4294967296", []string{"--kube-context=prod"}, 0, 4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fakeDockerCapacity(t, tc.cpus, tc.memory)
			fakeChecks(t)
			// Stop every run at preflight, even when capacity passes. A missing
			// context stops remote-image installs without ever touching a cluster.
			writeKubeconfig(t)
			var checked *doctor
			systemCheck = func(d *doctor, _ string, _ bool) {
				checked = d
				d.fails++ // test-only stop, excluded from the assertions below
			}
			flags := installCmd.Flags()
			version := bootstrap.DefaultImageVersion
			bootstrap.DefaultImageVersion = "v1.8.0"
			t.Cleanup(func() { bootstrap.DefaultImageVersion = version })
			for _, name := range []string{"build", "compact", "size", "kube-context", "skip-preflight", "no-tls", "no-monitoring"} {
				f := flags.Lookup(name)
				value, changed := f.Value.String(), f.Changed
				t.Cleanup(func() {
					if err := f.Value.Set(value); err != nil {
						t.Fatal(err)
					}
					f.Changed = changed
				})
			}
			if err := flags.Lookup("compact").Value.Set("false"); err != nil {
				t.Fatal(err)
			}
			flags.Lookup("compact").Changed = false
			argv := append([]string{"--build", "--size=", "--kube-context=", "--skip-preflight=false"}, tc.argv...)
			output := capacityOutput(t, func() {
				if err := flags.Parse(argv); err != nil {
					t.Fatal(err)
				}
				if err := installCmd.RunE(installCmd, []string{"local"}); err == nil || !strings.Contains(err.Error(), "preflight check(s) failed") {
					t.Fatalf("expected preflight stop, got %v", err)
				}
			})
			if tc.name == "existing cluster" {
				if checked != nil || strings.Contains(output, "engine CPUs:") || strings.Contains(output, "engine memory:") {
					t.Fatalf("remote-image install checked Docker: %s", output)
				}
				return
			}
			if checked == nil {
				t.Fatal("install did not reach the doctor")
			}
			// The missing --kube-context contributes one unrelated failure.
			failures := checked.fails - 1
			if installKubeContext != "" {
				failures--
			}
			if failures != tc.fails || checked.warns != tc.warns {
				t.Errorf("capacity failures=%d warnings=%d, want %d/%d\n%s", failures, checked.warns, tc.fails, tc.warns, output)
			}
			if tc.fails > 0 || tc.warns > 0 && !strings.Contains(output, "UNKNOWN engine CPUs:") {
				if !strings.Contains(output, "--compact") {
					t.Errorf("capacity guidance must name --compact:\n%s", output)
				}
			}
		})
	}
}

func TestUnknownDockerCPUIsVisibleAndAdvisory(t *testing.T) {
	for _, cpus := range []string{"", "not-a-number", "0", "-1", "999999999999999999999999999999999999"} {
		for _, fullSize := range []bool{false, true} {
			t.Run(fmt.Sprintf("cpus=%q/fullSize=%v", cpus, fullSize), func(t *testing.T) {
				fakeDockerCapacity(t, cpus, "17179869184")
				var observations []installflow.PreflightObservation
				d := &doctor{observe: func(o installflow.PreflightObservation) {
					observations = append(observations, o)
				}}
				d.checkDocker(doctorOpts{FullSizeInstall: fullSize})
				d.summary()
				if d.fails != 0 || d.warns != 1 {
					t.Fatalf("unknown CPU must be advisory, never pass or fail: fails=%d warns=%d", d.fails, d.warns)
				}
				var cpuRows int
				section := installflow.MessageCode("")
				for _, o := range observations {
					if o.Observation == installflow.ObserveSection {
						section = o.Section
					}
					if strings.HasPrefix(o.Text.String(), "engine CPUs:") {
						cpuRows++
						if section != installplain.SectionDockerEngine || o.Verdict != installflow.CheckUnknown || o.Fix.IsZero() {
							t.Errorf("CPU row must be UNKNOWN in Docker engine with a fix: %+v", o)
						}
					}
				}
				if cpuRows != 1 {
					t.Fatalf("got %d CPU rows, want exactly one UNKNOWN", cpuRows)
				}
				summary := observations[len(observations)-1]
				if summary.Observation != installflow.ObserveSummary || summary.Fails != 0 || summary.Warns != 1 {
					t.Fatalf("unknown CPU lost from summary: %+v", summary)
				}
			})
		}
	}
}

// Negative control for the byte-exact memory floor: the floored-GiB reading the doctor
// prints, compared against the same 7.5GiB floor, refuses a Docker Desktop given "8 GB"
// (about 7.7GiB). engineMemoryRefused must not.
func TestEngineMemoryFloorIsByteExact(t *testing.T) {
	const desktop8GB int64 = 8267812045 // 7.7GiB
	floored := desktop8GB / (1 << 30)
	if !(floored<<30 < engineMemoryRefuseBelow) {
		t.Fatalf("control is vacuous: floored %dGi is not below the 7.5GiB floor", floored)
	}
	if engineMemoryRefused(desktop8GB) {
		t.Fatal("a 7.7GiB engine was refused; the floor must compare the exact byte value")
	}
	if !engineMemoryRefused(7945689498) { // 7.4GiB
		t.Fatal("a 7.4GiB engine was not refused")
	}
}

func TestPreflightDockerCapacityRemainsAWarning(t *testing.T) {
	fakeDockerCapacity(t, "2", "17179869184")
	d := &doctor{}
	output := capacityOutput(t, func() { d.checkDocker(doctorOptsFor("", "lab", false)) })
	if d.fails != 0 || d.warns != 1 || !strings.Contains(output, "--compact") {
		t.Fatalf("plain preflight must warn with --compact guidance: failures=%d warnings=%d\n%s", d.fails, d.warns, output)
	}
}

func fakeDockerCapacity(t *testing.T, cpus, memory string) {
	t.Helper()
	fakeHost(t, func(string) bool { return true })
	baseRun := runCmd
	runCmd = func(name string, args ...string) string {
		if name == "docker" {
			switch strings.Join(args, " ") {
			case "info --format {{.ServerVersion}}":
				return "29.0.0"
			case "info --format {{.NCPU}}":
				return cpus
			case "info --format {{.MemTotal}}":
				return memory
			}
		}
		return baseRun(name, args...)
	}
}

func capacityOutput(t *testing.T, fn func()) string {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "stdout")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	original := os.Stdout
	os.Stdout = f
	defer func() { os.Stdout = original }()
	fn()
	b, err := os.ReadFile(f.Name())
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
