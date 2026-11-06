// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

// The event store's WAL allowance (max_wal_size) and checkpoint cadence
// (checkpoint_timeout) on an upgrade.
//
// An upgrade applies the instance root to an existing event store, and the root derives
// max_wal_size as a fifth of the volume (never below Postgres's own 1 GiB). On the default
// 32Gi that raises an existing store from the stock 1 GiB to 6.4 GiB, and pg_wal -- which
// lives on the data volume -- may grow that far between checkpoints. A store already close
// to full has less room than that to give, so three things are here:
//
//   - the flags --event-store-max-wal-mib and --event-store-checkpoint-timeout on
//     `dcctl upgrade`, which reach the root variables of the same names. They are passed to
//     the plan (so --dry-run shows their effect) and, once the apply succeeds, written to a
//     variable file beside the instance's state so that every LATER upgrade keeps them. A
//     value passed only with -var would be reverted by the next flagless run, which is the
//     silent revert the tuning overlay exists to prevent;
//   - a one-line notice, printed with the plan, when the effective max_wal_size rises;
//   - a WARNING, not a refusal, when the volume's free space is below the new allowance.
//
// 🔴 WHY A WARNING AND NOT A REFUSAL. max_wal_size is a soft limit: pg_wal reaches it only
// when checkpoints are requested faster than they complete or archiving falls behind, and
// a store with little free space may be perfectly healthy at its current rate. Refusing
// would block an upgrade that has a one-flag remedy and that is safe for most such stores,
// and the free-space reading is itself best effort (it execs df in the primary pod, which
// can fail). The other preflights that refuse are refusals of things that cost data
// regardless of load; this one costs only headroom, so it says so loudly and leaves the
// choice, with the flag that applies it, to the operator.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

const (
	// eventStoreVarFile carries the operator's event store tuning between upgrades. OpenTofu
	// loads a *.auto.tfvars.json in the root's directory on every plan and apply.
	eventStoreVarFile = "dcctl-event-store.auto.tfvars.json"

	varMaxWALMiB         = "timescale_max_wal_size_mib"
	varCheckpointTimeout = "timescale_checkpoint_timeout"

	// stockMaxWALMiB is Postgres's own max_wal_size, what an event store that was never given
	// one runs with.
	stockMaxWALMiB = 1024
	// minMaxWALMiB mirrors the root's validation of timescale_max_wal_size_mib.
	minMaxWALMiB = 64
)

var (
	checkpointTimeoutRe = regexp.MustCompile(`^([0-9]+)(ms|s|min|h|d)?$`)
	pgSizeRe            = regexp.MustCompile(`^\s*([0-9]+)\s*(kB|MB|GB|TB)?\s*$`)
)

// checkEventStoreTuning judges the two flags against the same bounds the root enforces, so
// a bad value is refused before anything is planned.
func checkEventStoreTuning(walMiB *int, timeout *string) error {
	if walMiB != nil && *walMiB != 0 && *walMiB < minMaxWALMiB {
		return fmt.Errorf("--event-store-max-wal-mib is a whole number of MiB of at least %d, or 0 to "+
			"derive it from the volume again; got %d", minMaxWALMiB, *walMiB)
	}
	if timeout == nil || *timeout == "" {
		return nil
	}
	m := checkpointTimeoutRe.FindStringSubmatch(*timeout)
	if m == nil {
		return fmt.Errorf("--event-store-checkpoint-timeout is a Postgres time with an optional unit "+
			"(ms, s, min, h, d), such as 15min; got %q", *timeout)
	}
	n, _ := strconv.ParseFloat(m[1], 64)
	unit := map[string]float64{"ms": 0.001, "s": 1, "": 1, "min": 60, "h": 3600, "d": 86400}[m[2]]
	if secs := n * unit; secs < 30 || secs > 86400 {
		return fmt.Errorf("--event-store-checkpoint-timeout must be between 30 seconds and 1 day; got %q", *timeout)
	}
	return nil
}

// eventStoreTuningVars are the -var assignments for the flags this run was given.
func eventStoreTuningVars(st *State) []string {
	var out []string
	if st.EventStoreMaxWALMiB != nil {
		out = append(out, fmt.Sprintf("%s=%d", varMaxWALMiB, *st.EventStoreMaxWALMiB))
	}
	if st.EventStoreCheckpointTimeout != nil && *st.EventStoreCheckpointTimeout != "" {
		out = append(out, varCheckpointTimeout+"="+*st.EventStoreCheckpointTimeout)
	}
	return out
}

// writeEventStoreVarFile records the flags this run was given beside the instance's state, so
// later upgrades keep them. A zero allowance removes the entry (the root derives it again).
// Called only after the apply succeeded: a rehearsal or a refused plan persists nothing.
func writeEventStoreVarFile(rootdir string, st *State) error {
	if st.EventStoreMaxWALMiB == nil && (st.EventStoreCheckpointTimeout == nil || *st.EventStoreCheckpointTimeout == "") {
		return nil
	}
	path := filepath.Join(rootdir, eventStoreVarFile)
	doc := map[string]any{}
	if b, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(b, &doc); err != nil {
			return fmt.Errorf("reading %s: %w", path, err)
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	if v := st.EventStoreMaxWALMiB; v != nil {
		if *v == 0 {
			delete(doc, varMaxWALMiB)
		} else {
			doc[varMaxWALMiB] = *v
		}
	}
	if v := st.EventStoreCheckpointTimeout; v != nil && *v != "" {
		doc[varCheckpointTimeout] = *v
	}
	if len(doc) == 0 {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		return fmt.Errorf("recording the event store tuning for instance %q: %w", st.Instance, err)
	}
	return nil
}

// walRise is an increase in the event store's max_wal_size, in MiB.
type walRise struct{ OldMiB, NewMiB int }

// parseMiB reads a Postgres size parameter ("6553MB", "1GB", "16384kB"); a bare number is MB.
func parseMiB(s string) (int, bool) {
	m := pgSizeRe.FindStringSubmatch(s)
	if m == nil {
		return 0, false
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		return 0, false
	}
	switch m[2] {
	case "kB":
		return n / 1024, true
	case "GB":
		return n * 1024, true
	case "TB":
		return n * 1024 * 1024, true
	}
	return n, true
}

// walRiseOf reads the event store release's before and after values and reports a rise in
// max_wal_size. An unset "before" is the stock allowance. Anything it cannot read is no rise:
// the plan's own diff line still names the parameter.
func walRiseOf(before, after map[string]interface{}) (walRise, bool) {
	a, ok := nestedString(after, "parameters", "max_wal_size")
	if !ok {
		return walRise{}, false
	}
	newMiB, ok := parseMiB(a)
	if !ok {
		return walRise{}, false
	}
	oldMiB := stockMaxWALMiB
	if b, ok := nestedString(before, "parameters", "max_wal_size"); ok {
		if v, ok := parseMiB(b); ok {
			oldMiB = v
		}
	}
	if newMiB <= oldMiB {
		return walRise{}, false
	}
	return walRise{OldMiB: oldMiB, NewMiB: newMiB}, true
}

// notice is the one line an upgrade says when the allowance rises.
func (r walRise) notice() string {
	return fmt.Sprintf("the event store's max_wal_size rises from %d MiB to %d MiB: pg_wal, which sits on the "+
		"event store volume, may now grow that far between checkpoints. To keep a smaller allowance, run the "+
		"upgrade with --event-store-max-wal-mib (1024 keeps the previous one)", r.OldMiB, r.NewMiB)
}

// lowSpaceWarning is the loud line for a volume with less free space than the new allowance.
func (r walRise) lowSpaceWarning(freeMiB int) string {
	return fmt.Sprintf("the event store volume has %d MiB free, less than its new max_wal_size of %d MiB. "+
		"If checkpoints fall behind or archiving stalls, pg_wal can fill the volume and PostgreSQL stops. "+
		"Free space or grow the volume first, or run the upgrade with --event-store-max-wal-mib set below "+
		"the free space (1024 keeps the previous allowance)", freeMiB, r.NewMiB)
}

// eventStoreFreeMiB reads the free space on the event store's data volume by running df in the
// primary's postgres container. Indirected so the verdict can be exercised without a cluster.
var eventStoreFreeMiB = func(ctx context.Context, st *State) (int, error) {
	restCfg, err := RestConfig(st.KubeContext)
	if err != nil {
		return 0, err
	}
	dyn, _, typed, err := kubeClients(st.KubeContext)
	if err != nil {
		return 0, err
	}
	ns := InstanceNamespace(st.Instance)
	cl, err := dyn.Resource(clusterGVR).Namespace(ns).Get(ctx, TsdbClusterName, metav1.GetOptions{})
	if err != nil {
		return 0, err
	}
	primary, _, _ := unstructured.NestedString(cl.Object, "status", "currentPrimary")
	if primary == "" {
		return 0, fmt.Errorf("the event store reports no primary")
	}
	out, stderr, err := execInPod(ctx, restCfg, typed, ns, primary, "postgres",
		[]string{"df", "-Pk", "/var/lib/postgresql/data"}, nil)
	if err != nil {
		return 0, fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr))
	}
	return parseDfFreeMiB(out)
}

// parseDfFreeMiB reads the available column of `df -Pk` output.
func parseDfFreeMiB(out string) (int, error) {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) < 2 {
		return 0, fmt.Errorf("unexpected df output %q", out)
	}
	f := strings.Fields(lines[len(lines)-1])
	if len(f) < 4 {
		return 0, fmt.Errorf("unexpected df output %q", out)
	}
	kb, err := strconv.ParseInt(f[3], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("unexpected df output %q", out)
	}
	return int(kb / 1024), nil
}

// walNotices builds the lines said about a rising allowance: always the notice, plus the
// free-space warning when the volume is below the new size, or a note when it could not be read.
func walNotices(ctx context.Context, st *State, r walRise) (notice string, warnings []string) {
	rctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	free, err := eventStoreFreeMiB(rctx, st)
	switch {
	case err != nil:
		warnings = append(warnings, fmt.Sprintf("could not read the event store volume's free space (%v); "+
			"check it against %d MiB before the upgrade", err, r.NewMiB))
	case free < r.NewMiB:
		warnings = append(warnings, r.lowSpaceWarning(free))
	}
	return r.notice(), warnings
}
