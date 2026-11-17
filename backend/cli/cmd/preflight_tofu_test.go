// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"io/fs"
	"reflect"
	"testing"
	"testing/fstest"
)

func TestToolingChecksSelectedTofuVersion(t *testing.T) {
	for _, tc := range []struct {
		name, output  string
		terraformOnly bool
		fails         int
	}{
		{"old tofu", `{"terraform_version":"1.9.1"}`, false, 1},
		{"floor", `{"terraform_version":"1.10.0"}`, false, 0},
		{"newer", `{"terraform_version":"1.11.2"}`, false, 0},
		{"old terraform", `{"terraform_version":"1.9.8"}`, true, 1},
		{"terraform floor", `{"terraform_version":"1.10.0"}`, true, 0},
		{"prerelease", `{"terraform_version":"1.10.0-rc1"}`, false, 1},
		{"command failed", "", false, 1},
		{"malformed", "not json", false, 1},
		{"missing version", `{}`, false, 1},
		{"invalid version", `{"terraform_version":"unknown"}`, false, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fakeHost(t, func(n string) bool { return !tc.terraformOnly || n != "tofu" })
			selected := "/fake/tofu"
			if tc.terraformOnly {
				selected = "/fake/terraform"
			}
			var calls []string
			runCmd = func(n string, args ...string) string {
				calls = append(calls, n)
				if !reflect.DeepEqual(args, []string{"version", "-json"}) {
					t.Errorf("version args = %v", args)
				}
				if n != selected {
					t.Errorf("queried %s, apply would use %s", n, selected)
					return `{"terraform_version":"1.99.0"}`
				}
				return tc.output
			}
			d := &doctor{}
			d.checkTools("local", doctorOpts{KubeContext: "prod"})
			if d.fails != tc.fails || len(calls) != 1 {
				t.Fatalf("failures=%d calls=%v, want %d failures and one version query", d.fails, calls, tc.fails)
			}
		})
	}
}

func TestTofuRequirementsFollowBothRootsAndFailClosed(t *testing.T) {
	root := func(source string) fs.FS {
		return fstest.MapFS{"versions.tf": &fstest.MapFile{Data: []byte(source)}}
	}
	floor := root(`required_version = ">= 1.10"`)
	higher := root(`required_version = ">= 1.12.3"`)
	for _, tc := range []struct {
		name  string
		roots []fs.FS
		fail  bool
	}{
		{"floor", []fs.FS{floor, floor}, false},
		{"cluster moves", []fs.FS{higher, floor}, true},
		{"instance moves", []fs.FS{floor, higher}, true},
		{"missing file", []fs.FS{fstest.MapFS{}}, true},
		{"missing declaration", []fs.FS{root("")}, true},
		{"unsupported constraint", []fs.FS{root(`required_version = "~> 1.10"`)}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := checkTofuRequirements(`{"terraform_version":"1.10.0"}`, tc.roots...)
			if (err != nil) != tc.fail {
				t.Fatalf("error=%v, want failure=%v", err, tc.fail)
			}
		})
	}
}
