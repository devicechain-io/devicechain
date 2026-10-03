// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	assets "github.com/devicechain-io/dc-deploy"
	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

// The v0.19.0 upgrade notes tell an operator how much free CPU an upgrade needs before
// they run it ("Make room"): what the services request once upgraded, and what their
// rolling update needs at once while old and new pods run side by side. Too low a figure
// leaves the upgrade half applied, with pods Pending, on a cluster the operator was told
// was big enough. Every figure in that table is arithmetic over things this package
// renders, so this test renders them the way dcctl installs each shape and does the
// arithmetic, rather than trusting a hand sum in prose.
//
// The pages live outside this module and Go's test cache does not track them, so a
// plain `go test` can serve a stale PASS over an edit there; CI runs -count=1.

// What the services ran with in v0.18.0, the release this one upgrades from: one pod per
// area (event-management's second pod under --ha is new), each requesting the chart's
// 100m, or the --compact preset's 25m. History, so stated here rather than rendered.
const (
	v0180Replicas             = 1
	v0180ServiceRequest int64 = 100
	v0180CompactRequest int64 = 25
)

var roomPages = map[string]string{
	"en": filepath.Join("..", "..", "..", "docs", "docs", "deployment", "releases-and-upgrades.md"),
	"es": filepath.Join("..", "..", "..", "docs", "i18n", "es", "docusaurus-plugin-content-docs", "current",
		"deployment", "releases-and-upgrades.md"),
}

// areaRollout is what the rollout arithmetic needs from one area's Deployment.
type areaRollout struct {
	replicas int64
	strategy appsv1.DeploymentStrategyType
	maxSurge int64 // pods, RollingUpdate only
	cpu      int64 // millicores, of the area's own container
}

// rolloutsOf renders the chart for st and returns each named area's rollout.
func rolloutsOf(t *testing.T, st *State, areas []string) map[string]areaRollout {
	t.Helper()
	manifest, err := renderChart(t, helmValues(st))
	if err != nil {
		t.Fatalf("rendering chart: %v", err)
	}
	out := map[string]areaRollout{}
	for _, area := range areas {
		d := renderedDeployment(t, manifest, area)
		r := areaRollout{replicas: 1, strategy: d.Spec.Strategy.Type}
		if d.Spec.Replicas != nil {
			r.replicas = int64(*d.Spec.Replicas)
		}
		for _, c := range d.Spec.Template.Spec.Containers {
			if c.Name == area {
				r.cpu = c.Resources.Requests.Cpu().MilliValue()
			}
		}
		if r.cpu == 0 {
			t.Fatalf("%s: no CPU request rendered on its container: its share would read as zero", area)
		}
		switch r.strategy {
		case appsv1.RollingUpdateDeploymentStrategyType:
			ru := d.Spec.Strategy.RollingUpdate
			if ru == nil || ru.MaxSurge == nil {
				t.Fatalf("%s: RollingUpdate with no maxSurge rendered", area)
			}
			// Rounded up, as the Deployment controller rounds a percentage surge.
			s, err := intstr.GetScaledValueFromIntOrPercent(ru.MaxSurge, int(r.replicas), true)
			if err != nil {
				t.Fatalf("%s: maxSurge %v: %v", area, ru.MaxSurge, err)
			}
			r.maxSurge = int64(s)
		case appsv1.RecreateDeploymentStrategyType:
		default:
			t.Fatalf("%s: strategy %q is neither RollingUpdate nor Recreate", area, r.strategy)
		}
		out[area] = r
	}
	return out
}

// measuredAreas is the chart's set of areas with a measured request: the services whose
// requests this release raises, the five the notes' figures are for.
func measuredAreas(t *testing.T) []string {
	t.Helper()
	ch, err := loadEmbeddedChart()
	if err != nil {
		t.Fatalf("loading embedded chart: %v", err)
	}
	areas, _ := ch.Values["functionalAreas"].(map[string]interface{})
	var out []string
	for area, cfg := range areas {
		if m, ok := cfg.(map[string]interface{}); ok {
			if _, has := m["measuredRequests"]; has {
				out = append(out, area)
			}
		}
	}
	if len(out) == 0 {
		t.Fatal("the chart has no area with measuredRequests: nothing to add up")
	}
	slices.Sort(out)
	return out
}

// natsSetting is one of each NATS server's resources for st: the value dcctl passes to
// the instance root, or that variable's default there when dcctl passes none.
func natsSetting(t *testing.T, st *State, name string) string {
	t.Helper()
	for _, v := range infraVars(st) {
		if n, val, ok := strings.Cut(v, "="); ok && n == name {
			return val
		}
	}
	raw, err := fs.ReadFile(assets.OpenTofuInstance(), "variables.tf")
	if err != nil {
		t.Fatalf("reading embedded variables.tf: %v", err)
	}
	m := regexp.MustCompile(`(?s)variable\s+"` + name + `"\s*\{.*?\n\s*default\s*=\s*"([^"]+)"`).FindSubmatch(raw)
	if m == nil {
		t.Fatalf("the instance root declares no default for %s", name)
	}
	return string(m[1])
}

// natsRequests is what each NATS server requests for st.
func natsRequests(t *testing.T, st *State) (cpu, memory string) {
	t.Helper()
	return natsSetting(t, st, "nats_cpu_request"), natsSetting(t, st, "nats_memory_request")
}

// roomFigures is one row of the table, worked out from the render: millicores the
// services request beyond v0.18.0 once upgraded; what the rollout needs at once, split
// into the pods that start beside their old ones and the Recreate pods, which start
// after theirs stop and so need only the difference; and each NATS server's requests.
type roomFigures struct {
	after, besideOld, recreate int64
	natsCPU, natsMemory        string
}

func (f roomFigures) during() int64 { return f.besideOld + f.recreate }

func roomFor(t *testing.T, st *State, areas []string) roomFigures {
	t.Helper()
	old := v0180ServiceRequest
	if st.Compact {
		old = v0180CompactRequest
	}
	var f roomFigures
	for _, d := range rolloutsOf(t, st, areas) {
		f.after += d.cpu*d.replicas - old*v0180Replicas
		switch d.strategy {
		case appsv1.RollingUpdateDeploymentStrategyType:
			// The new ReplicaSet may run up to replicas+maxSurge pods in all, the old
			// ones included, and none of the old ones stops until a new one is ready.
			atOnce := min(d.replicas, d.replicas+d.maxSurge-v0180Replicas)
			f.besideOld += d.cpu * atOnce
		case appsv1.RecreateDeploymentStrategyType:
			f.recreate += d.cpu*d.replicas - old*v0180Replicas
		}
	}
	f.natsCPU, f.natsMemory = natsRequests(t, st)
	return f
}

var (
	roomDecimal  = regexp.MustCompile(`\b\d+[.,]\d+\b`)
	roomQuantity = regexp.MustCompile(`\b\d+(?:m|Mi|Gi)\b`)
	roomService  = regexp.MustCompile("`([a-z-]+)`\\s+(\\d+m|\\d+\\s+(?:core|núcleo))\\b")
	roomV0180    = regexp.MustCompile("`v0\\.18\\.0`[^.]*?\\b(\\d+m)\\b")
	// The sentence under the table giving each NATS server's requests and memory limit
	// without --compact, up to the semicolon that ends it.
	roomNATS = regexp.MustCompile(`(?:Each NATS server now requests|Cada servidor NATS solicita ahora)([^;]*);`)
)

// cores parses a figure the table gives in cores ("4.45", "4,45", "0.3") as millicores.
func cores(t *testing.T, s string) int64 {
	t.Helper()
	whole, frac, _ := strings.Cut(strings.Replace(s, ",", ".", 1), ".")
	frac = (frac + "000")[:3]
	n, err := strconv.ParseInt(whole+frac, 10, 64)
	if err != nil {
		t.Fatalf("parsing %q as cores: %v", s, err)
	}
	return n
}

// TestUpgradeNotesMakeRoomIsTheRenderedArithmetic holds the "Make room" table of the
// v0.19.0 notes, in both locales, to the requests, replica counts and rollout strategies
// dcctl renders for --ha, a plain install and --compact.
func TestUpgradeNotesMakeRoomIsTheRenderedArithmetic(t *testing.T) {
	areas := measuredAreas(t)
	ha := haState(true)
	plain := haState(false)
	compact := compactState(true)
	want := map[string]roomFigures{
		"ha":      roomFor(t, ha, areas),
		"plain":   roomFor(t, plain, areas),
		"compact": roomFor(t, compact, areas),
	}
	planned := rolloutsOf(t, plain, areas)

	for locale, path := range roomPages {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("reading the %s upgrade notes: %v", locale, err)
		}
		page := string(raw)
		start := strings.Index(page, "{#v0190-room}")
		if start < 0 {
			t.Fatalf("%s: no {#v0190-room} section", locale)
		}
		section := page[start:]
		if end := strings.Index(section, "\n#####"); end >= 0 {
			section = section[:end]
		}

		seen := map[string]bool{}
		for _, line := range strings.Split(section, "\n") {
			if !strings.HasPrefix(line, "| ") || strings.HasPrefix(line, "| ---") {
				continue
			}
			cells := strings.Split(strings.Trim(line, "| "), " | ")
			if len(cells) != 3 || !strings.Contains(cells[0], "--") {
				continue // the header row
			}
			var row string
			switch {
			case strings.Contains(cells[0], "--compact"):
				row = "compact"
			case strings.TrimSpace(cells[0]) == "`--ha`":
				row = "ha"
			default:
				row = "plain"
			}
			seen[row] = true
			w := want[row]

			gotCores := func(cell string) []int64 {
				var out []int64
				for _, d := range roomDecimal.FindAllString(cell, -1) {
					out = append(out, cores(t, d))
				}
				return out
			}
			// A figure of zero is written as words ("none"), not a number, and the split
			// of the rollout figure is given only when it has two parts.
			var wantAfter, wantDuring []int64
			if w.after != 0 {
				wantAfter = []int64{w.after}
			}
			switch {
			case w.recreate != 0:
				wantDuring = []int64{w.during(), w.besideOld, w.recreate}
			case w.during() != 0:
				wantDuring = []int64{w.during()}
			}
			if got := gotCores(cells[1]); !slices.Equal(got, wantAfter) {
				t.Errorf("%s, %s row: once upgraded the table gives %v millicores, the render gives %v",
					locale, row, got, wantAfter)
			}
			if got := gotCores(cells[2]); !slices.Equal(got, wantDuring) {
				t.Errorf("%s, %s row: during the roll the table gives %v millicores (total, beside the "+
					"old pods, Recreate), the render gives %v", locale, row, got, wantDuring)
			}
			if got, wantQ := roomQuantity.FindAllString(cells[1], -1), []string{w.natsCPU, w.natsMemory}; !slices.Equal(got, wantQ) {
				t.Errorf("%s, %s row: each NATS server requests %v by the table, %v by the instance root",
					locale, row, got, wantQ)
			}
		}
		for _, row := range []string{"ha", "plain", "compact"} {
			if !seen[row] {
				t.Errorf("%s: the Make room table has no %s row this test can read", locale, row)
			}
		}

		// The per-service requests the paragraph under the table lists, and the one
		// every service had before.
		listed := map[string]int64{}
		for _, m := range roomService.FindAllStringSubmatch(section, -1) {
			// "800m" as written; "1 core" / "1 núcleo" as the whole number of cores.
			listed[m[1]] = q(t, "cpu", strings.Fields(m[2])[0])
		}
		if got := slices.Sorted(maps.Keys(listed)); !slices.Equal(got, areas) {
			t.Errorf("%s: the notes list new requests for %v, the chart measures %v", locale, got, areas)
		}
		for _, area := range areas {
			if got, w := listed[area], planned[area].cpu; got != w {
				t.Errorf("%s: the notes give %s a %dm request, the chart renders %dm", locale, area, got, w)
			}
		}

		// The paragraph restates the table's NATS requests and adds the memory limit,
		// which the table does not give; both are the instance root's, without --compact.
		wantNATS := []string{
			natsSetting(t, plain, "nats_cpu_request"),
			natsSetting(t, plain, "nats_memory_request"),
			natsSetting(t, plain, "nats_memory_limit"),
		}
		if m := roomNATS.FindStringSubmatch(section); m == nil {
			t.Errorf("%s: no sentence giving each NATS server's requests and limit this test can read", locale)
		} else if got := roomQuantity.FindAllString(m[1], -1); !slices.Equal(got, wantNATS) {
			t.Errorf("%s: the notes give each NATS server %v (CPU, memory, memory limit), the instance root %v",
				locale, got, wantNATS)
		}
		if m := roomV0180.FindStringSubmatch(section); m == nil || q(t, "cpu", m[1]) != v0180ServiceRequest {
			t.Errorf("%s: the notes give the v0.18.0 request as %v, this test adds up from %dm",
				locale, m, v0180ServiceRequest)
		}
	}
}
