// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"runtime/debug"
	"testing"

	coreauth "github.com/devicechain-io/dc-microservice/auth"
)

func TestServedVersionIsBuildVersion(t *testing.T) {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		t.Fatal("module build information unavailable")
	}
	want := info.Main.Version
	if want == "" || want == "(devel)" {
		for _, setting := range info.Settings {
			if setting.Key == "vcs.revision" {
				want = setting.Value
				break
			}
		}
	}
	cs, _ := skillSession(t, "acme", coreauth.ScopeReadOnly, false)
	got := cs.InitializeResult().ServerInfo.Version
	if got != want || got == "0.1.0" {
		t.Fatalf("serverInfo.version = %q, want build version %q (never 0.1.0)", got, want)
	}
}

func TestVersionFromBuildInfo(t *testing.T) {
	for _, tc := range []struct {
		name     string
		version  string
		revision string
		want     string
	}{
		{"module version takes precedence", "v1.10.2", "abc123", "v1.10.2"},
		{"development module uses revision", "(devel)", "abc123", "abc123"},
		{"empty module uses revision", "", "abc123", "abc123"},
		{"unstamped module retains its metadata", "(devel)", "", "(devel)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			info := &debug.BuildInfo{Main: debug.Module{Version: tc.version},
				Settings: []debug.BuildSetting{{Key: "vcs.revision", Value: tc.revision}}}
			if got := versionFromBuildInfo(info); got != tc.want {
				t.Fatalf("versionFromBuildInfo = %q, want %q", got, tc.want)
			}
		})
	}
}
