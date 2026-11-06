// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tfjson "github.com/hashicorp/terraform-json"
)

func walValues(maxWAL string) map[string]interface{} {
	p := map[string]interface{}{"wal_compression": "lz4"}
	if maxWAL != "" {
		p["max_wal_size"] = maxWAL
	}
	return map[string]interface{}{"parameters": p}
}

func reviewTsdb(t *testing.T, before, after map[string]interface{}) planReview {
	t.Helper()
	rc := releaseChange(t, tsdbReleaseAddress, "helm_release", tfjson.Actions{tfjson.ActionUpdate}, before, after)
	out, err := reviewUpgradePlan("prod", &tfjson.Plan{ResourceChanges: []*tfjson.ResourceChange{rc}}, nobody)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// The drill's case: an existing store with no max_wal_size set goes to a fifth of 32Gi.
func TestPlanReviewFlagsAMaxWALRise(t *testing.T) {
	out := reviewTsdb(t, walValues(""), walValues("6553MB"))
	if out.WALRise == nil || out.WALRise.OldMiB != 1024 || out.WALRise.NewMiB != 6553 {
		t.Fatalf("WALRise = %+v, want 1024 -> 6553", out.WALRise)
	}
	n := out.WALRise.notice()
	for _, want := range []string{"1024 MiB", "6553 MiB", "--event-store-max-wal-mib"} {
		if !strings.Contains(n, want) {
			t.Errorf("notice %q lacks %q", n, want)
		}
	}
}

// The negative controls: no rise, no notice.
func TestPlanReviewSaysNothingWithoutAMaxWALRise(t *testing.T) {
	for name, c := range map[string]struct{ before, after map[string]interface{} }{
		"already at the new value": {walValues("6553MB"), walValues("6553MB")},
		"kept at the stock value":  {walValues(""), walValues("1024MB")},
		"lowered":                  {walValues("6553MB"), walValues("2048MB")},
		"no max_wal_size at all":   {walValues(""), walValues("")},
	} {
		t.Run(name, func(t *testing.T) {
			if out := reviewTsdb(t, c.before, c.after); out.WALRise != nil {
				t.Fatalf("WALRise = %+v, want none", out.WALRise)
			}
		})
	}
}

func TestParseMiB(t *testing.T) {
	for in, want := range map[string]int{"6553MB": 6553, "1GB": 1024, "16384kB": 16, "2TB": 2 * 1024 * 1024, "512": 512} {
		if got, ok := parseMiB(in); !ok || got != want {
			t.Errorf("parseMiB(%q) = %d, %v; want %d", in, got, ok, want)
		}
	}
	for _, in := range []string{"", "lots", "1.5GB", "-1MB"} {
		if _, ok := parseMiB(in); ok {
			t.Errorf("parseMiB(%q) accepted", in)
		}
	}
}

func TestCheckEventStoreTuning(t *testing.T) {
	ok := []struct {
		w *int
		c *string
	}{{nil, nil}, {intp(0), nil}, {intp(64), nil}, {intp(1024), strp("15min")}, {nil, strp("30s")}, {nil, strp("1d")}, {nil, strp("")}}
	for _, c := range ok {
		if err := checkEventStoreTuning(c.w, c.c); err != nil {
			t.Errorf("rejected %v %v: %v", c.w, c.c, err)
		}
	}
	bad := []struct {
		w *int
		c *string
	}{{intp(63), nil}, {intp(-1), nil}, {nil, strp("29s")}, {nil, strp("2d")}, {nil, strp("fast")}, {nil, strp("15 min")}}
	for _, c := range bad {
		if err := checkEventStoreTuning(c.w, c.c); err == nil {
			t.Errorf("accepted %v %v", c.w, c.c)
		}
	}
}

func TestEventStoreTuningVarsOnlyWhenGiven(t *testing.T) {
	if got := eventStoreTuningVars(&State{}); len(got) != 0 {
		t.Fatalf("flagless run emitted %v", got)
	}
	got := eventStoreTuningVars(&State{EventStoreMaxWALMiB: intp(1024), EventStoreCheckpointTimeout: strp("10min")})
	if len(got) != 2 || got[0] != "timescale_max_wal_size_mib=1024" || got[1] != "timescale_checkpoint_timeout=10min" {
		t.Fatalf("vars = %v", got)
	}
	// 0 is a value: it passes the root's "derive it again".
	if got := eventStoreTuningVars(&State{EventStoreMaxWALMiB: intp(0)}); len(got) != 1 || got[0] != "timescale_max_wal_size_mib=0" {
		t.Fatalf("vars = %v", got)
	}
}

func TestEventStoreVarFileIsKeptMergedAndCleared(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, eventStoreVarFile)
	read := func() string {
		b, err := os.ReadFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				return ""
			}
			t.Fatal(err)
		}
		return string(b)
	}
	// A flagless run writes nothing.
	if err := writeEventStoreVarFile(dir, &State{Instance: "i"}); err != nil || read() != "" {
		t.Fatalf("flagless: err=%v file=%q", err, read())
	}
	if err := writeEventStoreVarFile(dir, &State{Instance: "i", EventStoreMaxWALMiB: intp(1024)}); err != nil {
		t.Fatal(err)
	}
	if got := read(); got != `{"timescale_max_wal_size_mib":1024}` {
		t.Fatalf("file = %q", got)
	}
	// The second flag merges; the first is kept.
	if err := writeEventStoreVarFile(dir, &State{Instance: "i", EventStoreCheckpointTimeout: strp("10min")}); err != nil {
		t.Fatal(err)
	}
	if got := read(); got != `{"timescale_checkpoint_timeout":"10min","timescale_max_wal_size_mib":1024}` {
		t.Fatalf("file = %q", got)
	}
	// 0 returns the allowance to derived; the timeout stays.
	if err := writeEventStoreVarFile(dir, &State{Instance: "i", EventStoreMaxWALMiB: intp(0)}); err != nil {
		t.Fatal(err)
	}
	if got := read(); got != `{"timescale_checkpoint_timeout":"10min"}` {
		t.Fatalf("file = %q", got)
	}
	// And the file is statedByOperator's evidence for the plan review.
	if !statedByOperator(dir, "timescale_checkpoint_timeout") || statedByOperator(dir, "timescale_max_wal_size_mib") {
		t.Fatal("statedByOperator does not read the recorded file")
	}
	if err := os.WriteFile(path, []byte(`{"timescale_max_wal_size_mib":1024}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeEventStoreVarFile(dir, &State{Instance: "i", EventStoreMaxWALMiB: intp(0)}); err != nil {
		t.Fatal(err)
	}
	if read() != "" {
		t.Fatalf("an emptied file should be removed, got %q", read())
	}
}

func TestParseDfFreeMiB(t *testing.T) {
	out := "Filesystem 1024-blocks Used Available Capacity Mounted on\n/dev/sdb 32894976 30000000 2048000 94% /var/lib/postgresql/data\n"
	if got, err := parseDfFreeMiB(out); err != nil || got != 2000 {
		t.Fatalf("got %d, %v", got, err)
	}
	for _, bad := range []string{"", "Filesystem\n", "a\nb c\n", "a\nb c d x\n"} {
		if _, err := parseDfFreeMiB(bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}

func TestWALNoticesWarnOnlyBelowTheNewAllowance(t *testing.T) {
	defer func(f func(context.Context, *State) (int, error)) { eventStoreFreeMiB = f }(eventStoreFreeMiB)
	r := walRise{OldMiB: 1024, NewMiB: 6553}
	run := func(free int, err error) (string, []string) {
		eventStoreFreeMiB = func(context.Context, *State) (int, error) { return free, err }
		return walNotices(context.Background(), &State{}, r)
	}
	if n, w := run(20000, nil); n == "" || len(w) != 0 {
		t.Fatalf("roomy volume: notice %q warnings %v", n, w)
	}
	n, w := run(3000, nil)
	if n == "" || len(w) != 1 || !strings.Contains(w[0], "3000 MiB free") || !strings.Contains(w[0], "6553 MiB") {
		t.Fatalf("tight volume: notice %q warnings %v", n, w)
	}
	// Free exactly at the allowance is not below it.
	if _, w := run(6553, nil); len(w) != 0 {
		t.Fatalf("exact fit warned: %v", w)
	}
	// An unreadable volume says so rather than passing silently.
	if _, w := run(0, errors.New("exec refused")); len(w) != 1 || !strings.Contains(w[0], "could not read") {
		t.Fatalf("unreadable volume: %v", w)
	}
}

// The plan prints the notice, and the warning in red, and nothing when the allowance does not rise.
func TestPlanSaysTheWALNotice(t *testing.T) {
	p := &upgradeInfraPlan{walNotice: "rises", walWarnings: []string{"tight"}}
	out := captureStdout(t, p.say)
	if !strings.Contains(out, "rises") || !strings.Contains(out, "tight") {
		t.Fatalf("plan output lacks the notice: %q", out)
	}
	if out := captureStdout(t, (&upgradeInfraPlan{}).say); strings.Contains(out, "max_wal_size") {
		t.Fatalf("unprompted notice: %q", out)
	}
}
